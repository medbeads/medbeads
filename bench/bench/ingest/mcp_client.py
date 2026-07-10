"""Thin MCP stdio client wrapper around `medbeadsd serve -role system`.

Per R8.5 ("bench/ は MCP/REST 経由でのみ core に触れる"), this is the *only*
module in bench/ that talks to a medbeadsd process — it never imports
anything under internal/engine, only the `mcp` PyPI package (the official
Python SDK) talking to a spawned subprocess over stdio.
"""

from __future__ import annotations

from contextlib import AsyncExitStack
from pathlib import Path
from typing import Any

from mcp import ClientSession, StdioServerParameters
from mcp.client.stdio import stdio_client


class MedBeadsClient:
    """One live stdio session against `medbeadsd serve -role <role>`.

    Use as an async context manager:

        async with MedBeadsClient(medbeadsd_path, data_dir) as client:
            result = await client.create_bead(...)

    `role` defaults to "system" (write access, per bench.ingest's original
    use of this client for create_bead); bench.perf passes role="viewer"
    instead, since its only calls are read-only (retrieve) and least-
    privilege is the right default for a harness that never needs to write.
    """

    def __init__(
        self,
        medbeadsd_path: Path,
        data_dir: Path,
        *,
        role: str = "system",
        embedder_url: str | None = None,
        embed_model: str | None = None,
        embed_model_query: str | None = None,
    ) -> None:
        args = ["serve", "-data", str(data_dir), "-role", role]
        # embedder_url is additive/opt-in (bench/README.md's "Embedding
        # sidecar" invocation): omitting it reproduces the exact args this
        # client has always passed, so every existing caller (bench.ingest,
        # bench.perf) is unaffected. Needed by bench.retrieval's rag/dag_full/
        # dag_nosib arms (R8.2), which require retrieve(semantic=true)/
        # rag_search — both tool-level errors without -embedder configured
        # server-side (see internal/mcpserver/retrieve.go's own check).
        if embedder_url:
            args += ["-embedder", embedder_url]
            if embed_model:
                args += ["-embed-model", embed_model]
            if embed_model_query:
                args += ["-embed-model-query", embed_model_query]
        self._params = StdioServerParameters(
            command=str(medbeadsd_path),
            args=args,
        )
        self._stack: AsyncExitStack | None = None
        self._session: ClientSession | None = None

    async def __aenter__(self) -> "MedBeadsClient":
        stack = AsyncExitStack()
        try:
            read, write = await stack.enter_async_context(stdio_client(self._params))
            session = await stack.enter_async_context(ClientSession(read, write))
            await session.initialize()
        except BaseException:
            await stack.aclose()
            raise
        self._stack = stack
        self._session = session
        return self

    async def __aexit__(self, exc_type, exc, tb) -> None:
        if self._stack is not None:
            await self._stack.aclose()
        self._stack = None
        self._session = None

    async def call_tool(self, name: str, arguments: dict[str, Any]) -> dict[str, Any]:
        """Call MCP tool `name`, returning its structuredContent as a dict.

        Raises RuntimeError if the tool call itself reports isError=True
        (see internal/mcpserver/result.go's toolError: failures come back
        as a normal CallToolResult with isError set, not as a transport-
        level exception) or if the server returned no structured content at
        all (a bug in this client's tool-call arguments, not a normal
        ingest-time failure).
        """
        assert self._session is not None, "MedBeadsClient used outside 'async with'"
        result = await self._session.call_tool(name, arguments)
        if result.isError:
            text = "; ".join(
                block.text for block in result.content if getattr(block, "type", None) == "text"
            )
            raise RuntimeError(f"medbeadsd tool {name!r} failed: {text}")
        if result.structuredContent is None:
            raise RuntimeError(f"medbeadsd tool {name!r} returned no structuredContent")
        return result.structuredContent

    async def create_bead(
        self,
        *,
        bead_type: str,
        timestamp: str,
        author: str,
        parents: list[str],
        content: dict[str, Any],
    ) -> str:
        """Call create_bead, returning the new Bead's sha256:-prefixed ID."""
        out = await self.call_tool(
            "create_bead",
            {
                "type": bead_type,
                "timestamp": timestamp,
                "author": author,
                "parents": parents,
                "content": content,
            },
        )
        return out["bead"]["id"]

    async def list_patients(self) -> list[dict[str, Any]]:
        out = await self.call_tool("list_patients", {})
        return out.get("patients", [])

    async def get_timeline(self, patient_id: str) -> list[dict[str, Any]]:
        """Every Bead under patient_id (patient_root), timestamp order.

        Return key is "beads" (getTimelineOut.Beads in
        internal/mcpserver/tools_read.go).
        """
        out = await self.call_tool("get_timeline", {"patient_id": patient_id})
        return out.get("beads", [])

    async def get_bead(self, bead_id: str) -> dict[str, Any]:
        """One Bead's full, hash-verified content by ID.

        v3.1 (specs/DESIGN_v3.1_draft.md §2/§5): the returned Bead never
        carries antigens/tags — those are index-projection-only now (Bead
        itself has no Antigens field at all; see internal/engine/bead's
        Bead.Antigens removal). To check what tags a Bead was
        server-side-derived to carry, use search_antigens instead (it
        queries bead_antigens, which IndexBead populates via
        antigen.Extract at index time, not at create_bead time).
        """
        out = await self.call_tool("get_bead", {"id": bead_id})
        return out["bead"]

    async def search_antigens(self, antigen: str, *, patient_id: str | None = None) -> list[dict[str, Any]]:
        """Every Bead carrying `antigen` in the bead_antigens projection
        (internal/mcpserver/tools_read.go's search_antigens tool), i.e. the
        server-side antigen.Extract(b.Type, b.Content) result at index time
        — the only place a Bead's derived tags are queryable after v3.1
        removed Bead.Antigens (see get_bead's doc comment).
        """
        args: dict[str, Any] = {"antigen": antigen}
        if patient_id is not None:
            args["patient_id"] = patient_id
        out = await self.call_tool("search_antigens", args)
        return out.get("beads", [])

    async def retrieve(
        self,
        *,
        query: str = "",
        patient_id: str = "",
        token_budget: int | None = None,
        semantic: bool | None = None,
        include_siblings: bool | None = None,
        chain_depth: int | None = None,
        antigens: list[str] | None = None,
        types: list[str] | None = None,
    ) -> dict[str, Any]:
        """Call the unified `retrieve` tool (R6.2, internal/mcpserver/retrieve.go),
        returning its full structuredContent (anchor_ids/items/truncated_refs/
        budget_tokens/used_tokens — see retrieveOut). Used by bench.perf to
        measure the "context bundle p95 <500ms" target
        (docs/requirements.md §7), and by bench.retrieval's dag_nosib/dag_full
        arms (R8.2), which set semantic=True and toggle include_siblings.

        semantic/include_siblings/chain_depth/antigens/types are additive,
        opt-in parameters (omitting them reproduces exactly the args this
        method has always sent) — include_siblings maps to retrieveIn's
        `include_siblings` *bool field
        (internal/mcpserver/retrieve.go), whose Go-side default (True) is
        used whenever this Python method's own default (None) is passed
        through unset.
        """
        args: dict[str, Any] = {}
        if query:
            args["query"] = query
        if patient_id:
            args["patient_id"] = patient_id
        if token_budget is not None:
            args["token_budget"] = token_budget
        if semantic is not None:
            args["semantic"] = semantic
        if include_siblings is not None:
            args["include_siblings"] = include_siblings
        if chain_depth is not None:
            args["chain_depth"] = chain_depth
        if antigens:
            args["antigens"] = antigens
        if types:
            args["types"] = types
        return await self.call_tool("retrieve", args)

    async def search_beads(
        self,
        *,
        query: str,
        patient_id: str = "",
        limit: int | None = None,
    ) -> list[dict[str, Any]]:
        """Call `search_beads` (FTS5 trigram, internal/mcpserver/tools_read.go's
        searchBeads): returns beadRefs (id/patient_root/type/timestamp/summary
        — no content). Used by bench.retrieval's fts arm (R8.2), which must
        then get_bead each hit for its L0 content, mirroring rag_search's
        richer response shape but via the two-call FTS path search_beads
        itself deliberately keeps thin (see searchBeadsOut's doc comment).
        """
        args: dict[str, Any] = {"query": query}
        if patient_id:
            args["patient_id"] = patient_id
        if limit is not None:
            args["limit"] = limit
        out = await self.call_tool("search_beads", args)
        return out.get("results", [])

    async def rag_search(
        self,
        *,
        query: str,
        patient_id: str = "",
        k: int | None = None,
    ) -> list[dict[str, Any]]:
        """Call `rag_search` (R6.3, pure vector top-k, internal/mcpserver/
        rag_search.go): returns each hit's full L0 content plus vector
        distance directly (no follow-up get_bead needed, unlike
        search_beads). Used by bench.retrieval's rag arm (R8.2).
        """
        args: dict[str, Any] = {"query": query}
        if patient_id:
            args["patient_id"] = patient_id
        if k is not None:
            args["k"] = k
        out = await self.call_tool("rag_search", args)
        return out.get("results", [])

    async def apc_trigger(self) -> dict[str, Any]:
        """Call `apc_trigger` (system role only, internal/mcpserver/
        tools_write.go): runs one apc.Scanner.Scan pass, durably ingesting any
        new sibling_link Beads it finds. Used by bench's scratch-data test
        setup (and bench.ingest's future `bench run` orchestration, R8.4) to
        produce real sibling_link data via MCP only, per R8.5 — never by
        importing internal/engine/apc directly.
        """
        return await self.call_tool("apc_trigger", {})
