"""M1 performance harness: MCP `retrieve` p95 latency vs docs/requirements.md
§7's "context bundle p95 <500ms" target.

Run via `uv run python -m bench.perf --data-dir <dir> --medbeadsd <bin>`
(see bench/bench/perf/__main__.py for the full CLI). This package never
imports anything under internal/engine (R8.5: "bench/ は MCP/REST 経由での
み core に触れる") — bench.ingest.mcp_client.MedBeadsClient is reused as the
one MCP transport, and query text is derived from the same Synthea FHIR
source files bench.ingest already reads (never from core's own internals).
"""

from __future__ import annotations
