"""bench.run's orchestration core (R8.4): for every (scenario, arm) pair,
retrieve -> answer -> score (retrieval/hallucination/temporal_order/token
usage), writing one row to results.jsonl per pair as it completes and a
run_manifest.json + summary.json once the whole sweep finishes.

Resume (R8.4's "resume 可能"): results.jsonl already containing a
(scenario_id, arm) pair is loaded up front (load_completed_keys) and every
already-present pair is skipped without calling the LLM again — the whole
point being billing protection on a real API run that died partway through
and is re-invoked with the same --out directory.

bench.run never imports internal/engine directly (R8.5): all patient/Bead
access goes through bench.ingest.mcp_client.MedBeadsClient, same as every
other bench/ package.
"""

from __future__ import annotations

import hashlib
import json
import logging
import re
import subprocess
from collections.abc import Iterator
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

from bench.ingest.mcp_client import MedBeadsClient
from bench.llm.base import AnswerResult, LlmClient
from bench.llm.claude import PROMPT_TEMPLATE_VERSION as ANSWER_PROMPT_VERSION
from bench.metrics.hallucination import JUDGE_PROMPT_TEMPLATE_VERSION, HallucinationScore, score_hallucination
from bench.metrics.temporal import TEMPORAL_ANSWER_PROMPT_SUFFIX, TemporalOrderScore, score_temporal_order
from bench.metrics.token import TokenUsage, aggregate_token_usage, token_usage_from_answer
from bench.perf.stats import compute_latency_stats
from bench.retrieval.base import RetrievalResult, Retriever
from bench.retrieval.dag import ARM_DAG, DagRetriever
from bench.retrieval.fts import FtsRetriever
from bench.retrieval.metrics import RetrievalScore, score_retrieval
from bench.retrieval.rag import RagRetriever
from bench.retrieval.render import render_l0
from bench.scenarios.generate import CATEGORY_TEMPORAL_ORDER
from bench.scenarios.model import Scenario

logger = logging.getLogger(__name__)

ARM_RAG = "rag"
ARM_FTS = "fts"
# U6 (specs/U6_clinical_note.md): dag_nosib/dag_full consolidated into a
# single `dag` arm — see bench.retrieval.dag's module docstring for why
# (U5a removed graph's sibling tiers entirely, so the two arms had measured
# identical numbers since then).
ALL_ARMS = (ARM_RAG, ARM_FTS, ARM_DAG)

DEFAULT_BUDGET = 2000

# fts/search_beads/retrieve's own anchor search all route through SQLite
# FTS5 MATCH underneath (internal/engine/index/read.go), whose default
# (unicode61) query-string parser gives special meaning to punctuation
# (', -, ", :, ...) — see bench/tests/retrieval/test_4arm_integration.py's
# own _fts_safe_query docstring for the VERIFIED real-server error this
# produces on scenario.question (a full, punctuated Japanese sentence) fed
# unquoted. This module's scenario questions ALWAYS have this shape
# (bench.scenarios.generate's four templates are all full sentences), so
# run_bench cannot pass scenario.question to the fts/dag arms as-is without
# hitting the same real error every single call — this
# helper is promoted from that test-only workaround into production code
# for exactly that reason. This is a known, reported limitation of R8.2's
# arms, not a fix to index.Search's own MATCH-string escaping (out of this
# task's scope) — a real run's fts/dag_* arms are only ever tested against
# a single representative word of the question, not the full question text
# a user would actually type.
#
# RunConfig.fts_query_mode (lead ruling, data-reviewer follow-up) controls
# whether rag gets the full free-text question or the *same* reduced
# fts_safe_query word every other arm is forced to use:
#
#   - "safe_word" (default): rag = full free-text question (it is pure
#     vector search, rag_search, and does not go through FTS5 MATCH at
#     all, so it never needed the reduction); fts/dag =
#     fts_safe_query(question). This is the original, higher-fidelity-for-
#     rag behavior and the right default for a single-arm sanity run.
#   - "shared_safe_word": every arm, including rag, gets
#     fts_safe_query(question) — an input-matched control run: since rag's
#     query is then identical in *content* to every FTS5-bound arm's query,
#     any recall/precision/hallucination gap between rag and the other
#     three arms in that run cannot be attributed to rag simply having
#     received a richer query string than the others did. Any full M2 paper
#     run should collect **both** conditions and report the FTS5-syntax-
#     driven query-asymmetry between arms as a stated Limitations-section
#     caveat (a real, load-bearing methodology point: "safe_word" alone
#     cannot separate "arm X is a better retriever" from "arm X got a
#     better query"; "shared_safe_word" alone cannot separate "rag is a
#     better retriever" from "rag would have done even better with its
#     normal full-text query" — the two conditions together, not either one
#     alone, are what supports a fair arm-vs-arm claim).
FTS_QUERY_MODE_SAFE_WORD = "safe_word"
FTS_QUERY_MODE_SHARED_SAFE_WORD = "shared_safe_word"
_VALID_FTS_QUERY_MODES = frozenset({FTS_QUERY_MODE_SAFE_WORD, FTS_QUERY_MODE_SHARED_SAFE_WORD})

_WORD_RE = re.compile(r"[A-Za-z]+")
_MIN_WORD_LEN = 4


def fts_safe_query(text: str) -> str:
    """The longest run of >= _MIN_WORD_LEN ASCII letters in text, lowercased
    — a FTS5-MATCH-safe stand-in for a full free-text question (see this
    module's docstring above). Falls back to "encounter" (present in every
    ingested patient's Bead set as an fhir_encounter type) if text has no
    such word, so this never returns an empty string."""
    words = [w for w in _WORD_RE.findall(text) if len(w) >= _MIN_WORD_LEN]
    if not words:
        return "encounter"
    return max(words, key=len).lower()


def _retrieval_query_for_arm(question: str, arm: str, fts_query_mode: str) -> str:
    """The query text passed to a given arm's retrieve() call, per
    fts_query_mode (see this module's docstring above
    FTS_QUERY_MODE_SAFE_WORD)."""
    if fts_query_mode not in _VALID_FTS_QUERY_MODES:
        raise ValueError(
            f"_retrieval_query_for_arm: unknown fts_query_mode {fts_query_mode!r} "
            f"(expected one of {sorted(_VALID_FTS_QUERY_MODES)})"
        )
    if arm == ARM_RAG and fts_query_mode == FTS_QUERY_MODE_SAFE_WORD:
        return question
    return fts_safe_query(question)


@dataclass
class RunConfig:
    """Every knob run_bench needs, gathered in one place so config_hash
    (R8.4's "config hash") is a hash of exactly this dataclass's own
    contents — see config_hash() below."""

    scenarios_path: Path
    data_dir: Path
    medbeadsd_path: Path
    out_dir: Path
    arms: tuple[str, ...] = ALL_ARMS
    budget: int = DEFAULT_BUDGET
    embedder_url: str | None = None
    embed_model: str | None = None
    embed_model_query: str | None = None
    run_judge: bool = True
    # "safe_word" (default) or "shared_safe_word" — see this module's
    # docstring above FTS_QUERY_MODE_SAFE_WORD for the full rationale and
    # the lead's "本走時は両条件を取る" (a full run should collect both)
    # ruling.
    fts_query_mode: str = FTS_QUERY_MODE_SAFE_WORD

    def config_hash(self) -> str:
        payload = {
            "arms": list(self.arms),
            "budget": self.budget,
            "embedder_url": self.embedder_url,
            "embed_model": self.embed_model,
            "embed_model_query": self.embed_model_query,
            "run_judge": self.run_judge,
            "fts_query_mode": self.fts_query_mode,
        }
        return hashlib.sha256(json.dumps(payload, sort_keys=True).encode("utf-8")).hexdigest()[:16]


@dataclass
class ArmResult:
    """One (scenario_id, arm) row's full result set — results.jsonl's own
    schema, one JSON object per line."""

    scenario_id: str
    patient_id: str
    category: str
    reasoning_type: str
    arm: str
    question: str
    ground_truth_answer: str
    llm_answer: str
    retrieval: RetrievalResult
    retrieval_score: RetrievalScore
    token_usage: TokenUsage
    hallucination: HallucinationScore | None
    temporal_order: TemporalOrderScore | None

    def to_json_dict(self) -> dict[str, Any]:
        return {
            "scenario_id": self.scenario_id,
            "patient_id": self.patient_id,
            "category": self.category,
            "reasoning_type": self.reasoning_type,
            "arm": self.arm,
            "question": self.question,
            "ground_truth_answer": self.ground_truth_answer,
            "llm_answer": self.llm_answer,
            "retrieval": self.retrieval.to_json_dict(),
            "retrieval_score": self.retrieval_score.to_json_dict(),
            "token_usage": self.token_usage.to_json_dict(),
            "hallucination": self.hallucination.to_json_dict() if self.hallucination else None,
            "temporal_order": self.temporal_order.to_json_dict() if self.temporal_order else None,
        }


def load_completed_keys(results_path: Path) -> set[tuple[str, str]]:
    """Every (scenario_id, arm) pair already present in results_path — the
    resume contract's read side. Missing file / empty file yields an empty
    set (a fresh run, not an error)."""
    completed: set[tuple[str, str]] = set()
    if not results_path.is_file():
        return completed
    with results_path.open("r", encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            row = json.loads(line)
            completed.add((row["scenario_id"], row["arm"]))
    return completed


def _iter_results_rows(results_path: Path) -> Iterator[dict[str, Any]]:
    """Every row of results_path, streamed one at a time (never all-rows-
    in-a-list) — the single source of truth _aggregate_by_arm reads from at
    the end of run_bench, per data-reviewer's blocker fix: summary.json
    must reflect the *whole* completed sweep (every pair ever written to
    results_path, across every invocation that ever appended to it), never
    just the pairs this particular run_bench call happened to score, since
    a resumed run's in-memory accumulators only ever see the delta it
    itself ran."""
    if not results_path.is_file():
        return
    with results_path.open("r", encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            yield json.loads(line)


def _aggregate_by_arm(results_path: Path, arms: tuple[str, ...]) -> dict[str, dict[str, Any]]:
    """Re-derives every by_arm summary.json field from results_path's full
    contents (re-read from disk, not from any in-run accumulator) — the
    fix for data-reviewer's resume blocker: results.jsonl is the single
    source of truth for summary.json, always, whether this invocation
    scored 0 pairs (fully resumed) or every pair (a fresh complete run).

    latency_s is re-derived from each row's persisted
    retrieval.latency_ms (RetrievalResult.latency_ms, already written to
    every results.jsonl row) — this is *retrieve-call* latency only, not
    the full retrieve+answer(+judge) wall time run_bench's in-run
    accumulator used to measure (that per-pair wall clock is not persisted
    to any row field and so cannot be re-derived from disk after the fact;
    re-timing on resume/summary-only invocations is impossible without
    re-running the call). This is a deliberate, narrower-but-reproducible
    substitute: any summary.json produced by this function (whether from a
    single complete run or a resumed one) reports the exact same numbers
    for the exact same results.jsonl contents, which the reviewer's fix
    requires; the old broader wall-time number could not offer that
    guarantee across resumed runs.
    """
    retrieval_recalls: dict[str, list[float]] = {a: [] for a in arms}
    retrieval_precisions: dict[str, list[float]] = {a: [] for a in arms}
    token_usages: dict[str, list[TokenUsage]] = {a: [] for a in arms}
    retrieval_latencies_s: dict[str, list[float]] = {a: [] for a in arms}
    hallucination_rates: dict[str, list[float]] = {a: [] for a in arms}
    temporal_agreements: dict[str, list[bool]] = {a: [] for a in arms}
    row_count: dict[str, int] = {a: 0 for a in arms}

    for row in _iter_results_rows(results_path):
        arm = row["arm"]
        if arm not in retrieval_recalls:
            # A results.jsonl row for an arm outside this call's `arms`
            # tuple (e.g. a prior run covered more arms than this
            # invocation was asked to summarize) — counted nowhere in this
            # arms-scoped summary, same as it would be invisible to a
            # fresh run configured with this same `arms` tuple.
            continue
        row_count[arm] += 1
        retrieval_score = row.get("retrieval_score") or {}
        if "recall" in retrieval_score:
            retrieval_recalls[arm].append(retrieval_score["recall"])
        if "precision" in retrieval_score:
            retrieval_precisions[arm].append(retrieval_score["precision"])
        token_usage = row.get("token_usage") or {}
        token_usages[arm].append(
            TokenUsage(
                input_tokens=token_usage.get("input_tokens", 0),
                output_tokens=token_usage.get("output_tokens", 0),
            )
        )
        retrieval = row.get("retrieval") or {}
        if "latency_ms" in retrieval:
            retrieval_latencies_s[arm].append(retrieval["latency_ms"] / 1000.0)
        hallucination = row.get("hallucination")
        if hallucination is not None and "hallucination_rate" in hallucination:
            hallucination_rates[arm].append(hallucination["hallucination_rate"])
        temporal_order = row.get("temporal_order")
        if temporal_order is not None and "agrees" in temporal_order:
            temporal_agreements[arm].append(bool(temporal_order["agrees"]))

    by_arm: dict[str, dict[str, Any]] = {}
    for arm in arms:
        usage_total = aggregate_token_usage(token_usages[arm])
        recalls = retrieval_recalls[arm]
        precisions = retrieval_precisions[arm]
        by_arm[arm] = {
            "pairs": row_count[arm],
            "scored_this_run": row_count[arm],
            "mean_recall": (sum(recalls) / len(recalls)) if recalls else None,
            "mean_precision": (sum(precisions) / len(precisions)) if precisions else None,
            "token_usage_total": usage_total.to_json_dict(),
            "latency_s": (
                compute_latency_stats(retrieval_latencies_s[arm]).to_json_dict()
                if retrieval_latencies_s[arm]
                else None
            ),
            "mean_hallucination_rate": (
                sum(hallucination_rates[arm]) / len(hallucination_rates[arm])
                if hallucination_rates[arm]
                else None
            ),
            "temporal_order_agreement_rate": (
                sum(1 for a in temporal_agreements[arm] if a) / len(temporal_agreements[arm])
                if temporal_agreements[arm]
                else None
            ),
        }
    return by_arm


def _git_commit(repo_dir: Path) -> str:
    """Mirrors bench.ingest.run._git_commit / bench.perf.run._git_commit:
    best-effort provenance, never fatal."""
    try:
        out = subprocess.run(
            ["git", "-C", str(repo_dir), "rev-parse", "HEAD"],
            capture_output=True,
            text=True,
            check=True,
            timeout=5,
        )
        return out.stdout.strip()
    except Exception:  # noqa: BLE001 - provenance best-effort, never blocks the run
        return "unknown"


def _dataset_fingerprint(data_dir: Path) -> str:
    """sha256 of data_dir/manifest.jsonl's bytes (R8.4's "データセット指紋
    (manifest.jsonl の sha256)") — "unknown" if the file is absent (e.g. a
    hand-built scratch dir with no bench.ingest manifest), never fatal."""
    manifest_path = data_dir / "manifest.jsonl"
    if not manifest_path.is_file():
        return "unknown"
    digest = hashlib.sha256()
    with manifest_path.open("rb") as f:
        for chunk in iter(lambda: f.read(65536), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _build_retriever(arm: str, client: MedBeadsClient) -> Retriever:
    if arm == ARM_RAG:
        return RagRetriever(client)
    if arm == ARM_FTS:
        return FtsRetriever(client)
    if arm == ARM_DAG:
        return DagRetriever(client)
    raise ValueError(f"_build_retriever: unknown arm {arm!r} (expected one of {ALL_ARMS})")


class _FullRecordCache:
    """Per-patient full-record text cache (bead_id -> rendered L0 text),
    fetched via get_timeline + get_bead (MCP-only, R8.5) — the hallucination
    judge's FULL_RECORD pool (see bench.metrics.hallucination's docstring:
    "context_out_all_in" needs every Bead's text, not just what an arm
    retrieved). Cached per patient_id across every (scenario, arm) pair
    sharing that patient within one run_bench call, since a patient's
    record does not change mid-run and re-fetching it once per arm (4x)
    would be pure waste, not correctness risk — this is a performance
    optimization only, never load-bearing.

    Cost/scale caveat (see bench/README.md's own "Cost/scale caveat"
    section): every Bead in the patient's timeline is rendered here with
    no truncation and no token budget — for a patient with a large
    timeline this can produce a very large judge prompt (context-window
    risk on the judge model, and per-scenario judge-call cost that scales
    with timeline size, not with the answer being judged). `run_judge=False`
    (CLI: `--no-judge`) skips this entirely; a budgeted/sampled FULL_RECORD
    is not implemented yet.
    """

    def __init__(self, client: MedBeadsClient) -> None:
        self._client = client
        self._cache: dict[str, list[str]] = {}

    async def get(self, patient_id: str) -> list[str]:
        if patient_id in self._cache:
            return self._cache[patient_id]
        timeline = await self._client.get_timeline(patient_id)
        texts: list[str] = []
        for ref in timeline:
            bead = await self._client.get_bead(ref["id"])
            texts.append(render_l0(bead.get("type", ""), bead.get("content", {})))
        self._cache[patient_id] = texts
        return texts


async def _run_one(
    *,
    scenario: Scenario,
    arm: str,
    client: MedBeadsClient,
    llm: LlmClient,
    judge: LlmClient | None,
    full_record_cache: _FullRecordCache,
    budget: int,
    fts_query_mode: str = FTS_QUERY_MODE_SAFE_WORD,
) -> ArmResult:
    retriever = _build_retriever(arm, client)
    # fts/dag always route through FTS5 MATCH underneath and need
    # fts_safe_query's single-word form; rag's own query depends on
    # fts_query_mode — see this module's docstring above
    # FTS_QUERY_MODE_SAFE_WORD / _retrieval_query_for_arm.
    retrieval_query = _retrieval_query_for_arm(scenario.question, arm, fts_query_mode)
    result = await retriever.retrieve(question=retrieval_query, patient_id=scenario.patient_id, budget=budget)
    retrieval_score = score_retrieval(result, scenario.evidence_bead_ids)

    is_temporal = scenario.category == CATEGORY_TEMPORAL_ORDER
    answer: AnswerResult
    temporal_score: TemporalOrderScore | None = None
    if is_temporal:
        # Ground-truth answer is always the *earlier* event's display name
        # (bench.scenarios.generate._generate_temporal_order) — assign it
        # arbitrarily but deterministically to option A, the *other* named
        # event (parsed back out of the question string's fixed
        # "'X' と 'Y' はどちらが先か?" shape) to option B, per
        # bench.metrics.temporal's forced-choice contract.
        option_a, option_b = _temporal_options_from_question(scenario.question)
        correct_choice = "A" if option_a == scenario.answer else "B"
        prompt_suffix = TEMPORAL_ANSWER_PROMPT_SUFFIX.format(option_a=option_a, option_b=option_b)
        answer = await llm.complete(
            system="",
            user=_temporal_prompt(scenario.question, result.texts, prompt_suffix),
            scenario_id=scenario.scenario_id,
            arm=arm,
            call_kind="answer",
        )
        temporal_score = score_temporal_order(answer.answer_text, correct_choice=correct_choice)
    else:
        answer = await llm.answer(
            question=scenario.question,
            context_texts=result.texts,
            scenario_id=scenario.scenario_id,
            arm=arm,
        )

    hallucination_score: HallucinationScore | None = None
    if judge is not None:
        full_record_texts = await full_record_cache.get(scenario.patient_id)
        hallucination_score = await score_hallucination(
            judge,
            question=scenario.question,
            answer_text=answer.answer_text,
            retrieved_context_texts=result.texts,
            full_record_texts=full_record_texts,
            scenario_id=scenario.scenario_id,
            arm=arm,
        )

    return ArmResult(
        scenario_id=scenario.scenario_id,
        patient_id=scenario.patient_id,
        category=scenario.category,
        reasoning_type=scenario.reasoning_type,
        arm=arm,
        question=scenario.question,
        ground_truth_answer=scenario.answer,
        llm_answer=answer.answer_text,
        retrieval=result,
        retrieval_score=retrieval_score,
        token_usage=token_usage_from_answer(answer),
        hallucination=hallucination_score,
        temporal_order=temporal_score,
    )


def _temporal_options_from_question(question: str) -> tuple[str, str]:
    """Parses "'X' と 'Y' はどちらが先か?" back into (X, Y) — the exact
    inverse of bench.scenarios.generate._generate_temporal_order's own
    question-string construction (f"'{earlier[1]}' と '{later[1]}' は
    どちらが先か?"). Raises ValueError if question does not match that
    fixed shape (a caller passing a non-temporal_order scenario through
    this path is a pipeline bug, not a recoverable data issue)."""
    match = re.match(r"^'(.*)' と '(.*)' はどちらが先か\?$", question)
    if not match:
        raise ValueError(f"_temporal_options_from_question: unexpected temporal_order question shape: {question!r}")
    return match.group(1), match.group(2)


def _temporal_prompt(question: str, context_texts: list[str], suffix: str) -> str:
    from bench.llm.claude import render_answer_prompt

    return render_answer_prompt(question, context_texts) + suffix


@dataclass
class RunReport:
    """run_bench's return value. completed_pairs/skipped_pairs describe
    only *this invocation* (how many pairs it actually called the LLM for
    vs. skipped via resume) — for the full-sweep-to-date totals (every
    pair ever written to results.jsonl, across every invocation), sum
    by_arm[arm]["pairs"] across arms, or read run_manifest.json's own
    by_arm (same numbers — see run_bench's _aggregate_by_arm call, which
    always re-derives by_arm from results.jsonl's complete on-disk
    contents, never from this invocation's own accumulator)."""

    started_at: str
    finished_at: str
    total_pairs: int
    completed_pairs: int
    skipped_pairs: int
    git_commit: str
    config_hash: str
    dataset_fingerprint: str
    model: str
    judge_model: str | None
    answer_prompt_version: str
    judge_prompt_version: str
    by_arm: dict[str, dict[str, Any]] = field(default_factory=dict)

    def to_json_dict(self) -> dict[str, Any]:
        return {
            "started_at": self.started_at,
            "finished_at": self.finished_at,
            "total_pairs": self.total_pairs,
            "completed_pairs": self.completed_pairs,
            "skipped_pairs": self.skipped_pairs,
            "git_commit": self.git_commit,
            "config_hash": self.config_hash,
            "dataset_fingerprint": self.dataset_fingerprint,
            "model": self.model,
            "judge_model": self.judge_model,
            "answer_prompt_version": self.answer_prompt_version,
            "judge_prompt_version": self.judge_prompt_version,
            "by_arm": self.by_arm,
        }


async def run_bench(
    config: RunConfig,
    *,
    llm: LlmClient,
    judge: LlmClient | None = None,
    repo_dir: Path | None = None,
) -> RunReport:
    """The full sweep: every scenario in config.scenarios_path x every arm
    in config.arms, resume-aware, writing results.jsonl as it goes and
    run_manifest.json + summary.json once done.

    judge=None disables hallucination scoring entirely (every ArmResult's
    hallucination field is None) — a legitimate, honestly-reported
    configuration (e.g. a cheaper run that only needs retrieval/token
    metrics), not an error.
    """
    from bench.scenarios.model import load_scenarios_yaml

    scenarios = load_scenarios_yaml(config.scenarios_path)
    config.out_dir.mkdir(parents=True, exist_ok=True)
    results_path = config.out_dir / "results.jsonl"
    run_manifest_path = config.out_dir / "run_manifest.json"
    summary_path = config.out_dir / "summary.json"

    completed = load_completed_keys(results_path)
    total_pairs = len(scenarios) * len(config.arms)

    started_at = datetime.now(timezone.utc).isoformat()

    skipped = 0
    completed_now = 0

    async with MedBeadsClient(
        config.medbeadsd_path,
        config.data_dir,
        role="viewer",
        embedder_url=config.embedder_url,
        embed_model=config.embed_model,
        embed_model_query=config.embed_model_query,
    ) as client:
        full_record_cache = _FullRecordCache(client)

        with results_path.open("a", encoding="utf-8") as results_file:
            for scenario in scenarios:
                for arm in config.arms:
                    key = (scenario.scenario_id, arm)
                    if key in completed:
                        skipped += 1
                        continue

                    arm_result = await _run_one(
                        scenario=scenario,
                        arm=arm,
                        client=client,
                        llm=llm,
                        judge=judge if config.run_judge else None,
                        full_record_cache=full_record_cache,
                        budget=config.budget,
                        fts_query_mode=config.fts_query_mode,
                    )

                    results_file.write(json.dumps(arm_result.to_json_dict(), sort_keys=True, ensure_ascii=False))
                    results_file.write("\n")
                    results_file.flush()
                    completed_now += 1

    finished_at = datetime.now(timezone.utc).isoformat()

    # summary.json's by_arm is always re-derived from results_path's full,
    # on-disk contents (data-reviewer's resume-summary blocker fix) — never
    # from an in-run accumulator, so a resumed run's summary reflects the
    # *entire* completed sweep (every pair ever written across every
    # invocation), not just the pairs this particular call scored.
    by_arm = _aggregate_by_arm(results_path, config.arms)

    report = RunReport(
        started_at=started_at,
        finished_at=finished_at,
        total_pairs=total_pairs,
        completed_pairs=completed_now,
        skipped_pairs=skipped,
        git_commit=_git_commit(repo_dir or Path(__file__).resolve().parents[3]),
        config_hash=config.config_hash(),
        dataset_fingerprint=_dataset_fingerprint(config.data_dir),
        model=llm.model,
        judge_model=judge.model if (judge is not None and config.run_judge) else None,
        answer_prompt_version=ANSWER_PROMPT_VERSION,
        judge_prompt_version=JUDGE_PROMPT_TEMPLATE_VERSION if (judge is not None and config.run_judge) else "",
        by_arm=by_arm,
    )

    run_manifest = {
        **report.to_json_dict(),
        "scenarios_path": str(config.scenarios_path),
        "data_dir": str(config.data_dir),
        "out_dir": str(config.out_dir),
        "arms": list(config.arms),
        "budget": config.budget,
        "fts_query_mode": config.fts_query_mode,
        "total_scenarios": len(scenarios),
    }
    with run_manifest_path.open("w", encoding="utf-8") as f:
        json.dump(run_manifest, f, indent=2, sort_keys=True)
        f.write("\n")

    with summary_path.open("w", encoding="utf-8") as f:
        json.dump({"by_arm": by_arm}, f, indent=2, sort_keys=True)
        f.write("\n")

    return report
