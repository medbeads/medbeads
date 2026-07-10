from __future__ import annotations

from bench.llm.base import AnswerResult
from bench.metrics.token import TokenUsage, aggregate_token_usage, token_usage_from_answer


def test_token_usage_total_tokens_is_sum() -> None:
    usage = TokenUsage(input_tokens=10, output_tokens=5)
    assert usage.total_tokens == 15


def test_token_usage_from_answer_copies_fields() -> None:
    result = AnswerResult(answer_text="x", input_tokens=7, output_tokens=3)
    usage = token_usage_from_answer(result)
    assert usage.input_tokens == 7
    assert usage.output_tokens == 3


def test_aggregate_token_usage_sums_across_calls() -> None:
    usages = [TokenUsage(input_tokens=10, output_tokens=2), TokenUsage(input_tokens=5, output_tokens=1)]
    total = aggregate_token_usage(usages)
    assert total.input_tokens == 15
    assert total.output_tokens == 3
    assert total.total_tokens == 18


def test_aggregate_token_usage_empty_is_zero() -> None:
    total = aggregate_token_usage([])
    assert total.input_tokens == 0
    assert total.output_tokens == 0
