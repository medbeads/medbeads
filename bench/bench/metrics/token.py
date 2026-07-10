"""Token-efficiency metrics (R8.3): usage実測(input/output tokens) per arm
per scenario, aggregated from AnswerResult objects a run already produced —
no separate tokenizer call, since bench.llm.AnswerResult already carries
the backend's own real usage counts (ClaudeClient: anthropic SDK's
message.usage; FakeLlmClient: its own deterministic synthesis) — this
module is pure aggregation, never re-measures.
"""

from __future__ import annotations

from dataclasses import dataclass

from bench.llm.base import AnswerResult


@dataclass(frozen=True)
class TokenUsage:
    """One AnswerResult's usage, plus its total (a reviewer-friendly
    convenience field, not a third independent measurement)."""

    input_tokens: int
    output_tokens: int

    @property
    def total_tokens(self) -> int:
        return self.input_tokens + self.output_tokens

    def to_json_dict(self) -> dict[str, int]:
        return {
            "input_tokens": self.input_tokens,
            "output_tokens": self.output_tokens,
            "total_tokens": self.total_tokens,
        }


def token_usage_from_answer(result: AnswerResult) -> TokenUsage:
    return TokenUsage(input_tokens=result.input_tokens, output_tokens=result.output_tokens)


def aggregate_token_usage(usages: list[TokenUsage]) -> TokenUsage:
    """Sum of every field across usages — used by bench.run's summary.json
    to report per-arm total/average token consumption across every scenario
    that arm answered. Empty input sums to all-zero (never raises: a
    zero-scenario arm is a legitimate, reportable state, not an error)."""
    return TokenUsage(
        input_tokens=sum(u.input_tokens for u in usages),
        output_tokens=sum(u.output_tokens for u in usages),
    )
