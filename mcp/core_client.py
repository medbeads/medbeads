"""HTTP client for the MedBeads Go core, used by the MCP server.

The viewer context (clearance roles, user id) is fixed at process start from
environment variables and forwarded to the core on every request. The MCP
client / LLM never chooses its own roles, so it cannot escalate its privileges:
the core masks the content of any Bead the configured role may not see.
"""

import os

import requests

CORE_URL = os.environ.get("CORE_URL", "http://localhost:8080")
_TIMEOUT = 30


class CoreError(RuntimeError):
    """Raised when the MedBeads core returns an error or is unreachable."""


def viewer_headers() -> dict:
    """Access-control headers forwarded to the core on every request.

    Roles come from the environment, not from the agent. ``X-Service-Token`` and
    ``X-Access-Reason`` are only sent when explicitly configured.
    """
    headers = {
        "X-Viewer-Roles": os.environ.get("MEDBEADS_VIEWER_ROLES", "primary_care"),
        "X-User-ID": os.environ.get("MEDBEADS_USER_ID", "mcp-agent"),
    }
    token = os.environ.get("MEDBEADS_SERVICE_TOKEN", "")
    if token:
        headers["X-Service-Token"] = token
    reason = os.environ.get("MEDBEADS_ACCESS_REASON", "")
    if reason:
        headers["X-Access-Reason"] = reason
    return headers


def _get(path: str, params: dict | None = None):
    """GET a core endpoint, returning parsed JSON or raising CoreError."""
    try:
        resp = requests.get(
            f"{CORE_URL}{path}",
            params=params or {},
            headers=viewer_headers(),
            timeout=_TIMEOUT,
        )
    except requests.exceptions.RequestException as e:
        raise CoreError(f"MedBeads core is unreachable at {CORE_URL}: {e}") from e

    if resp.status_code == 404:
        raise CoreError(f"Not found: {path} {params or ''}")
    if resp.status_code >= 400:
        raise CoreError(f"Core returned HTTP {resp.status_code}: {resp.text[:200]}")
    return resp.json()


def list_patients():
    """All patient root Beads."""
    return _get("/patients")


def search_beads(query: str, resource_types: str = ""):
    """Full-text / resource-type search over indexed Beads."""
    params: dict = {}
    if query:
        params["q"] = query
    if resource_types:
        params["resourceTypes"] = resource_types
    return _get("/search", params)


def get_bead(bead_id: str):
    """A single Bead by its content-hash id."""
    return _get("/beads", {"id": bead_id})


def get_context(bead_id: str, depth: int = 5):
    """Ancestor Beads of the target (causal history, upward traversal)."""
    return _get("/beads/context", {"id": bead_id, "depth": depth})


def get_patient_timeline(patient_id: str, depth: int = 50):
    """Descendant Beads of a patient root (full timeline, downward traversal)."""
    return _get("/beads/context", {"id": patient_id, "depth": depth, "lookup": "reverse"})


def get_resource_counts():
    """Count of Beads per clinical resource type."""
    return _get("/resource-counts")
