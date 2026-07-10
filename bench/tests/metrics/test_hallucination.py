"""Hallucination 3-classification logic (R8.3), driven end to end through
score_hallucination with FakeLlmClient's scripted judge_responses (the
lead's fake-judge-fixed-response test requirement) — never a real judge
call here.
"""

from __future__ import annotations

import asyncio
import hashlib
import json
from pathlib import Path

from bench.llm.fake import FakeLlmClient
from bench.llm.transcript import TranscriptLogger
from bench.metrics.hallucination import (
    JUDGE_PROMPT_TEMPLATE,
    JUDGE_PROMPT_TEMPLATE_VERSION,
    ClaimAttribution,
    score_hallucination,
)

_QUESTION = "What medication was prescribed?"
_ANSWER = "Lisinopril was prescribed for hypertension diagnosed in 2019."
_RETRIEVED_CONTEXT_TEXTS = ["fhir_medicationrequest: Lisinopril prescribed."]
_FULL_RECORD_TEXTS = [
    "fhir_medicationrequest: Lisinopril prescribed.",
    "fhir_condition: Hypertension diagnosed 2020.",
]


def _judge_with_response(tmp_path: Path, response: str) -> tuple[FakeLlmClient, TranscriptLogger]:
    """Builds a FakeLlmClient whose judge_responses is scripted to answer
    `response` for exactly the prompt score_hallucination will actually
    render for (_QUESTION, _ANSWER, _RETRIEVED_CONTEXT_TEXTS,
    _FULL_RECORD_TEXTS) — reusing JUDGE_PROMPT_TEMPLATE.format with the
    same "\\n\\n".join(...) rule score_hallucination itself applies, so this
    fixture can never silently drift from the module-under-test's own
    prompt-rendering logic."""
    transcript = TranscriptLogger(tmp_path / "t.jsonl")
    prompt = JUDGE_PROMPT_TEMPLATE.format(
        retrieved_context="\n\n".join(_RETRIEVED_CONTEXT_TEXTS),
        full_record="\n\n".join(_FULL_RECORD_TEXTS),
        question=_QUESTION,
        answer=_ANSWER,
    )
    judge = FakeLlmClient(transcript=transcript, judge_responses={prompt: response})
    return judge, transcript


def _score(tmp_path: Path, response: str):
    judge, transcript = _judge_with_response(tmp_path, response)
    try:
        return asyncio.run(
            score_hallucination(
                judge,
                question=_QUESTION,
                answer_text=_ANSWER,
                retrieved_context_texts=_RETRIEVED_CONTEXT_TEXTS,
                full_record_texts=_FULL_RECORD_TEXTS,
                scenario_id="s1",
                arm="rag",
            )
        )
    finally:
        transcript.close()


def test_all_three_attribution_buckets_classified_correctly(tmp_path: Path) -> None:
    response = (
        '[{"claim": "Lisinopril was prescribed", "attribution": "supported"}, '
        '{"claim": "prescribed for hypertension", "attribution": "context_out_all_in"}, '
        '{"claim": "diagnosed in 2019", "attribution": "hallucination"}]'
    )
    score = _score(tmp_path, response)

    assert score.total_claims == 3
    assert score.supported_count == 1
    assert score.context_out_all_in_count == 1
    assert score.hallucination_count == 1
    assert score.hallucination_rate == 1 / 3
    assert score.claims == [
        ClaimAttribution(claim="Lisinopril was prescribed", attribution="supported"),
        ClaimAttribution(claim="prescribed for hypertension", attribution="context_out_all_in"),
        ClaimAttribution(claim="diagnosed in 2019", attribution="hallucination"),
    ]


def test_no_claims_no_hallucination_rate_is_zero_not_undefined(tmp_path: Path) -> None:
    score = _score(tmp_path, "[]")
    assert score.total_claims == 0
    assert score.hallucination_rate == 0.0


def test_malformed_judge_json_never_raises(tmp_path: Path) -> None:
    score = _score(tmp_path, "not valid json at all, sorry")
    assert score.total_claims == 0
    assert score.raw_judge_response == "not valid json at all, sorry"


def test_judge_response_wrapped_in_prose_or_fence_still_parses(tmp_path: Path) -> None:
    response = (
        "Here is the analysis:\n```json\n"
        '[{"claim": "Lisinopril was prescribed", "attribution": "supported"}]\n```\nThanks!'
    )
    score = _score(tmp_path, response)
    assert score.total_claims == 1
    assert score.supported_count == 1


def test_judge_calls_logged_to_transcript(tmp_path: Path) -> None:
    response = '[{"claim": "x", "attribution": "supported"}]'
    _score(tmp_path, response)

    lines = (tmp_path / "t.jsonl").read_text(encoding="utf-8").strip().splitlines()
    assert len(lines) == 1
    row = json.loads(lines[0])
    assert row["call_kind"] == "judge"
    assert row["scenario_id"] == "s1"
    assert row["response_text"] == response


def test_judge_prompt_template_version_is_stable_hash() -> None:
    assert JUDGE_PROMPT_TEMPLATE_VERSION == hashlib.sha256(JUDGE_PROMPT_TEMPLATE.encode("utf-8")).hexdigest()[:16]
