"""MedBeads MCP server.

Exposes the MedBeads immutable clinical Merkle-DAG to Model Context Protocol
clients (Claude Desktop, Claude Code, ...) as a set of read-only tools and
resources. Access is governed by the clearance roles configured in the
environment (see core_client.viewer_headers); Beads the configured role may not
see are returned content-masked by the core, so an agent only ever receives the
subgraph it is authorized for.
"""

import json

from mcp.server.fastmcp import FastMCP

import core_client

mcp = FastMCP("MedBeads")


@mcp.tool()
def list_patients() -> list[dict]:
    """List all patient root Beads in the MedBeads store.

    Returns one entry per patient with the patient's Bead id, type, timestamp
    and content (name, demographics). Use an id with get_patient_timeline or
    get_context.
    """
    return core_client.list_patients()


@mcp.tool()
def search_beads(query: str, resource_types: str = "") -> list[dict]:
    """Full-text search over clinical Beads.

    Args:
        query: free-text search terms (e.g. a condition or medication name).
        resource_types: optional comma-separated types to filter by
            (e.g. "fhir_condition,fhir_observation").

    Use this to locate an entry-point Bead, then call get_context to retrieve
    its deterministic causal history.
    """
    return core_client.search_beads(query, resource_types)


@mcp.tool()
def get_bead(bead_id: str) -> dict:
    """Retrieve a single Bead by its content-hash id."""
    return core_client.get_bead(bead_id)


@mcp.tool()
def get_context(bead_id: str, depth: int = 5) -> list[dict]:
    """Retrieve the causal context (ancestors) of a Bead.

    Traverses the Merkle DAG upward through parent links to deterministically
    return every Bead that causally precedes the target. This answers
    "why did this event occur?" — call it before reasoning about a clinical
    event. depth bounds the traversal (valid range 1-50).
    """
    return core_client.get_context(bead_id, depth)


@mcp.tool()
def get_patient_timeline(patient_id: str, depth: int = 50) -> list[dict]:
    """Retrieve a patient's full timeline (descendants of the patient root).

    Traverses the DAG downward from the patient registration Bead to collect
    every clinical event recorded for that patient.
    """
    return core_client.get_patient_timeline(patient_id, depth)


@mcp.tool()
def get_resource_counts() -> dict:
    """Return the count of Beads per clinical resource type across the store."""
    return core_client.get_resource_counts()


@mcp.resource("medbeads://bead/{bead_id}")
def bead_resource(bead_id: str) -> str:
    """A single Bead, addressed by its content hash."""
    return json.dumps(core_client.get_bead(bead_id), ensure_ascii=False, indent=2)


@mcp.resource("medbeads://patient/{patient_id}")
def patient_resource(patient_id: str) -> str:
    """A patient's full clinical timeline, addressed by patient root id."""
    return json.dumps(
        core_client.get_patient_timeline(patient_id), ensure_ascii=False, indent=2
    )


def main() -> None:
    """Run the server over stdio (the transport MCP desktop clients use)."""
    mcp.run()


if __name__ == "__main__":
    main()
