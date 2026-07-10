"""Reads bench.ingest's ground-truth ID-map manifest (manifest.jsonl, one
row per Bead — see bench/bench/ingest/patient.py's ManifestRow) into the
shape bench.scenarios.generate needs: fhir_resource_id -> bead_id, grouped
by patient_root.
"""

from __future__ import annotations

import json
from dataclasses import dataclass
from pathlib import Path


@dataclass(frozen=True)
class ManifestEntry:
    fhir_resource_id: str
    fhir_type: str
    bead_id: str
    patient_root: str
    timestamp: str
    parent_fallback: bool


@dataclass(frozen=True)
class PatientManifest:
    """One patient's manifest rows, indexed for scenario generation:
    by_fhir_id resolves a FHIR resource's own `id` to its ManifestEntry
    (bench.ingest.beads.plan_resource_bead sets fhir_id = resource.get("id",
    "") — the same key resource.get("id") on a raw FHIR resource yields, so
    this index can be looked up directly from parsed Bundle JSON without
    needing fullUrl at all)."""

    patient_root: str
    entries: list[ManifestEntry]

    def by_fhir_id(self) -> dict[str, ManifestEntry]:
        return {e.fhir_resource_id: e for e in self.entries if e.fhir_resource_id}

    def patient_root_entry(self) -> ManifestEntry:
        for e in self.entries:
            if e.fhir_type == "Patient":
                return e
        raise ValueError(f"PatientManifest for {self.patient_root}: no Patient row found")


def load_manifest(manifest_path: Path) -> list[ManifestEntry]:
    """Every row of manifest_path, in file order (bench.ingest.run writes
    rows as it goes: Patient root first, then every Encounter, then every
    other resource, per bench/bench/ingest/patient.py's two-pass order)."""
    out: list[ManifestEntry] = []
    with manifest_path.open("r", encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            row = json.loads(line)
            out.append(
                ManifestEntry(
                    fhir_resource_id=row["fhir_resource_id"],
                    fhir_type=row["fhir_type"],
                    bead_id=row["bead_id"],
                    patient_root=row["patient_root"],
                    timestamp=row["timestamp"],
                    parent_fallback=row.get("parent_fallback", False),
                )
            )
    return out


def group_by_patient(entries: list[ManifestEntry]) -> dict[str, PatientManifest]:
    """entries grouped by patient_root, in file (i.e. ingest) order within
    each group — deterministic since load_manifest's own row order is."""
    by_patient: dict[str, list[ManifestEntry]] = {}
    for e in entries:
        by_patient.setdefault(e.patient_root, []).append(e)
    return {root: PatientManifest(patient_root=root, entries=rows) for root, rows in by_patient.items()}
