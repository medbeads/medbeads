"""bench.metrics: R8.3's metric functions — token efficiency, retrieval
recall/precision@budget (re-exported from bench.retrieval.metrics, which
predates this package and stays the single implementation — see
bench/bench/retrieval/metrics.py's own docstring for why it lives there),
hallucination rate (claim decomposition -> 3-way attribution via an LLM
judge), and causal-order (temporal_order) agreement.
"""

from __future__ import annotations

from bench.metrics.hallucination import (
    ATTRIBUTION_CONTEXT_OUT_ALL_IN,
    ATTRIBUTION_HALLUCINATION,
    ATTRIBUTION_SUPPORTED,
    JUDGE_PROMPT_TEMPLATE,
    JUDGE_PROMPT_TEMPLATE_VERSION,
    ClaimAttribution,
    HallucinationScore,
    score_hallucination,
)
from bench.metrics.temporal import TEMPORAL_ANSWER_PROMPT_SUFFIX, TemporalOrderScore, score_temporal_order
from bench.metrics.token import TokenUsage, aggregate_token_usage
from bench.retrieval.metrics import RetrievalScore, score_retrieval

__all__ = [
    "RetrievalScore",
    "score_retrieval",
    "TokenUsage",
    "aggregate_token_usage",
    "ClaimAttribution",
    "HallucinationScore",
    "score_hallucination",
    "ATTRIBUTION_SUPPORTED",
    "ATTRIBUTION_CONTEXT_OUT_ALL_IN",
    "ATTRIBUTION_HALLUCINATION",
    "JUDGE_PROMPT_TEMPLATE",
    "JUDGE_PROMPT_TEMPLATE_VERSION",
    "TemporalOrderScore",
    "score_temporal_order",
    "TEMPORAL_ANSWER_PROMPT_SUFFIX",
]
