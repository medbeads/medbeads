"""FakeLlmClient: deterministic answer synthesis (never invents text beyond
context_texts), scripted judge responses, and transcript logging."""

from __future__ import annotations

import asyncio
import json
from pathlib import Path

import pytest

from bench.llm.fake import FakeLlmClient
from bench.llm.transcript import TranscriptLogger


def test_answer_synthesizes_from_context_only(tmp_path: Path) -> None:
    with TranscriptLogger(tmp_path / "t.jsonl") as transcript:
        client = FakeLlmClient(transcript=transcript)
        result = asyncio.run(
            client.answer(
                question="What medication was prescribed?",
                context_texts=["fhir_medicationrequest: Lisinopril 10mg prescribed for hypertension."],
                scenario_id="s1",
                arm="rag",
            )
        )
    assert "Lisinopril" in result.answer_text
    assert result.input_tokens > 0
    assert result.output_tokens > 0


def test_answer_empty_context_yields_not_stated(tmp_path: Path) -> None:
    with TranscriptLogger(tmp_path / "t.jsonl") as transcript:
        client = FakeLlmClient(transcript=transcript)
        result = asyncio.run(
            client.answer(question="anything?", context_texts=[], scenario_id="s1", arm="fts")
        )
    assert result.answer_text == "Not stated in the record."


def test_answer_never_contains_text_outside_context(tmp_path: Path) -> None:
    """The whole point of a fake LLM used in an e2e hallucination test: it
    can never hallucinate by construction, since every word of its answer
    is a substring drawn from context_texts."""
    context = ["fhir_condition: Diabetes mellitus type 2 diagnosed."]
    with TranscriptLogger(tmp_path / "t.jsonl") as transcript:
        client = FakeLlmClient(transcript=transcript)
        result = asyncio.run(
            client.answer(question="q", context_texts=context, scenario_id="s1", arm="dag_full")
        )
    assert result.answer_text in context[0] or result.answer_text == "Not stated in the record."


def test_complete_judge_scripted_response(tmp_path: Path) -> None:
    with TranscriptLogger(tmp_path / "t.jsonl") as transcript:
        client = FakeLlmClient(
            transcript=transcript,
            judge_responses={"JUDGE_PROMPT": '[{"claim": "x", "attribution": "supported"}]'},
        )
        result = asyncio.run(
            client.complete(
                system="", user="JUDGE_PROMPT", scenario_id="s1", arm="rag", call_kind="judge"
            )
        )
    assert result.answer_text == '[{"claim": "x", "attribution": "supported"}]'


def test_complete_judge_missing_script_raises_keyerror(tmp_path: Path) -> None:
    with TranscriptLogger(tmp_path / "t.jsonl") as transcript:
        client = FakeLlmClient(transcript=transcript)
        with pytest.raises(KeyError):
            asyncio.run(
                client.complete(
                    system="", user="unscripted prompt", scenario_id="s1", arm="rag", call_kind="judge"
                )
            )


def test_calls_are_recorded_in_order(tmp_path: Path) -> None:
    with TranscriptLogger(tmp_path / "t.jsonl") as transcript:
        client = FakeLlmClient(transcript=transcript)
        asyncio.run(client.answer(question="q1", context_texts=["a"], scenario_id="s1", arm="rag"))
        asyncio.run(client.answer(question="q2", context_texts=["b"], scenario_id="s2", arm="fts"))
    assert client.calls == [("s1", "rag", "answer"), ("s2", "fts", "answer")]


def test_transcript_jsonl_has_one_row_per_call(tmp_path: Path) -> None:
    path = tmp_path / "t.jsonl"
    with TranscriptLogger(path) as transcript:
        client = FakeLlmClient(transcript=transcript)
        asyncio.run(client.answer(question="q1", context_texts=["a"], scenario_id="s1", arm="rag"))
        asyncio.run(client.answer(question="q2", context_texts=["b"], scenario_id="s2", arm="fts"))

    lines = path.read_text(encoding="utf-8").strip().splitlines()
    assert len(lines) == 2
    rows = [json.loads(line) for line in lines]
    assert rows[0]["scenario_id"] == "s1"
    assert rows[0]["arm"] == "rag"
    assert rows[0]["call_kind"] == "answer"
    assert "timestamp" in rows[0]
    assert rows[0]["input_tokens"] > 0


def test_transcript_append_mode_preserves_prior_rows(tmp_path: Path) -> None:
    """Resume contract (R8.4): reopening the same transcript path must not
    truncate rows from an earlier partial run."""
    path = tmp_path / "t.jsonl"
    with TranscriptLogger(path) as transcript:
        client = FakeLlmClient(transcript=transcript)
        asyncio.run(client.answer(question="q1", context_texts=["a"], scenario_id="s1", arm="rag"))

    with TranscriptLogger(path) as transcript:
        client = FakeLlmClient(transcript=transcript)
        asyncio.run(client.answer(question="q2", context_texts=["b"], scenario_id="s2", arm="fts"))

    lines = path.read_text(encoding="utf-8").strip().splitlines()
    assert len(lines) == 2
