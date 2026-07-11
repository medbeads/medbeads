"""Real Claude API smoke test (billed) — skipped unless ANTHROPIC_API_KEY is
set in the environment, per this task's "実 LLM API は課金を伴う — 実 API を
叩くテストは1シナリオ×2アームのスモーク1回だけ" mandate: this file is that
one scenario x 2-arm (rag, dag) smoke, asserting an answer comes back
and usage is recorded in the JSONL transcript — nothing more. (arm label
updated from dag_full to dag in U6 — see bench.retrieval.dag's docstring
for the dag_nosib/dag_full consolidation; "arm" here is just an opaque
string tag passed to ClaudeClient.answer, not a Retriever, so this rename
is cosmetic/consistency-only, not a behavior change.)
"""

from __future__ import annotations

import asyncio
import json
import os
from pathlib import Path

import pytest

from bench.llm.claude import ClaudeClient
from bench.llm.transcript import TranscriptLogger

pytestmark = pytest.mark.skipif(
    not os.environ.get("ANTHROPIC_API_KEY"),
    reason="ANTHROPIC_API_KEY not set — real-API smoke test skipped (opt-in, billed)",
)


def test_real_claude_answers_for_rag_and_dag(tmp_path: Path) -> None:
    transcript_path = tmp_path / "smoke_transcript.jsonl"
    context_texts = [
        "fhir_medicationrequest: Lisinopril 10mg prescribed for hypertension, started 2020-03-01.",
        "fhir_condition: Essential hypertension, onset 2020-02-15.",
    ]

    async def _run() -> None:
        with TranscriptLogger(transcript_path) as transcript:
            client = ClaudeClient(transcript=transcript, max_tokens=128)
            for arm in ("rag", "dag"):
                result = await client.answer(
                    question="What medication was prescribed for the patient's hypertension?",
                    context_texts=context_texts,
                    scenario_id="smoke-scenario-1",
                    arm=arm,
                )
                assert result.answer_text.strip(), f"{arm}: empty answer_text from real API"
                assert result.input_tokens > 0, f"{arm}: input_tokens not recorded"
                assert result.output_tokens > 0, f"{arm}: output_tokens not recorded"

    asyncio.run(_run())

    lines = transcript_path.read_text(encoding="utf-8").strip().splitlines()
    assert len(lines) == 2
    rows = [json.loads(line) for line in lines]
    assert {r["arm"] for r in rows} == {"rag", "dag"}
    for row in rows:
        assert row["scenario_id"] == "smoke-scenario-1"
        assert row["input_tokens"] > 0
        assert row["output_tokens"] > 0
        assert row["error"] is None
