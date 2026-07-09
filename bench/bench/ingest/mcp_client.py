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
    """One live stdio session against `medbeadsd serve -role system`.

    Use as an async context manager:

        async with MedBeadsClient(medbeadsd_path, data_dir) as client:
            result = await client.create_bead(...)
    """

    def __init__(self, medbeadsd_path: Path, data_dir: Path) -> None:
        self._params = StdioServerParameters(
            command=str(medbeadsd_path),
            args=["serve", "-data", str(data_dir), "-role", "system"],
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
        """One Bead's full, hash-verified content by ID (including
        server-derived `antigens` — see internal/mcpserver/tools_read.go's
        getBead / getBeadOut.Bead). Used by tests to verify antigen.Extract
        actually ran server-side on the content this client submitted (the
        reviewer's rxnorm-antigen assertion needs this, not just the
        create_bead response, to be an honest round-trip check).
        """
        out = await self.call_tool("get_bead", {"id": bead_id})
        return out["bead"]
