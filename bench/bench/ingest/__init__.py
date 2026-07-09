"""Synthea FHIR -> Bead ingestion (R8.1 M1 slice).

Reads Synthea FHIR R4 Bundles, deterministically maps the clinical resource
types to Beads (type/timestamp/parent-edge rules follow v2's
``scripts/import_fhir.py`` semantics, adapted to the v3 schema — see
docs/fhir_timeline_mapping.md and docs/mapping.md), and ingests them into a
running ``medbeadsd`` process via MCP over stdio (create_bead is
system-role-only; see internal/mcpserver/tools_write.go). Per R8.5, this
package never imports the Go engine directly — the MCP client in
``bench.ingest.mcp_client`` is the only channel to core.

This M1 slice produces the "ID map manifest" ground-truth material (JSONL of
fhir_resource_id -> bead_id) documented in specs/DESIGN_v3.md §9; scenario
generation (clinical question YAML) is M2 scope.
"""
