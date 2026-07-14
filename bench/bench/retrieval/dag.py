"""dag arm: the unified `retrieve` MCP tool (internal/mcpserver/retrieve.go,
R6.2/R4.2), semantic=True.

Unlike rag/fts (this bench module's own pack_greedy over a single L0
granularity), retrieve's own token-budgeted packing already happened
server-side (graph.BuildContext's L0/L1/L2 tiered greedy packing) by the
time this arm's retrieve() call returns — used_tokens/bead_ids/texts here
come directly from retrieveOut's Items in the server's own priority order
(anchor L0 -> clinical_link L0 -> ancestor L1 -> descendant L2).

U6 consolidation (specs/U6_clinical_note.md, docs/decisions.md 2026-07-11 U6
entry): this module used to expose two arms, dag_nosib/dag_full, toggling
retrieve's include_siblings flag. U5a (specs/U5_api_retrieve.md) removed
package apc and graph's sibling tiers entirely — graph.BuildContext has had
no sibling tier to toggle since then. R9 later gave include_links a new,
standard-term meaning: bounded projection-link expansion with status,
clearance and provenance. This module still exposes one canonical MedBeads
`dag` arm, leaving expansion at its server defaults; rag/fts remain the
baselines. Link candidate/truncation counts are preserved in result metadata
so the new behavior can be audited without resurrecting the obsolete
dag_nosib/dag_full names.
"""

from __future__ import annotations

import time
from typing import Any

from bench.ingest.mcp_client import MedBeadsClient
from bench.retrieval.base import RetrievalResult

DEFAULT_CHAIN_DEPTH = 3

ARM_DAG = "dag"


class DagRetriever:
    """Retriever protocol implementation over MedBeadsClient.retrieve
    (semantic=True) — see this module's docstring for why include_siblings/
    dag_nosib/dag_full are gone (U6 consolidation)."""

    def __init__(
        self,
        client: MedBeadsClient,
        *,
        chain_depth: int = DEFAULT_CHAIN_DEPTH,
    ) -> None:
        self._client = client
        self._chain_depth = chain_depth
        self.arm = ARM_DAG

    async def retrieve(self, *, question: str, patient_id: str, budget: int) -> RetrievalResult:
        start = time.perf_counter()
        out = await self._client.retrieve(
            query=question,
            patient_id=patient_id,
            token_budget=budget,
            semantic=True,
            chain_depth=self._chain_depth,
        )
        elapsed_ms = (time.perf_counter() - start) * 1000.0

        # Deduplicate by id, keeping the first occurrence (retrieveOut's own
        # Items order is already tier-priority order — anchor before
        # ancestor before descendant — so "first occurrence" is
        # "highest-priority occurrence"). Go side is fixed at the source
        # (internal/engine/graph/context.go's BuildContext now resolves each
        # Bead's final tier in a first pass, materializing tiers[] only once
        # afterward — see BuildContext's own doc comment and
        # TestBuildContext_MultiAnchor_CrossAnchorTierPromotion_NoDuplicate,
        # internal/engine/graph/context_test.go — so a duplicate Bead ID
        # across retrieveOut.Items should no longer be structurally possible
        # from a correct server). This dedup is now defense-in-depth only:
        # cheap, harmless if the server-side invariant ever regresses, and
        # kept because a client should not trust a single upstream
        # invariant to hold forever without its own local safety net.
        items: list[dict[str, Any]] = out.get("items") or []
        seen_ids: set[str] = set()
        deduped: list[dict[str, Any]] = []
        for item in items:
            item_id = item["id"]
            if item_id in seen_ids:
                continue
            seen_ids.add(item_id)
            deduped.append(item)
        items = deduped

        bead_ids = [item["id"] for item in items]
        # retrieveOut's contextItemView.Text is "" for an L2-only item
        # (graph.ContextItem's own "L2 carries no content text" contract —
        # see context.go's renderItem) — preserved as "" here rather than
        # dropped, so bead_ids/texts stay the same length/order (this
        # module's RetrievalResult contract) and a caller can still tell an
        # L0/L1 item's real text apart from an L2 reference's deliberate
        # emptiness via meta["granularity"] below.
        texts = [item.get("text", "") for item in items]

        truncated: list[dict[str, Any]] = out.get("truncated_refs") or []
        anchor_ids: list[str] = out.get("anchor_ids") or []
        link_expansion: dict[str, Any] = out.get("link_expansion") or {}
        clinical_links: list[dict[str, Any]] = out.get("clinical_links") or []

        return RetrievalResult(
            arm=self.arm,
            bead_ids=bead_ids,
            texts=texts,
            used_tokens=out.get("used_tokens", 0),
            latency_ms=elapsed_ms,
            meta={
                "anchor_ids": anchor_ids,
                "granularity": [item.get("granularity", "") for item in items],
                "provenance": [item.get("provenance", "") for item in items],
                "truncated_ref_count": len(truncated),
                "linked_item_count": sum(item.get("provenance") == "clinical_link" for item in items),
                "clinical_link_count": len(clinical_links),
                "link_candidate_count": link_expansion.get("candidate_count", 0),
                "link_expansion_truncated": link_expansion.get("truncated", False),
            },
        )
