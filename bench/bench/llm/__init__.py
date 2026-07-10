"""bench.llm: a thin, DI-able "answer(question, context_texts) ->
AnswerResult" client (R8.3/DESIGN_v3.md §9's `llm/` package), plus a JSONL
transcript logger every real/fake call writes through, per the lead's "全往復
を JSONL 記録(request/response/usage/timestamp/scenario_id/arm)".

Exports:
  - LlmClient (Protocol): the DI seam bench.run's orchestration codes
    against — ClaudeClient (real, anthropic SDK) and FakeLlmClient (tests)
    both implement it.
  - AnswerResult: one answer() call's return shape (answer_text + usage).
  - TranscriptLogger: append-only JSONL writer, shared by llm and metrics
    (judge) call sites — one file, one schema, distinguished by a
    `call_kind` field ("answer" vs "judge", see bench.metrics.hallucination).
  - PROMPT_TEMPLATE / PROMPT_TEMPLATE_VERSION: the versioned answer-prompt
    constant (see claude.py's docstring for the full text and rationale).
"""

from __future__ import annotations

from bench.llm.base import AnswerResult, LlmClient
from bench.llm.claude import PROMPT_TEMPLATE, PROMPT_TEMPLATE_VERSION, ClaudeClient, render_answer_prompt
from bench.llm.fake import FakeLlmClient
from bench.llm.transcript import TranscriptLogger

__all__ = [
    "AnswerResult",
    "LlmClient",
    "ClaudeClient",
    "FakeLlmClient",
    "TranscriptLogger",
    "PROMPT_TEMPLATE",
    "PROMPT_TEMPLATE_VERSION",
    "render_answer_prompt",
]
