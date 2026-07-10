"""rag arm: pure vector top-k via `rag_search`, greedily packed into budget.

R8.2: "rag: rag_search(pure vector top-k)。予算内に L0 content を詰める(予算
到達で打ち切り)" — rag_search already returns each hit's full content plus
vector distance in rank order (internal/mcpserver/rag_search.go), so this
arm's only job is to render that content the same way graph.renderL0 does
(so token costs are comparable across arms — see base.py's estimate_tokens
docstring) and hand the ranked (bead_id, text) pairs to pack_greedy.
"""

from __future__ import annotations

import time
from typing import Any

from bench.ingest.mcp_client import MedBeadsClient
from bench.retrieval.base import RetrievalResult, estimate_tokens, pack_greedy
from bench.retrieval.render import render_l0

DEFAULT_K = 50


class RagRetriever:
    """Retriever protocol implementation over MedBeadsClient.rag_search."""

    arm = "rag"

    def __init__(self, client: MedBeadsClient, *, k: int = DEFAULT_K) -> None:
        self._client = client
        self._k = k

    async def retrieve(self, *, question: str, patient_id: str, budget: int) -> RetrievalResult:
        start = time.perf_counter()
        hits: list[dict[str, Any]] = await self._client.rag_search(
            query=question, patient_id=patient_id, k=self._k
        )
        elapsed_ms = (time.perf_counter() - start) * 1000.0

        ranked = [(hit["id"], render_l0(hit.get("type", ""), hit.get("content", {}))) for hit in hits]
        bead_ids, texts, used_tokens = pack_greedy(ranked, budget)

        distance_by_id = {hit["id"]: hit.get("distance") for hit in hits}
        return RetrievalResult(
            arm=self.arm,
            bead_ids=bead_ids,
            texts=texts,
            used_tokens=used_tokens,
            latency_ms=elapsed_ms,
            meta={
                "candidates_considered": len(hits),
                "vector_distances": [distance_by_id.get(bid) for bid in bead_ids],
            },
        )


# re-exported for tests/other arms that want the exact same token cost
# accounting rag.py uses.
__all__ = ["RagRetriever", "estimate_tokens"]
