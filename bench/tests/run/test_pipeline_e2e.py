"""End-to-end run_bench pipeline test (fake LLM, real scratch medbeadsd +
fake embedding sidecar — mirrors tests/retrieval/test_4arm_integration.py's
own scratch-data setup): 2 patients x 4 arms, checking results.jsonl /
run_manifest.json / summary.json's shape, per this task's "フェイク LLM で:
run パイプライン e2e テスト(scratch データ、2患者×4アーム → results/summary/
manifest の形状)".

Requires the go toolchain and the real Synthea dataset (see
tests/conftest.py) — skipped automatically if either is unavailable, same
as every other bench.retrieval/bench.run integration test. Never touches
the real store (~/medbeads-synthea/medbeads_data) — everything here is a
tmp_path scratch ingest.
"""

from __future__ import annotations

import asyncio
import json
from pathlib import Path

import pytest

from bench.llm.fake import FakeLlmClient
from bench.llm.transcript import TranscriptLogger
from bench.run.pipeline import ALL_ARMS, RunConfig, run_bench


def _run(
    tmp_path: Path,
    medbeadsd_binary: Path,
    scratch_run_fixture: tuple[Path, Path, Path],
    fake_embed_sidecar_url: str,
    *,
    out_dir: Path | None = None,
    run_judge: bool = True,
):
    data_dir, _manifest_path, scenarios_path = scratch_run_fixture
    out_dir = out_dir or (tmp_path / "runs" / "e2e")

    config = RunConfig(
        scenarios_path=scenarios_path,
        data_dir=data_dir,
        medbeadsd_path=medbeadsd_binary,
        out_dir=out_dir,
        arms=ALL_ARMS,
        budget=2000,
        embedder_url=fake_embed_sidecar_url,
        run_judge=run_judge,
    )

    transcript_path = out_dir / "llm_transcript.jsonl"
    with TranscriptLogger(transcript_path) as transcript:
        llm = FakeLlmClient(transcript=transcript, judge_responses=_FAKE_JUDGE_RESPONSES)
        report = asyncio.run(run_bench(config, llm=llm, judge=llm if run_judge else None))

    return report, out_dir


_FAKE_JUDGE_RESPONSES: dict[str, str] = {}


@pytest.fixture(autouse=True)
def _judge_always_supported(monkeypatch: pytest.MonkeyPatch) -> None:
    """Patches FakeLlmClient.complete's judge branch so every judge call in
    this test module returns a fixed, always-parseable "no claims" (empty
    array) response regardless of prompt text — avoids needing to
    pre-script every possible (scenario, arm, answer) judge prompt combo
    2 patients x 4 arms would otherwise require."""

    async def _fake_complete(self, *, system, user, scenario_id, arm, call_kind):  # noqa: ANN001
        from bench.llm.base import AnswerResult
        from bench.llm.transcript import TranscriptRow, now_iso

        if call_kind == "judge":
            response_text = "[]"
        else:
            from bench.llm.fake import _synthesize_answer

            response_text = _synthesize_answer([user], max_context_sentences=self._max_context_sentences)
        input_tokens = max(1, len(user) // 4)
        output_tokens = max(1, len(response_text) // 4)
        self.calls.append((scenario_id, arm, call_kind))
        self._transcript.log(
            TranscriptRow(
                timestamp=now_iso(),
                scenario_id=scenario_id,
                arm=arm,
                call_kind=call_kind,
                model=self.model,
                prompt_template_version="fake",
                request={"text": user},
                response_text=response_text,
                input_tokens=input_tokens,
                output_tokens=output_tokens,
            )
        )
        return AnswerResult(answer_text=response_text, input_tokens=input_tokens, output_tokens=output_tokens)

    monkeypatch.setattr(FakeLlmClient, "complete", _fake_complete)


def test_e2e_produces_results_manifest_and_summary_shapes(
    tmp_path: Path,
    medbeadsd_binary: Path,
    scratch_run_fixture: tuple[Path, Path, Path],
    fake_embed_sidecar_url: str,
) -> None:
    report, out_dir = _run(tmp_path, medbeadsd_binary, scratch_run_fixture, fake_embed_sidecar_url)

    results_path = out_dir / "results.jsonl"
    manifest_path = out_dir / "run_manifest.json"
    summary_path = out_dir / "summary.json"
    assert results_path.is_file()
    assert manifest_path.is_file()
    assert summary_path.is_file()

    _, _, scenarios_path = scratch_run_fixture
    from bench.scenarios.model import load_scenarios_yaml

    scenarios = load_scenarios_yaml(scenarios_path)
    assert report.total_pairs == len(scenarios) * len(ALL_ARMS)
    assert report.completed_pairs == report.total_pairs
    assert report.skipped_pairs == 0

    rows = [json.loads(line) for line in results_path.read_text(encoding="utf-8").strip().splitlines()]
    assert len(rows) == report.total_pairs
    seen_pairs = {(r["scenario_id"], r["arm"]) for r in rows}
    assert len(seen_pairs) == len(rows), "every (scenario_id, arm) pair should appear exactly once"
    for r in rows:
        assert r["arm"] in ALL_ARMS
        assert "retrieval" in r and "bead_ids" in r["retrieval"]
        assert "retrieval_score" in r and "recall" in r["retrieval_score"]
        assert "token_usage" in r and "total_tokens" in r["token_usage"]
        assert "llm_answer" in r and isinstance(r["llm_answer"], str)
        # hallucination is scored for every non-refusal/refusal answer alike
        # (run_judge=True default) — shape check only, logic is
        # tests/metrics/test_hallucination.py's job.
        assert r["hallucination"] is not None
        assert "hallucination_rate" in r["hallucination"]

    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    for key in (
        "git_commit",
        "config_hash",
        "dataset_fingerprint",
        "model",
        "judge_model",
        "answer_prompt_version",
        "judge_prompt_version",
        "arms",
        "budget",
        "by_arm",
    ):
        assert key in manifest, f"run_manifest.json missing key {key!r}"
    assert manifest["arms"] == list(ALL_ARMS)
    assert manifest["dataset_fingerprint"] != "unknown"  # scratch ingest always writes manifest.jsonl

    summary = json.loads(summary_path.read_text(encoding="utf-8"))
    assert set(summary["by_arm"].keys()) == set(ALL_ARMS)
    for arm in ALL_ARMS:
        arm_stats = summary["by_arm"][arm]
        assert arm_stats["scored_this_run"] == len(scenarios)
        assert arm_stats["token_usage_total"]["total_tokens"] > 0


def test_e2e_no_judge_flag_skips_hallucination_scoring(
    tmp_path: Path,
    medbeadsd_binary: Path,
    scratch_run_fixture: tuple[Path, Path, Path],
    fake_embed_sidecar_url: str,
) -> None:
    report, out_dir = _run(
        tmp_path,
        medbeadsd_binary,
        scratch_run_fixture,
        fake_embed_sidecar_url,
        out_dir=tmp_path / "runs" / "no_judge",
        run_judge=False,
    )
    assert report.judge_model is None
    rows = [
        json.loads(line)
        for line in (out_dir / "results.jsonl").read_text(encoding="utf-8").strip().splitlines()
    ]
    assert all(r["hallucination"] is None for r in rows)


def test_e2e_resume_skips_already_completed_pairs(
    tmp_path: Path,
    medbeadsd_binary: Path,
    scratch_run_fixture: tuple[Path, Path, Path],
    fake_embed_sidecar_url: str,
) -> None:
    """Billing-protection contract (R8.4): re-invoking run_bench against the
    same --out directory must skip every (scenario_id, arm) pair already in
    results.jsonl, never re-calling the LLM for it."""
    out_dir = tmp_path / "runs" / "resume"
    first_report, _ = _run(
        tmp_path, medbeadsd_binary, scratch_run_fixture, fake_embed_sidecar_url, out_dir=out_dir
    )
    assert first_report.completed_pairs == first_report.total_pairs
    assert first_report.skipped_pairs == 0

    results_path = out_dir / "results.jsonl"
    lines_after_first_run = results_path.read_text(encoding="utf-8").strip().splitlines()

    second_report, _ = _run(
        tmp_path, medbeadsd_binary, scratch_run_fixture, fake_embed_sidecar_url, out_dir=out_dir
    )
    assert second_report.completed_pairs == 0
    assert second_report.skipped_pairs == second_report.total_pairs

    lines_after_second_run = results_path.read_text(encoding="utf-8").strip().splitlines()
    assert lines_after_second_run == lines_after_first_run, (
        "resume must not rewrite/duplicate rows for already-completed (scenario_id, arm) pairs"
    )


def test_e2e_resume_after_partial_out_dir_only_runs_missing_pairs(
    tmp_path: Path,
    medbeadsd_binary: Path,
    scratch_run_fixture: tuple[Path, Path, Path],
    fake_embed_sidecar_url: str,
) -> None:
    """Simulates a run that died partway through (results.jsonl has some
    but not all pairs already written) by hand-seeding results.jsonl with
    every rag-arm row from a full run, then re-invoking with all 4 arms —
    only the non-rag arms' pairs should actually execute."""
    out_dir = tmp_path / "runs" / "partial"
    full_report, full_out_dir = _run(
        tmp_path, medbeadsd_binary, scratch_run_fixture, fake_embed_sidecar_url, out_dir=tmp_path / "runs" / "full"
    )
    full_rows = [
        json.loads(line)
        for line in (full_out_dir / "results.jsonl").read_text(encoding="utf-8").strip().splitlines()
    ]
    rag_only_rows = [r for r in full_rows if r["arm"] == "rag"]
    assert rag_only_rows, "full run should include rag-arm rows to seed the partial-resume scenario"

    out_dir.mkdir(parents=True, exist_ok=True)
    with (out_dir / "results.jsonl").open("w", encoding="utf-8") as f:
        for row in rag_only_rows:
            f.write(json.dumps(row, sort_keys=True))
            f.write("\n")

    resumed_report, _ = _run(
        tmp_path, medbeadsd_binary, scratch_run_fixture, fake_embed_sidecar_url, out_dir=out_dir
    )
    assert resumed_report.skipped_pairs == len(rag_only_rows)
    assert resumed_report.completed_pairs == resumed_report.total_pairs - len(rag_only_rows)

    final_rows = [
        json.loads(line)
        for line in (out_dir / "results.jsonl").read_text(encoding="utf-8").strip().splitlines()
    ]
    assert len(final_rows) == full_report.total_pairs
    assert {(r["scenario_id"], r["arm"]) for r in final_rows} == {
        (r["scenario_id"], r["arm"]) for r in full_rows
    }


def test_e2e_resumed_summary_matches_one_shot_summary(
    tmp_path: Path,
    medbeadsd_binary: Path,
    scratch_run_fixture: tuple[Path, Path, Path],
    fake_embed_sidecar_url: str,
) -> None:
    """data-reviewer blocker fix: summary.json after an interrupt-then-
    resume-to-completion run must be numerically identical to summary.json
    from a single one-shot run over the same scenarios/arms — resume must
    never leave summary.json reflecting only the delta this invocation
    itself scored (the old, buggy in-memory-accumulator behavior)."""
    # One-shot: everything in a single run_bench call.
    one_shot_report, one_shot_out_dir = _run(
        tmp_path,
        medbeadsd_binary,
        scratch_run_fixture,
        fake_embed_sidecar_url,
        out_dir=tmp_path / "runs" / "one_shot",
    )
    one_shot_summary = json.loads((one_shot_out_dir / "summary.json").read_text(encoding="utf-8"))

    # Interrupted-then-resumed: seed results.jsonl with only the rag-arm
    # rows from the one-shot run (simulating a run that died after
    # finishing rag but before starting the other 3 arms), then resume.
    one_shot_rows = [
        json.loads(line)
        for line in (one_shot_out_dir / "results.jsonl").read_text(encoding="utf-8").strip().splitlines()
    ]
    rag_only_rows = [r for r in one_shot_rows if r["arm"] == "rag"]
    assert rag_only_rows, "one-shot run should include rag-arm rows to seed the partial-resume scenario"

    resumed_out_dir = tmp_path / "runs" / "resumed"
    resumed_out_dir.mkdir(parents=True, exist_ok=True)
    with (resumed_out_dir / "results.jsonl").open("w", encoding="utf-8") as f:
        for row in rag_only_rows:
            f.write(json.dumps(row, sort_keys=True))
            f.write("\n")

    resumed_report, _ = _run(
        tmp_path, medbeadsd_binary, scratch_run_fixture, fake_embed_sidecar_url, out_dir=resumed_out_dir
    )
    assert resumed_report.skipped_pairs == len(rag_only_rows)  # actually resumed, not a fresh run
    resumed_summary = json.loads((resumed_out_dir / "summary.json").read_text(encoding="utf-8"))

    def _without_latency(summary: dict) -> dict:
        # latency_s is a real wall-clock measurement of each retrieve() MCP
        # call (RetrievalResult.latency_ms, persisted per row) — it is
        # expected to differ slightly between two independent process runs
        # even over logically-identical scenario/arm inputs (scheduler
        # jitter, cache warmth, etc.), so it is excluded from this
        # exact-equality check; every *other* field (recall/precision/
        # token usage/hallucination rate/temporal agreement/pair counts —
        # i.e. every field that is a pure function of already-recorded,
        # non-timing row data) must match exactly.
        return {
            arm: {k: v for k, v in stats.items() if k != "latency_s"}
            for arm, stats in summary["by_arm"].items()
        }

    assert _without_latency(resumed_summary) == _without_latency(one_shot_summary), (
        "resumed run's summary.json must match a one-shot run's summary.json exactly "
        "(latency_s excluded — real wall-clock timing, expected to vary between runs) — "
        f"resumed={resumed_summary!r} one_shot={one_shot_summary!r}"
    )
    # And specifically: the resumed run's summary must NOT be scoped to
    # just the pairs *this* invocation itself scored (the bug this test
    # guards against) — every arm's "pairs" must cover the full sweep,
    # including the pre-seeded rag rows this invocation never re-ran.
    for arm in ALL_ARMS:
        assert resumed_summary["by_arm"][arm]["pairs"] == one_shot_summary["by_arm"][arm]["pairs"]
        assert resumed_summary["by_arm"][arm]["latency_s"]["n"] == one_shot_summary["by_arm"][arm]["latency_s"]["n"]
