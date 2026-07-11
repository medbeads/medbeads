"""Unit tests for bench.ingest.fhir: bundle loading + resource filtering."""

from __future__ import annotations

import json
from pathlib import Path

import pytest

from bench.ingest.fhir import (
    EXCLUDED_RESOURCE_TYPES,
    INCLUDED_RESOURCE_TYPES,
    clinical_resources,
    count_dropped_superseded_document_references,
    find_patient_entry,
    is_patient_bundle_file,
    iter_patient_bundle_files,
    load_bundle,
    resource_encounter_reference,
    resource_timestamp,
)

FIXTURE_DIR = Path(__file__).resolve().parents[2].parent / "FHIR_sample"
FIXTURE_BUNDLE = FIXTURE_DIR / "Bauch324_Santina70_10.json"


def test_fixture_directory_exists() -> None:
    # Guards every other test in this file against a silently-empty fixture
    # dir (e.g. a path typo) producing false green results.
    assert FIXTURE_DIR.is_dir(), FIXTURE_DIR
    assert FIXTURE_BUNDLE.is_file(), FIXTURE_BUNDLE


def test_included_and_excluded_types_are_disjoint() -> None:
    assert INCLUDED_RESOURCE_TYPES.isdisjoint(EXCLUDED_RESOURCE_TYPES)


def test_document_reference_is_included_not_excluded() -> None:
    # U6 (specs/U6_clinical_note.md) reverses the earlier M1-slice decision
    # to exclude DocumentReference: it is now ingested (as a clinical_note
    # Bead, per bench.ingest.beads), not skipped.
    assert "DocumentReference" in INCLUDED_RESOURCE_TYPES
    assert "DocumentReference" not in EXCLUDED_RESOURCE_TYPES


def test_clinical_resources_drops_superseded_document_reference() -> None:
    bundle = {
        "resourceType": "Bundle",
        "entry": [
            {
                "fullUrl": "urn:uuid:doc-current",
                "resource": {
                    "resourceType": "DocumentReference",
                    "id": "doc-current",
                    "status": "current",
                    "date": "2024-01-01T00:00:00Z",
                    "context": {"encounter": [{"reference": "urn:uuid:enc-1"}]},
                    "content": [{"attachment": {"data": "aGVsbG8="}}],
                },
            },
            {
                "fullUrl": "urn:uuid:doc-superseded",
                "resource": {
                    "resourceType": "DocumentReference",
                    "id": "doc-superseded",
                    "status": "superseded",
                    "date": "2023-01-01T00:00:00Z",
                    "context": {"encounter": [{"reference": "urn:uuid:enc-0"}]},
                    "content": [{"attachment": {"data": "b2xk"}}],
                },
            },
        ],
    }

    resources = clinical_resources(bundle)
    doc_refs = [r for r in resources if r.resource_type == "DocumentReference"]
    assert [r.fhir_id for r in doc_refs] == ["doc-current"]

    assert count_dropped_superseded_document_references(bundle) == 1


def test_clinical_resources_drops_document_reference_without_status() -> None:
    # A DocumentReference missing the `status` field must be dropped too (only
    # status == "current" is ingested): `.get("status")` returns None and
    # `None != "current"`, so an absent/null status is filtered out exactly
    # like superseded. Pins the "absent status does not slip through" case
    # (data-reviewer non-blocking coverage note, 2026-07-11).
    bundle = {
        "resourceType": "Bundle",
        "entry": [
            {
                "fullUrl": "urn:uuid:doc-nostatus",
                "resource": {
                    "resourceType": "DocumentReference",
                    "id": "doc-nostatus",
                    "date": "2024-01-01T00:00:00Z",
                    "context": {"encounter": [{"reference": "urn:uuid:enc-1"}]},
                    "content": [{"attachment": {"data": "aGVsbG8="}}],
                },
            },
        ],
    }

    resources = clinical_resources(bundle)
    doc_refs = [r for r in resources if r.resource_type == "DocumentReference"]
    assert doc_refs == []


def test_is_patient_bundle_file_excludes_synthea_sidecars() -> None:
    assert not is_patient_bundle_file(Path("hospitalInformation1783634042442.json"))
    assert not is_patient_bundle_file(Path("practitionerInformation1783634042442.json"))
    assert is_patient_bundle_file(Path("Bauch324_Santina70_10.json"))


def test_iter_patient_bundle_files_sorted_and_filters_sidecars(tmp_path: Path) -> None:
    (tmp_path / "hospitalInformation1.json").write_text("{}")
    (tmp_path / "practitionerInformation1.json").write_text("{}")
    (tmp_path / "Zed_Zeta_1.json").write_text("{}")
    (tmp_path / "Abe_Alpha_2.json").write_text("{}")

    files = iter_patient_bundle_files(tmp_path)

    assert [f.name for f in files] == ["Abe_Alpha_2.json", "Zed_Zeta_1.json"]


def test_load_bundle_reads_json() -> None:
    bundle = load_bundle(FIXTURE_BUNDLE)
    assert bundle["resourceType"] == "Bundle"
    assert "entry" in bundle


def test_find_patient_entry() -> None:
    bundle = load_bundle(FIXTURE_BUNDLE)
    entry = find_patient_entry(bundle)
    assert entry is not None
    assert entry["resource"]["resourceType"] == "Patient"
    assert entry["resource"]["id"] == "bccd5886-4ceb-4eb2-85f6-b8bafbbd1f1b"


def test_clinical_resources_excludes_patient_and_non_clinical_types() -> None:
    bundle = load_bundle(FIXTURE_BUNDLE)
    resources = clinical_resources(bundle)

    types_seen = {r.resource_type for r in resources}
    assert "Patient" not in types_seen
    assert types_seen <= INCLUDED_RESOURCE_TYPES

    # This fixture bundle contains a CarePlan and a Medication (per direct
    # inspection) — both explicitly excluded (non-clinical/administrative,
    # see bench.ingest.fhir's module docstring); assert they are actually
    # absent from the filtered output rather than merely absent from
    # types_seen by coincidence of INCLUDED_RESOURCE_TYPES's definition.
    raw_types = {e["resource"]["resourceType"] for e in bundle["entry"]}
    assert "CarePlan" in raw_types  # sanity: fixture really has one
    assert "CarePlan" not in types_seen


def test_clinical_resources_includes_encounter_and_observation() -> None:
    bundle = load_bundle(FIXTURE_BUNDLE)
    resources = clinical_resources(bundle)
    types_seen = {r.resource_type for r in resources}
    assert "Encounter" in types_seen
    assert "Observation" in types_seen
    assert "MedicationRequest" in types_seen
    assert "Immunization" in types_seen


def test_resource_timestamp_encounter_uses_period_start() -> None:
    bundle = load_bundle(FIXTURE_BUNDLE)
    resources = clinical_resources(bundle)
    encounters = [r for r in resources if r.resource_type == "Encounter"]
    assert encounters
    first = next(r for r in encounters if r.fhir_id == "b4b30dae-6234-4b60-8ade-1ea65d783b80")
    assert resource_timestamp(first) == "2010-11-28T04:38:01-05:00"


def test_resource_timestamp_immunization_uses_encounter_date_field() -> None:
    # This fixture's Immunization resources use plain `date`, not
    # `occurrenceDateTime` (an older/DSTU2-flavored Synthea export) and
    # carry no `id` at all — resource_timestamp should fall back to the
    # sentinel rather than raise, and fhir_id should come back empty (not
    # crash on a missing "id" key).
    bundle = load_bundle(FIXTURE_BUNDLE)
    resources = clinical_resources(bundle)
    immunizations = [r for r in resources if r.resource_type == "Immunization"]
    assert immunizations
    assert resource_timestamp(immunizations[0]) == "2099-01-01"
    assert immunizations[0].fhir_id == ""


def test_resource_encounter_reference() -> None:
    bundle = load_bundle(FIXTURE_BUNDLE)
    resources = clinical_resources(bundle)
    immunizations = [r for r in resources if r.resource_type == "Immunization"]
    ref, from_nested = resource_encounter_reference(immunizations[0])
    assert ref == "urn:uuid:b4b30dae-6234-4b60-8ade-1ea65d783b80"
    assert from_nested is False

    # This fixture's Condition/Procedure/Observation/MedicationRequest use
    # an older `context: {reference: ...}` field (DSTU2-flavored, a bare
    # reference object, not R4 DocumentReference's `context: {encounter:
    # [...]}` shape) — per v2's import_fhir.py semantics (which this module
    # preserves verbatim), resource_encounter_reference must return
    # (None, False) here (neither the top-level `encounter` field nor a
    # `context.encounter[]` list is present — this fixture's `context` has
    # no `encounter` key at all), so plan_resource_bead's generic "fall back
    # to Patient root" branch is what handles them, not a fabricated
    # Encounter edge.
    conditions = [r for r in resources if r.resource_type == "Condition"]
    assert conditions
    assert resource_encounter_reference(conditions[0]) == (None, False)


def test_resource_encounter_reference_nested_context_encounter_array() -> None:
    """U6 (specs/U6_clinical_note.md): a resource with no top-level
    `encounter` field but a nested `context.encounter[0].reference` (the
    real DocumentReference shape) should resolve via the nested path, with
    from_nested=True."""
    from bench.ingest.fhir import FhirResource

    resource = FhirResource(
        resource_type="DocumentReference",
        fhir_id="doc-1",
        full_url="urn:uuid:doc-1",
        data={
            "resourceType": "DocumentReference",
            "id": "doc-1",
            "status": "current",
            "context": {"encounter": [{"reference": "urn:uuid:encounter-1"}]},
        },
    )
    ref, from_nested = resource_encounter_reference(resource)
    assert ref == "urn:uuid:encounter-1"
    assert from_nested is True


def test_resource_encounter_reference_top_level_wins_over_nested() -> None:
    """A resource carrying both a top-level `encounter` and a nested
    `context.encounter[]` keeps the top-level value (resource_encounter_
    reference's documented priority — see its own doc comment)."""
    from bench.ingest.fhir import FhirResource

    resource = FhirResource(
        resource_type="Observation",
        fhir_id="obs-1",
        full_url=None,
        data={
            "resourceType": "Observation",
            "id": "obs-1",
            "encounter": {"reference": "urn:uuid:top-level-encounter"},
            "context": {"encounter": [{"reference": "urn:uuid:nested-encounter"}]},
        },
    )
    ref, from_nested = resource_encounter_reference(resource)
    assert ref == "urn:uuid:top-level-encounter"
    assert from_nested is False
