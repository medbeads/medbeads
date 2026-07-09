"""Single-patient ingest: FHIR Bundle -> ordered create_bead calls.

Owns the per-bundle id_map (FHIR id/fullUrl -> real Bead ID) v2's
import_fhir.py kept as a module-level dict; here it is a local variable
scoped to one patient so patients can be ingested independently (a failure
on one patient must not corrupt or leak into another's id_map — see the
task's "エラーは患者単位でリトライ/記録").

Two-pass ingest order (reviewer-mandated fix: "同時刻の親子で ingest 順序が破れる"):
a plain (timestamp, fhir_id) sort over *all* clinical resources does not
guarantee every Encounter is ingested before its children, because Synthea
frequently stamps an Encounter and same-visit children (Observation,
Procedure, ...) with the identical instant, and a child's fhir_id can sort
lexicographically before its own Encounter's fhir_id at that tie — VERIFIED
against the reviewer's finding (this affects a non-trivial fraction of
real Bundles: same-timestamp Encounter/child pairs are common, e.g. one
Observation panel drawn during a visit shares a timestamp with the visit's
own Encounter.period.start). The reviewer also confirmed (real-data check)
that Synthea's Encounter resources are never nested (`partOf` is never
present in the sampled corpus — VERIFIED separately, 200-file sample, 0
occurrences), so a simple two-pass split is sufficient and correct: no
Encounter here is itself a child of another Encounter, so passing over all
Encounters first (in (timestamp, fhir_id) order, unchanged) and only then
every remaining resource (same order) guarantees every child's Encounter
parent already has a real Bead ID in id_map by the time the child is
planned — no topological sort is needed beyond this one split.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

from bench.ingest.beads import INGEST_AUTHOR, plan_patient_root, plan_resource_bead, sort_key
from bench.ingest.fhir import (
    clinical_resources,
    find_patient_entry,
    index_medications_by_ref,
    load_bundle,
)
from bench.ingest.mcp_client import MedBeadsClient


@dataclass
class ManifestRow:
    """One line of the ground-truth ID-map manifest (JSONL), per the task's
    {fhir_resource_id, fhir_type, bead_id, patient_root, timestamp} shape,
    plus `parent_fallback` (reviewer-mandated addition: True if this Bead's
    Encounter reference did not resolve in id_map and it was attached to
    the Patient root instead of its intended Encounter — see
    ingest_patient_bundle's "サイレント禁止" handling below). False for
    every Bead whose parent resolved as intended (including Encounters and
    AllergyIntolerance, which fall back to the Patient root *by design*,
    not because a reference failed to resolve — see bench.ingest.beads'
    edge-rule doc comment).
    """

    fhir_resource_id: str
    fhir_type: str
    bead_id: str
    patient_root: str
    timestamp: str
    parent_fallback: bool = False

    def to_json_dict(self) -> dict[str, Any]:
        return {
            "fhir_resource_id": self.fhir_resource_id,
            "fhir_type": self.fhir_type,
            "bead_id": self.bead_id,
            "patient_root": self.patient_root,
            "timestamp": self.timestamp,
            "parent_fallback": self.parent_fallback,
        }


@dataclass
class PatientIngestResult:
    bundle_path: Path
    ok: bool
    error: str | None = None
    manifest_rows: list[ManifestRow] = field(default_factory=list)
    bead_count: int = 0
    warnings: list[str] = field(default_factory=list)


async def ingest_patient_bundle(client: MedBeadsClient, bundle_path: Path) -> PatientIngestResult:
    """Ingest one Synthea Bundle file: Patient root, then every Encounter
    (timestamp, fhir_id order), then every remaining clinical resource
    (timestamp, fhir_id order) — the two-pass split documented in this
    module's docstring — each create_bead call carrying the id_map-resolved
    parent per bench.ingest.beads' edge rule.

    Never raises for a per-patient failure (e.g. a malformed bundle, or a
    create_bead RPC error partway through) — the task requires ingest to
    keep going for the remaining patients, so failures are captured in
    PatientIngestResult.error and re-raised only for genuinely unexpected
    bugs (there are none we anticipate; this function's own try/except is
    the single point where "this patient's data is untrustworthy, skip it"
    is decided).
    """
    try:
        bundle = load_bundle(bundle_path)
    except Exception as exc:  # noqa: BLE001 - convert to a per-patient result, not a crash
        return PatientIngestResult(bundle_path=bundle_path, ok=False, error=f"load bundle: {exc}")

    patient_entry = find_patient_entry(bundle)
    if patient_entry is None:
        return PatientIngestResult(bundle_path=bundle_path, ok=False, error="no Patient resource in bundle")

    planned_root = plan_patient_root(patient_entry)

    try:
        root_bead_id = await client.create_bead(
            bead_type=planned_root.bead_type,
            timestamp=planned_root.timestamp,
            author=INGEST_AUTHOR,
            parents=[],
            content=planned_root.content,
        )
    except Exception as exc:  # noqa: BLE001
        return PatientIngestResult(bundle_path=bundle_path, ok=False, error=f"create root bead: {exc}")

    # id_map: FHIR id/fullUrl (both forms, per v2's import_fhir.py, since
    # other resources' `reference` fields may use either "urn:uuid:<id>" or
    # the bare id) -> real Bead ID.
    id_map: dict[str, str] = {}
    if planned_root.fhir_id:
        id_map[planned_root.fhir_id] = root_bead_id
    if planned_root.full_url:
        id_map[planned_root.full_url] = root_bead_id

    manifest_rows = [
        ManifestRow(
            fhir_resource_id=planned_root.fhir_id,
            fhir_type="Patient",
            bead_id=root_bead_id,
            patient_root=root_bead_id,
            timestamp=planned_root.timestamp,
        )
    ]
    warnings: list[str] = []

    medications_by_ref = index_medications_by_ref(bundle)

    resources = clinical_resources(bundle)
    encounters = sorted((r for r in resources if r.resource_type == "Encounter"), key=sort_key)
    non_encounters = sorted((r for r in resources if r.resource_type != "Encounter"), key=sort_key)

    patient_ref = planned_root.full_url or planned_root.fhir_id

    # Pass 1: every Encounter first (see module docstring — Encounters are
    # never nested in this dataset, so this pass alone guarantees every
    # Encounter has a real Bead ID in id_map before pass 2 plans anything
    # that might reference it).
    for resource in encounters:
        planned = plan_resource_bead(resource, patient_ref, medications_by_ref)
        # Encounters always parent to the Patient root (bench.ingest.beads'
        # edge rule) — this can never be an unresolved-reference fallback,
        # so parent_fallback is unconditionally False here.
        try:
            bead_id = await client.create_bead(
                bead_type=planned.bead_type,
                timestamp=planned.timestamp,
                author=INGEST_AUTHOR,
                parents=[root_bead_id],
                content=planned.content,
            )
        except Exception as exc:  # noqa: BLE001
            return PatientIngestResult(
                bundle_path=bundle_path,
                ok=False,
                error=f"create bead for {planned.fhir_type} {planned.fhir_id}: {exc}",
                manifest_rows=manifest_rows,
                bead_count=len(manifest_rows),
                warnings=warnings,
            )

        if planned.fhir_id:
            id_map[planned.fhir_id] = bead_id
        if planned.full_url:
            id_map[planned.full_url] = bead_id

        manifest_rows.append(
            ManifestRow(
                fhir_resource_id=planned.fhir_id,
                fhir_type=planned.fhir_type,
                bead_id=bead_id,
                patient_root=root_bead_id,
                timestamp=planned.timestamp,
            )
        )

    # Pass 2: everything else, in (timestamp, fhir_id) order. Every
    # Encounter this pass could possibly reference already has a real Bead
    # ID in id_map from pass 1.
    for resource in non_encounters:
        planned = plan_resource_bead(resource, patient_ref, medications_by_ref)

        parent_bead_id = id_map.get(planned.parent_ref) if planned.parent_ref else None
        is_fallback = False
        if parent_bead_id is None:
            # planned.parent_ref was an Encounter reference that did not
            # resolve in id_map (a Bundle where a resource references an
            # Encounter FHIR id/fullUrl not present anywhere in this
            # Bundle — e.g. a malformed or partial export) — NOT the
            # designed "no encounter reference at all" case, which
            # plan_resource_bead already resolves to patient_ref itself
            # (so parent_bead_id would be root_bead_id via a normal id_map
            # hit, not this None branch). Per the reviewer's "サイレント禁止"
            # requirement, this is logged and flagged in the manifest
            # rather than silently reattached.
            if planned.parent_ref != patient_ref:
                warnings.append(
                    f"{planned.fhir_type} {planned.fhir_id}: encounter reference "
                    f"{planned.parent_ref!r} not found in id_map; falling back to patient root"
                )
                is_fallback = True
            parent_bead_id = root_bead_id
        parents = [parent_bead_id]

        try:
            bead_id = await client.create_bead(
                bead_type=planned.bead_type,
                timestamp=planned.timestamp,
                author=INGEST_AUTHOR,
                parents=parents,
                content=planned.content,
            )
        except Exception as exc:  # noqa: BLE001
            return PatientIngestResult(
                bundle_path=bundle_path,
                ok=False,
                error=f"create bead for {planned.fhir_type} {planned.fhir_id}: {exc}",
                manifest_rows=manifest_rows,
                bead_count=len(manifest_rows),
                warnings=warnings,
            )

        if planned.fhir_id:
            id_map[planned.fhir_id] = bead_id
        if planned.full_url:
            id_map[planned.full_url] = bead_id

        manifest_rows.append(
            ManifestRow(
                fhir_resource_id=planned.fhir_id,
                fhir_type=planned.fhir_type,
                bead_id=bead_id,
                patient_root=root_bead_id,
                timestamp=planned.timestamp,
                parent_fallback=is_fallback,
            )
        )

    return PatientIngestResult(
        bundle_path=bundle_path,
        ok=True,
        manifest_rows=manifest_rows,
        bead_count=len(manifest_rows),
        warnings=warnings,
    )
