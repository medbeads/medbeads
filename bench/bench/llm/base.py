"""Shared LlmClient Protocol + AnswerResult (the DI seam bench.run and
bench.metrics.hallucination code against, so a fake, deterministic double
can stand in for ClaudeClient in every test except the one opt-in real-API
smoke test — mirrors bench.retrieval.base.Retriever's own Protocol pattern).
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any, Protocol


@dataclass(frozen=True)
class AnswerResult:
    """One LLM call's result: the text answer plus token usage, in the
    shape both ClaudeClient (real usage.input_tokens/output_tokens from the
    anthropic SDK) and FakeLlmClient (synthesized deterministic counts) can
    produce identically, so bench.metrics' token-efficiency accounting
    (R8.3) never needs to special-case which backend produced a given
    result.
    """

    answer_text: str
    input_tokens: int
    output_tokens: int
    # Free-form, backend-specific extras (e.g. stop_reason, model) — kept
    # out of the two required token fields so bench.metrics stays
    # backend-agnostic, same rationale as RetrievalResult.meta.
    meta: dict[str, Any] = field(default_factory=dict)

    def to_json_dict(self) -> dict[str, Any]:
        return {
            "answer_text": self.answer_text,
            "input_tokens": self.input_tokens,
            "output_tokens": self.output_tokens,
            "meta": self.meta,
        }


class LlmClient(Protocol):
    """Common interface bench.run's orchestration and bench.metrics.hallucination's
    judge calls both code against. `answer` is the scenario-answering call
    (R8.3's prompt template, see bench.llm.claude); `complete` is a lower-level
    "one prompt in, one text out" call used by the hallucination judge (whose
    prompt is a different, judge-specific template — see
    bench.metrics.hallucination) and by temporal-order's LLM-driven answer
    parsing where applicable. Both calls go through the same
    TranscriptLogger, distinguished by call_kind.
    """

    model: str

    async def answer(
        self, *, question: str, context_texts: list[str], scenario_id: str, arm: str
    ) -> AnswerResult: ...

    async def complete(
        self, *, system: str, user: str, scenario_id: str, arm: str, call_kind: str
    ) -> AnswerResult: ...
