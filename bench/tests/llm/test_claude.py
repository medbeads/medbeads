"""ClaudeClient: prompt template rendering/versioning + construction — no
network calls here (see tests/llm/test_claude_smoke.py for the opt-in real
API smoke test)."""

from __future__ import annotations

import asyncio
import hashlib
import json
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

from bench.llm.claude import (
    PROMPT_TEMPLATE,
    PROMPT_TEMPLATE_VERSION,
    ClaudeClient,
    render_answer_prompt,
)
from bench.llm.transcript import TranscriptLogger


def test_prompt_template_version_is_sha256_prefix_of_template() -> None:
    expected = hashlib.sha256(PROMPT_TEMPLATE.encode("utf-8")).hexdigest()[:16]
    assert PROMPT_TEMPLATE_VERSION == expected


def test_render_answer_prompt_includes_question_and_context() -> None:
    prompt = render_answer_prompt("What drug?", ["fhir_medicationrequest: Lisinopril"])
    assert "What drug?" in prompt
    assert "Lisinopril" in prompt


def test_render_answer_prompt_empty_context_has_placeholder() -> None:
    prompt = render_answer_prompt("q", [])
    assert "(no context retrieved)" in prompt


def test_render_answer_prompt_instructs_not_stated_fallback() -> None:
    assert "Not stated in the record." in PROMPT_TEMPLATE


def test_client_construction_does_not_require_network(tmp_path: Path) -> None:
    with TranscriptLogger(tmp_path / "t.jsonl") as transcript:
        client = ClaudeClient(transcript=transcript, api_key="sk-test-not-real")
        assert client.model == "claude-sonnet-5"


@dataclass
class _FakeTextBlock:
    text: str
    type: str = "text"


@dataclass
class _FakeUsage:
    input_tokens: int
    output_tokens: int


@dataclass
class _FakeMessage:
    content: list[_FakeTextBlock] = field(default_factory=list)
    usage: _FakeUsage = field(default_factory=lambda: _FakeUsage(0, 0))


def test_logged_request_matches_actual_sent_kwargs(tmp_path: Path, monkeypatch) -> None:
    """data-reviewer note: TranscriptRow.request must be the exact dict
    passed to anthropic's messages.create — not a separately hand-assembled
    lookalike that can silently drift from what was actually sent over the
    wire (e.g. the old code always logged both "system" and "user" string
    keys, which the real payload never has in that shape: "system" is a
    top-level kwarg only when non-empty, and message content lives under a
    "messages" list, not a "user" key)."""
    captured_kwargs: dict[str, Any] = {}

    async def _fake_create(**kwargs: Any) -> _FakeMessage:
        captured_kwargs.update(kwargs)
        return _FakeMessage(
            content=[_FakeTextBlock(text="Lisinopril was prescribed.")],
            usage=_FakeUsage(input_tokens=42, output_tokens=7),
        )

    transcript_path = tmp_path / "t.jsonl"
    with TranscriptLogger(transcript_path) as transcript:
        client = ClaudeClient(transcript=transcript, api_key="sk-test-not-real")
        monkeypatch.setattr(client._client.messages, "create", _fake_create)

        asyncio.run(
            client.answer(
                question="What medication was prescribed?",
                context_texts=["fhir_medicationrequest: Lisinopril prescribed."],
                scenario_id="s1",
                arm="rag",
            )
        )

    logged_row = json.loads(transcript_path.read_text(encoding="utf-8").strip().splitlines()[0])
    assert logged_row["request"] == captured_kwargs, (
        f"logged request {logged_row['request']!r} must equal actually-sent kwargs {captured_kwargs!r}"
    )
    # And specifically: no "system"/"user" placeholder keys the real
    # request never has (system omitted entirely when empty; content is
    # under "messages", never a top-level "user" key).
    assert "user" not in logged_row["request"]
    assert "system" not in logged_row["request"]  # answer() always calls complete(system="")
    assert "messages" in logged_row["request"]
