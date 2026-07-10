"""Clinical question scenario generation (R8.1/R8.2's ground-truth half).

Reads bench.ingest's manifest.jsonl (fhir_resource_id -> bead_id) plus the
original Synthea FHIR Bundles (the same --fhir-dir bench.ingest read from)
and deterministically generates clinical question/answer/evidence-Bead-ID
scenarios across four pilot categories — see bench.scenarios.generate's
module docstring for the templates and bench.scenarios.model for the YAML
output shape.

Run via `uv run python -m bench.scenarios --fhir-dir <dir> --manifest
<path> --out <path> [--patients N] [--per-patient M]` — see __main__.py.
This package never talks to a running medbeadsd process at all (not even
via MCP): both its inputs (the manifest, the source FHIR Bundles) are
already-written files from a prior `bench.ingest` run.
"""
