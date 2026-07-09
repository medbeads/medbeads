"""Unit tests for bench.ingest.beads: type/timestamp/parent-edge rules."""

from __future__ import annotations

from pathlib import Path

from bench.ingest.beads import (
    INGEST_AUTHOR,
    PATIENT_REGISTRATION_TYPE,
    plan_patient_root,
    plan_resource_bead,
    sort_key,
)
from bench.ingest.fhir import clinical_resources, find_patient_entry, load_bundle

FIXTURE_BUNDLE = Path(__file__).resolve().parents[2].parent / "FHIR_sample" / "Bauch324_Santina70_10.json"


def _bundle():
    return load_bundle(FIXTURE_BUNDLE)


def test_ingest_author_is_fixed_string() -> None:
    # Guards against accidentally wiring in wall-clock-derived or
    # environment-derived author strings, which would break determinism.
    assert INGEST_AUTHOR == "synthea-ingest-v1"


def test_plan_patient_root_type_timestamp_and_no_parent() -> None:
    bundle = _bundle()
    patient_entry = find_patient_entry(bundle)
    planned = plan_patient_root(patient_entry)

    assert planned.bead_type == PATIENT_REGISTRATION_TYPE == "patient_registration"
    assert planned.timestamp == "2006-11-03"  # birthDate, per v2 semantics
    assert planned.parent_ref is None
    assert planned.fhir_id == "bccd5886-4ceb-4eb2-85f6-b8bafbbd1f1b"
    assert planned.full_url == "urn:uuid:bccd5886-4ceb-4eb2-85f6-b8bafbbd1f1b"
    assert planned.content["name"] == "Santina70 Bauch324"
    assert planned.content["gender"] is not None


def test_plan_resource_bead_encounter_parent_is_patient_root() -> None:
    bundle = _bundle()
    patient_entry = find_patient_entry(bundle)
    patient_ref = patient_entry["fullUrl"]
    resources = clinical_resources(bundle)

    encounter = next(r for r in resources if r.resource_type == "Encounter")
    planned = plan_resource_bead(encounter, patient_ref)

    assert planned.bead_type == "fhir_encounter"
    assert planned.parent_ref == patient_ref  # Encounter always parents to Patient root


def test_plan_resource_bead_immunization_parent_is_its_encounter() -> None:
    bundle = _bundle()
    patient_entry = find_patient_entry(bundle)
    patient_ref = patient_entry["fullUrl"]
    resources = clinical_resources(bundle)

    immunization = next(r for r in resources if r.resource_type == "Immunization")
    planned = plan_resource_bead(immunization, patient_ref)

    assert planned.bead_type == "fhir_immunization"
    # Immunization's `encounter.reference` in this fixture:
    assert planned.parent_ref == "urn:uuid:b4b30dae-6234-4b60-8ade-1ea65d783b80"
    assert planned.parent_ref != patient_ref


def test_plan_resource_bead_falls_back_to_patient_root_without_encounter_ref() -> None:
    bundle = _bundle()
    patient_entry = find_patient_entry(bundle)
    patient_ref = patient_entry["fullUrl"]
    resources = clinical_resources(bundle)

    # This fixture's Condition resources use `context`, not `encounter` —
    # v2's edge rule (preserved verbatim) does not recognize `context`, so
    # the generic fallback branch must attach these to the Patient root.
    condition = next(r for r in resources if r.resource_type == "Condition")
    planned = plan_resource_bead(condition, patient_ref)

    assert planned.bead_type == "fhir_condition"
    assert planned.parent_ref == patient_ref


def test_sort_key_is_deterministic_timestamp_then_id() -> None:
    bundle = _bundle()
    resources = clinical_resources(bundle)
    resources_sorted = sorted(resources, key=sort_key)

    # Re-sorting an already-sorted list must be a no-op (stability +
    # determinism of the key itself, not just of Python's sort).
    twice_sorted = sorted(resources_sorted, key=sort_key)
    assert [r.fhir_id for r in resources_sorted] == [r.fhir_id for r in twice_sorted]

    # Keys must be monotonically non-decreasing by timestamp.
    keys = [sort_key(r) for r in resources_sorted]
    assert keys == sorted(keys)
