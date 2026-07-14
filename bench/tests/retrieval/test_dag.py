"""Unit tests for bench.retrieval.dag.DagRetriever — no real medbeadsd
needed, uses a fake client that returns a canned retrieveOut shape.

U6 consolidated dag_nosib/dag_full into a single `dag` arm (see dag.py's
module docstring) — these tests exercise that single arm, dropping the
former include_siblings toggle entirely.
"""

from __future__ import annotations

import asyncio
from typing import Any

from bench.retrieval.dag import ARM_DAG, DagRetriever


class _FakeClient:
    def __init__(self, retrieve_out: dict[str, Any]) -> None:
        self._out = retrieve_out
        self.last_call_kwargs: dict[str, Any] | None = None

    async def retrieve(self, **kwargs: Any) -> dict[str, Any]:
        self.last_call_kwargs = kwargs
        return self._out


def test_retrieve_passes_semantic_true_and_no_include_siblings_kwarg() -> None:
    client = _FakeClient({"items": [], "used_tokens": 0, "anchor_ids": [], "truncated_refs": []})
    retriever = DagRetriever(client, chain_depth=7)

    asyncio.run(retriever.retrieve(question="q", patient_id="sha256:p", budget=1000))

    assert client.last_call_kwargs == {
        "query": "q",
        "patient_id": "sha256:p",
        "token_budget": 1000,
        "semantic": True,
        "chain_depth": 7,
    }


def test_arm_name_is_dag() -> None:
    client = _FakeClient({"items": []})
    assert DagRetriever(client).arm == ARM_DAG == "dag"


def test_bead_ids_and_texts_come_from_items_in_order() -> None:
    out = {
        "items": [
            {"id": "sha256:a", "text": "anchor text", "granularity": "L0", "provenance": "anchor"},
            {"id": "sha256:b", "text": "ancestor text", "granularity": "L1", "provenance": "ancestor"},
            {"id": "sha256:c", "text": "", "granularity": "L2", "provenance": "descendant"},
        ],
        "used_tokens": 123,
        "anchor_ids": ["sha256:a"],
        "truncated_refs": [{"id": "sha256:d"}],
    }
    client = _FakeClient(out)
    retriever = DagRetriever(client)

    result = asyncio.run(retriever.retrieve(question="q", patient_id="sha256:p", budget=1000))

    assert result.bead_ids == ["sha256:a", "sha256:b", "sha256:c"]
    assert result.texts == ["anchor text", "ancestor text", ""]
    assert result.used_tokens == 123
    assert result.meta["anchor_ids"] == ["sha256:a"]
    assert result.meta["granularity"] == ["L0", "L1", "L2"]
    assert result.meta["provenance"] == ["anchor", "ancestor", "descendant"]
    assert result.meta["truncated_ref_count"] == 1
    assert result.meta["linked_item_count"] == 0
    assert result.meta["clinical_link_count"] == 0
    assert result.meta["link_candidate_count"] == 0
    assert result.meta["link_expansion_truncated"] is False
    assert "include_siblings" not in result.meta


def test_duplicate_item_ids_are_deduplicated_keeping_first_occurrence() -> None:
    # Regression test for a real, VERIFIED-against-scratch-data edge case in
    # graph.BuildContext (internal/engine/graph/context.go, out of this
    # bench unit's authorized change surface): a multi-anchor retrieve call
    # can return the same Bead ID twice in Items — once at a low-priority
    # tier claimed by one anchor, once at a higher-priority tier claimed by
    # a later anchor — because BuildContext's own dedup only blocks
    # re-claiming into an equal-or-worse tier, not the reverse. See dag.py's
    # own doc comment on this exact scenario.
    out = {
        "items": [
            {"id": "sha256:x", "text": "first (ancestor) occurrence", "granularity": "L1", "provenance": "ancestor"},
            {"id": "sha256:y", "text": "unique", "granularity": "L0", "provenance": "anchor"},
            {"id": "sha256:x", "text": "second (descendant) occurrence", "granularity": "L2", "provenance": "descendant"},
        ],
        "used_tokens": 50,
        "anchor_ids": ["sha256:y"],
        "truncated_refs": [],
    }
    client = _FakeClient(out)
    retriever = DagRetriever(client)

    result = asyncio.run(retriever.retrieve(question="q", patient_id="sha256:p", budget=1000))

    assert result.bead_ids == ["sha256:x", "sha256:y"]
    assert len(result.bead_ids) == len(set(result.bead_ids)) == 2
    # First occurrence (ancestor) wins, not the later (descendant) duplicate.
    assert result.texts == ["first (ancestor) occurrence", "unique"]
    assert result.meta["provenance"] == ["ancestor", "anchor"]


def test_missing_items_key_yields_empty_result() -> None:
    client = _FakeClient({})
    retriever = DagRetriever(client)

    result = asyncio.run(retriever.retrieve(question="q", patient_id="sha256:p", budget=1000))

    assert result.bead_ids == []
    assert result.texts == []
    assert result.used_tokens == 0
