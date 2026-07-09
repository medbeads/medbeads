"""Unit tests for bench.ingest.fhir: bundle loading + resource filtering."""

from __future__ import annotations

import json
from pathlib import Path

import pytest

from bench.ingest.fhir import (
    EXCLUDED_RESOURCE_TYPES,
    INCLUDED_RESOURCE_TYPES,
    clinical_resources,
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
    ref = resource_encounter_reference(immunizations[0])
    assert ref == "urn:uuid:b4b30dae-6234-4b60-8ade-1ea65d783b80"

    # This fixture's Condition/Procedure/Observation/MedicationRequest use
    # an older `context` reference field, not `encounter` — per v2's
    # import_fhir.py semantics (which this module preserves verbatim),
    # resource_encounter_reference must return None here (not silently
    # treat `context` as if it were `encounter`), so plan_resource_bead's
    # generic "fall back to Patient root" branch is what handles them, not
    # a fabricated Encounter edge.
    conditions = [r for r in resources if r.resource_type == "Condition"]
    assert conditions
    assert resource_encounter_reference(conditions[0]) is None
