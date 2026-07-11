"""Unit tests for bench.ingest.beads: type/timestamp/parent-edge rules."""

from __future__ import annotations

import base64
from pathlib import Path

from bench.ingest.beads import (
    CLINICAL_NOTE_TYPE,
    INGEST_AUTHOR,
    PATIENT_REGISTRATION_TYPE,
    plan_clinical_note_bead,
    plan_patient_root,
    plan_resource_bead,
    sort_key,
)
from bench.ingest.fhir import FhirResource, clinical_resources, find_patient_entry, load_bundle

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


def _synthetic_document_reference(
    *,
    status: str = "current",
    note_text: str = "Chief Complaint\nSynthetic patient reports mild headache.",
    encounter_ref: str | None = "urn:uuid:encounter-1",
    note_type_code: str | None = "34117-2",
) -> FhirResource:
    """A hand-written, synthetic DocumentReference (NOT real Synthea/patient
    text — PHI rule: fixtures must never carry real-store note content).
    Mirrors the real Synthea shape verified in specs/U6_clinical_note.md:
    type.coding[] (LOINC), content[0].attachment.data (base64 text/plain),
    context.encounter[0].reference (nested, not top-level)."""
    data: dict = {
        "resourceType": "DocumentReference",
        "id": "docref-1",
        "status": status,
        "date": "2024-06-01T10:00:00Z",
        "content": [
            {
                "attachment": {
                    "contentType": "text/plain",
                    "data": base64.b64encode(note_text.encode("utf-8")).decode("ascii"),
                }
            }
        ],
    }
    if note_type_code is not None:
        data["type"] = {
            "coding": [
                {
                    "system": "http://loinc.org",
                    "code": note_type_code,
                    "display": "History and physical note",
                }
            ]
        }
    if encounter_ref is not None:
        data["context"] = {"encounter": [{"reference": encounter_ref}]}

    return FhirResource(
        resource_type="DocumentReference",
        fhir_id="docref-1",
        full_url="urn:uuid:docref-1",
        data=data,
    )


def test_plan_clinical_note_bead_decodes_base64_and_sets_type() -> None:
    resource = _synthetic_document_reference()
    planned = plan_clinical_note_bead(resource, patient_ref="urn:uuid:patient-1")

    assert planned.bead_type == CLINICAL_NOTE_TYPE == "clinical_note"
    assert planned.content["raw_text"] == "Chief Complaint\nSynthetic patient reports mild headache."
    assert planned.content["source_system"] == "synthea"
    assert planned.content["source_document_id"] == "docref-1"
    assert planned.content["language"] == "en"
    assert planned.content["status"] == "current"
    assert planned.content["note_type_code"] == "34117-2"


def test_plan_clinical_note_bead_content_never_contains_base64_or_coding() -> None:
    resource = _synthetic_document_reference()
    planned = plan_clinical_note_bead(resource, patient_ref="urn:uuid:patient-1")

    # The raw base64 attachment.data string must never appear verbatim in
    # content (data-reviewer finding #1: base64 pollutes FTS).
    raw_base64 = resource.data["content"][0]["attachment"]["data"]
    for value in planned.content.values():
        assert value != raw_base64

    # No key in content is a coding[] structure (data-reviewer finding #2:
    # a raw coding[] would make antigen.Extract mis-tag the document type
    # itself as a clinical finding, contradicting untagged-by-default).
    assert "coding" not in planned.content
    assert "type" not in planned.content
    # note_type_code must be a plain string, not a dict/list.
    assert isinstance(planned.content["note_type_code"], str)


def test_plan_clinical_note_bead_parent_is_nested_context_encounter() -> None:
    resource = _synthetic_document_reference(encounter_ref="urn:uuid:encounter-1")
    planned = plan_clinical_note_bead(resource, patient_ref="urn:uuid:patient-1")

    assert planned.parent_ref == "urn:uuid:encounter-1"


def test_plan_clinical_note_bead_falls_back_to_patient_root_without_encounter() -> None:
    resource = _synthetic_document_reference(encounter_ref=None)
    planned = plan_clinical_note_bead(resource, patient_ref="urn:uuid:patient-1")

    assert planned.parent_ref == "urn:uuid:patient-1"


def test_plan_resource_bead_dispatches_document_reference_to_clinical_note() -> None:
    """plan_resource_bead (the generic dispatch point patient.py calls) must
    route DocumentReference to plan_clinical_note_bead, not the generic
    f"fhir_{type.lower()}" + resource.data-spread path."""
    resource = _synthetic_document_reference()
    planned = plan_resource_bead(resource, patient_ref="urn:uuid:patient-1")

    assert planned.bead_type == "clinical_note"
    assert planned.bead_type != "fhir_documentreference"
    assert planned.content["raw_text"]


def test_plan_clinical_note_bead_missing_note_type_code_is_omitted() -> None:
    resource = _synthetic_document_reference(note_type_code=None)
    planned = plan_clinical_note_bead(resource, patient_ref="urn:uuid:patient-1")

    assert "note_type_code" not in planned.content


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
