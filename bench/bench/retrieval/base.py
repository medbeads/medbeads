"""Shared Retriever protocol + RetrievalResult (R8.2's common return shape:
"{bead_ids(取得順), texts, used_tokens, latency_ms, arm 固有メタ}").

estimate_tokens mirrors internal/engine/graph/context.go's EstimateTokens
(``len(s) / 3``, i.e. UTF-8 byte length / 3) byte-for-byte, so a token_budget
value means the same thing across all four arms: rag/fts (this module's own
greedy packer, see pack_greedy) and dag_nosib/dag_full (graph.BuildContext,
already budgeted server-side) are otherwise using completely different
packing code paths, and R8.2's "同一 token_budget" comparison is only fair if
both sides count a token the same way — reusing a *different* Python
tokenizer (e.g. tiktoken) here would silently reintroduce the very unfairness
R8.2 exists to eliminate.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any, Protocol


def estimate_tokens(text: str) -> int:
    """UTF-8 byte length / 3 — see this module's docstring for why this must
    stay byte-for-byte identical to graph.EstimateTokens (Go)."""
    return len(text.encode("utf-8")) // 3


@dataclass(frozen=True)
class RetrievalResult:
    """One arm's answer to one (question, patient_id, budget) query.

    bead_ids is in retrieval order (R8.2's "取得順" — the order this arm
    actually surfaced/packed Beads in, which for rag/fts is rank order and
    for dag_nosib/dag_full is graph.BuildContext's tier-then-timestamp
    order); texts is the same length and order, one rendered text per
    bead_id (empty string for an L2-only dag reference, matching
    graph.ContextItem's own "L2 carries no content text" contract). meta is
    arm-specific bookkeeping (e.g. rag's vector distances, dag's
    truncated_refs count) kept out of the common fields so bench.metrics can
    stay arm-agnostic.
    """

    arm: str
    bead_ids: list[str]
    texts: list[str]
    used_tokens: int
    latency_ms: float
    meta: dict[str, Any] = field(default_factory=dict)

    def to_json_dict(self) -> dict[str, Any]:
        return {
            "arm": self.arm,
            "bead_ids": self.bead_ids,
            "texts": self.texts,
            "used_tokens": self.used_tokens,
            "latency_ms": self.latency_ms,
            "meta": self.meta,
        }


class Retriever(Protocol):
    """Common interface every retrieval arm implements (R8.2)."""

    arm: str

    async def retrieve(self, *, question: str, patient_id: str, budget: int) -> RetrievalResult: ...


def pack_greedy(
    ranked: list[tuple[str, str]], budget: int
) -> tuple[list[str], list[str], int]:
    """Greedily pack (bead_id, text) pairs (already in the arm's own rank
    order) into budget estimated tokens, stopping at the first item that
    would not fit — R8.2's "予算内に L0 content を詰める(budget 到達で打ち切
    り)" for the rag/fts arms.

    Unlike graph.BuildContext (which demotes an over-budget item to a
    cheaper granularity — L1/L2 — before giving up on it), rag/fts have only
    one granularity (full L0 content, since rag_search/search_beads+get_bead
    never return a pre-summarized text): once one item does not fit, every
    later (lower-ranked) item is also skipped rather than probed
    individually — this is "budget 到達で打ち切り" (stop at budget), not
    "skip the ones that don't fit and keep trying smaller ones", per the
    lead's arm definition. This keeps rag/fts's packing rule the simplest
    possible baseline, deliberately not reimplementing BuildContext's
    tiered-demotion sophistication for a single-tier input.

    Returns (bead_ids, texts, used_tokens) — bead_ids/texts share the same
    order and length (RetrievalResult's own contract).
    """
    bead_ids: list[str] = []
    texts: list[str] = []
    used = 0
    for bead_id, text in ranked:
        cost = estimate_tokens(text)
        if used + cost > budget:
            break
        bead_ids.append(bead_id)
        texts.append(text)
        used += cost
    return bead_ids, texts, used
