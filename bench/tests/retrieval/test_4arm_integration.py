"""Integration test: real scratch ingest (--limit 10) + a real
(fake-encoder-backed) embedding sidecar, then all 3 retrieval arms
(rag/fts/dag) against the same scenario, checking every arm returns a
RetrievalResult for the same (question, patient_id, budget) input.

U6 consolidation (specs/U6_clinical_note.md, docs/decisions.md 2026-07-11 U6
entry): this module used to run 4 arms (rag/fts/dag_nosib/dag_full) and
additionally verify dag_full-vs-dag_nosib diverged for a patient with a real
apc_trigger-produced sibling_link Bead. U5a (specs/U5_api_retrieve.md)
removed package apc, the apc_trigger MCP tool, and graph's sibling tiers
entirely (internal/mcpserver/tools_read_test.go's U5a regression test
asserts apc_trigger is no longer registered) — calling it now errors, and
there is no longer a sibling-link mechanism to build a difference test
around. dag_nosib/dag_full were consolidated into a single `dag` arm in U6
(see bench.retrieval.dag's own docstring for the VERIFIED reasoning: the
two arms measured identical numbers since U5a landed). This test module was
rewritten to exercise the current 3-arm set only, with no apc_trigger/
sibling_link setup at all.

Requires the `go` toolchain and the real Synthea dataset (see
tests/conftest.py) — skipped automatically if either is unavailable. Never
touches ~/medbeads-synthea/medbeads_data (the busy real store) — every
ingest here goes into tmp_path via --limit, per this task's scratch-data
mandate.
"""

from __future__ import annotations

import asyncio
import re
from pathlib import Path

from bench.ingest.mcp_client import MedBeadsClient
from bench.ingest.run import run_ingest
from bench.retrieval.dag import DagRetriever
from bench.retrieval.fts import FtsRetriever
from bench.retrieval.metrics import score_retrieval
from bench.retrieval.rag import RagRetriever
from bench.scenarios.generate import generate_scenarios

REPO_ROOT = Path(__file__).resolve().parents[3]

# FTS5's default (unicode61) query-string parser gives special meaning to
# punctuation (', -, ", :, ...) — see bench/bench/perf/queries.py's own
# _WORD_RE/_longest_word doc comment for the VERIFIED real-server error this
# produces on an unquoted multi-word/punctuated string. This test's own
# scenario questions are full sentences (Japanese question text), so every
# fts/dag arm query used here reduces to one safe plain-ASCII-letters word
# first — the exact same constraint any real fts/dag caller (including a
# future LLM-driven bench run, feeding bench.scenarios' own generated
# question text straight in) will need to satisfy; this is not a test-only
# workaround, and is reported as a real caveat in this unit's final report.
_WORD_RE = re.compile(r"[A-Za-z]+")
_MIN_WORD_LEN = 4


def _fts_safe_query(text: str) -> str:
    words = [w for w in _WORD_RE.findall(text) if len(w) >= _MIN_WORD_LEN]
    if not words:
        return "encounter"  # universal fallback: every scratch patient has fhir_encounter Beads
    return max(words, key=len).lower()


async def _ingest_scratch(
    tmp_path: Path, medbeadsd_binary: Path, synthea_fhir_dir: Path, *, limit: int
) -> tuple[Path, Path]:
    data_dir = tmp_path / "data"
    manifest_path = tmp_path / "manifest.jsonl"
    run_manifest_path = tmp_path / "run_manifest.json"
    summary = await run_ingest(
        fhir_dir=synthea_fhir_dir,
        data_dir=data_dir,
        medbeadsd_path=medbeadsd_binary,
        manifest_path=manifest_path,
        run_manifest_path=run_manifest_path,
        limit=limit,
    )
    assert summary.ok_patients == limit, f"scratch ingest: {summary.failures}"
    return data_dir, manifest_path


def _run_embed_backfill(medbeadsd_binary: Path, data_dir: Path, embedder_url: str) -> None:
    """One-shot `medbeadsd embed -data <dir> -embedder <url>` CLI backfill
    (a standalone subcommand, not an MCP tool — same tier as building the
    medbeadsd binary itself: infrastructure this test needs to set up
    before a `serve -embedder` session can do semantic search over data
    that already exists, mirroring bench/README.md's own documented
    invocation)."""
    import subprocess

    result = subprocess.run(
        [str(medbeadsd_binary), "embed", "-data", str(data_dir), "-embedder", embedder_url],
        capture_output=True,
        text=True,
        timeout=60,
    )
    assert result.returncode == 0, f"medbeadsd embed failed:\nstdout={result.stdout}\nstderr={result.stderr}"


def test_all_three_arms_return_a_retrieval_result_for_the_same_scenario(
    tmp_path: Path,
    medbeadsd_binary: Path,
    synthea_fhir_dir: Path,
    fake_embed_sidecar_url: str,
) -> None:
    asyncio.run(_run_all_three_arms(tmp_path, medbeadsd_binary, synthea_fhir_dir, fake_embed_sidecar_url))


async def _run_all_three_arms(
    tmp_path: Path,
    medbeadsd_binary: Path,
    synthea_fhir_dir: Path,
    fake_embed_sidecar_url: str,
) -> None:
    data_dir, manifest_path = await _ingest_scratch(tmp_path, medbeadsd_binary, synthea_fhir_dir, limit=10)
    _run_embed_backfill(medbeadsd_binary, data_dir, fake_embed_sidecar_url)

    scenarios = generate_scenarios(fhir_dir=synthea_fhir_dir, manifest_path=manifest_path)
    assert scenarios, "scratch ingest should produce at least one scenario"
    scenario = scenarios[0]
    # rag (pure vector) can safely use the full free-text question; fts/dag
    # arms route through FTS5 MATCH underneath (search_beads / retrieve's
    # anchor search), which cannot tolerate raw punctuation/multi-word text
    # unquoted — see this module's _fts_safe_query doc comment.
    fts_query = _fts_safe_query(scenario.question)

    budget = 2000

    async with MedBeadsClient(
        medbeadsd_binary, data_dir, role="viewer", embedder_url=fake_embed_sidecar_url
    ) as client:
        rag = RagRetriever(client)
        fts = FtsRetriever(client)
        dag = DagRetriever(client)

        for retriever, question in (
            (rag, scenario.question),
            (fts, fts_query),
            (dag, fts_query),
        ):
            result = await retriever.retrieve(
                question=question, patient_id=scenario.patient_id, budget=budget
            )
            assert result.arm == retriever.arm
            assert len(result.bead_ids) == len(result.texts), (
                f"{retriever.arm}: bead_ids/texts length mismatch"
            )
            assert result.used_tokens <= budget, f"{retriever.arm}: used_tokens exceeds budget"
            assert result.latency_ms >= 0.0

            score = score_retrieval(result, scenario.evidence_bead_ids)
            assert 0.0 <= score.recall <= 1.0
            assert 0.0 <= score.precision <= 1.0
