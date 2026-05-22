# MedBeads MCP Server

A [Model Context Protocol](https://modelcontextprotocol.io) server that exposes
the MedBeads immutable clinical Merkle-DAG to any MCP client (Claude Desktop,
Claude Code, …). It lets an agent **deterministically traverse** a patient's
causal history instead of probabilistically retrieving it.

The server is read-only and talks to the Go core's REST API; it does not use the
Gemini AI layer.

## Tools

| Tool | Description |
|------|-------------|
| `list_patients()` | List all patient root Beads. |
| `search_beads(query, resource_types="")` | Full-text / resource-type search for an entry-point Bead. |
| `get_bead(bead_id)` | Retrieve one Bead by its content-hash id. |
| `get_context(bead_id, depth=5)` | Ancestor traversal — the causal history of a Bead ("why did this happen?"). |
| `get_patient_timeline(patient_id, depth=50)` | Descendant traversal — a patient's full timeline. |
| `get_resource_counts()` | Bead counts per clinical resource type. |

## Resources

- `medbeads://bead/{bead_id}` — a single Bead.
- `medbeads://patient/{patient_id}` — a patient's full timeline.

## Access control (important)

The MCP server runs with a **fixed clearance context set at startup** via
environment variables. The agent/LLM cannot choose its own roles, so it cannot
escalate privileges — the core masks the content of any Bead the configured role
is not authorized to see.

| Variable | Default | Purpose |
|----------|---------|---------|
| `CORE_URL` | `http://localhost:8080` | MedBeads Go core base URL. |
| `MEDBEADS_VIEWER_ROLES` | `primary_care` | Comma-separated roles, e.g. `specialist,dept:genetics`. |
| `MEDBEADS_USER_ID` | `mcp-agent` | Identity recorded in the core's audit log. |
| `MEDBEADS_SERVICE_TOKEN` | _(unset)_ | Only needed to assert the privileged `system` role. |
| `MEDBEADS_ACCESS_REASON` | _(unset)_ | Optional reason string for the audit log. |

## Running

```bash
cd medbeads/mcp
uv sync
uv run server.py            # stdio transport
```

Inspect interactively with the MCP Inspector:

```bash
uv run mcp dev server.py
```

## Connecting an MCP client

Claude Desktop (`claude_desktop_config.json`) or Claude Code (`.mcp.json`):

```json
{
  "mcpServers": {
    "medbeads": {
      "command": "uv",
      "args": ["--directory", "/absolute/path/to/medbeads/mcp", "run", "server.py"],
      "env": {
        "CORE_URL": "http://localhost:8080",
        "MEDBEADS_VIEWER_ROLES": "primary_care"
      }
    }
  }
}
```

The MedBeads core must be running and populated (see the repository root README
and `sample_data/`).

## Tests

```bash
uv sync --dev
uv run pytest -q
```
