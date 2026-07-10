"""fts arm: FTS5 hits via `search_beads`, greedily packed into budget.

R8.2: "fts: search_beads(FTS)ヒットを同様に予算詰め". search_beads returns
only beadRefs (id/patient_root/type/timestamp/summary — no content, see
internal/mcpserver/render.go's beadRefView doc comment), so this arm makes
one follow-up get_bead call per hit (in rank order) to resolve each hit's
full content before rendering/packing — the two-MCP-call cost this arm pays
that rag_search's single call does not is itself a real, honestly-reported
difference between the two arms' access patterns (not hidden from
meta), not something this arm should try to disguise.
"""

from __future__ import annotations

import time
from typing import Any

from bench.ingest.mcp_client import MedBeadsClient
from bench.retrieval.base import RetrievalResult, pack_greedy
from bench.retrieval.render import render_l0

DEFAULT_LIMIT = 50


class FtsRetriever:
    """Retriever protocol implementation over MedBeadsClient.search_beads +
    get_bead (search_beads alone carries no content to pack)."""

    arm = "fts"

    def __init__(self, client: MedBeadsClient, *, limit: int = DEFAULT_LIMIT) -> None:
        self._client = client
        self._limit = limit

    async def retrieve(self, *, question: str, patient_id: str, budget: int) -> RetrievalResult:
        start = time.perf_counter()
        hits: list[dict[str, Any]] = await self._client.search_beads(
            query=question, patient_id=patient_id, limit=self._limit
        )

        ranked: list[tuple[str, str]] = []
        for hit in hits:
            bead = await self._client.get_bead(hit["id"])
            ranked.append((hit["id"], render_l0(bead.get("type", ""), bead.get("content", {}))))
        elapsed_ms = (time.perf_counter() - start) * 1000.0

        bead_ids, texts, used_tokens = pack_greedy(ranked, budget)

        return RetrievalResult(
            arm=self.arm,
            bead_ids=bead_ids,
            texts=texts,
            used_tokens=used_tokens,
            latency_ms=elapsed_ms,
            meta={"candidates_considered": len(hits)},
        )
