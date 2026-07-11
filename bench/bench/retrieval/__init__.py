"""Retriever interface + 3-arm implementation (R8.2, specs/DESIGN_v3.md §9).

Every arm implements the same ``Retriever`` protocol (``retrieve(question,
patient_id, budget) -> RetrievalResult``) so bench.metrics can score them
identically regardless of which MCP tool an arm calls underneath — chunk =
Bead is the unifying unit across all three (docs/requirements.md R8.2's
"チャンク = Bead に統一").

Arms (bench/bench/retrieval/):

  - ``rag.py`` (RagRetriever): pure vector top-k via ``rag_search``, greedily
    packed into budget as L0 content.
  - ``fts.py`` (FtsRetriever): FTS5 hits via ``search_beads`` (+ get_bead for
    content), greedily packed the same way.
  - ``dag.py`` (DagRetriever): the unified ``retrieve`` MCP tool with
    semantic=True, using its own token-budgeted L0/L1/L2 packing
    (graph.BuildContext) rather than this module's greedy-L0-only packing.
    U6 (specs/U6_clinical_note.md) consolidated the former dag_nosib/
    dag_full split into this single arm — see dag.py's own docstring for why
    (U5a removed graph's sibling tiers, making the two measure identical
    numbers).

Per R8.5, every arm talks to core exclusively through
``bench.ingest.mcp_client.MedBeadsClient`` (MCP) — never internal/engine
directly.
"""

from __future__ import annotations

from bench.retrieval.base import RetrievalResult, Retriever
from bench.retrieval.dag import DagRetriever
from bench.retrieval.fts import FtsRetriever
from bench.retrieval.rag import RagRetriever

__all__ = [
    "RetrievalResult",
    "Retriever",
    "RagRetriever",
    "FtsRetriever",
    "DagRetriever",
]
