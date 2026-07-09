"""Unit tests for the reviewer-mandated medicationReference -> inline
medicationCodeableConcept synthesis (bench.ingest.fhir.index_medications_by_ref
/ resolve_medication_code, bench.ingest.beads._content_with_resolved_medication
via plan_resource_bead).

Uses a small synthetic Bundle (not the real Synthea corpus) so this test is
fast, self-contained, and pins down the exact contract independent of
Synthea's actual output shape drifting over time; test_integration.py's
rxnorm-antigen assertion is the real-data end-to-end check.
"""

from __future__ import annotations

from bench.ingest.beads import plan_resource_bead
from bench.ingest.fhir import FhirResource, index_medications_by_ref, resolve_medication_code

MEDICATION_REQUEST_REF = {
    "resourceType": "MedicationRequest",
    "id": "mr-1",
    "status": "completed",
    "intent": "order",
    "medicationReference": {"reference": "urn:uuid:med-1"},
    "subject": {"reference": "urn:uuid:patient-1"},
    "encounter": {"reference": "urn:uuid:enc-1"},
    "authoredOn": "2020-01-01T00:00:00+00:00",
}

MEDICATION_REQUEST_INLINE = {
    "resourceType": "MedicationRequest",
    "id": "mr-2",
    "status": "completed",
    "intent": "order",
    "medicationCodeableConcept": {
        "coding": [{"system": "http://www.nlm.nih.gov/research/umls/rxnorm", "code": "999999", "display": "already inline"}],
        "text": "already inline",
    },
    "subject": {"reference": "urn:uuid:patient-1"},
    "encounter": {"reference": "urn:uuid:enc-1"},
    "authoredOn": "2020-01-02T00:00:00+00:00",
}

MEDICATION_RESOURCE = {
    "resourceType": "Medication",
    "id": "med-1",
    "code": {
        "coding": [
            {
                "system": "http://www.nlm.nih.gov/research/umls/rxnorm",
                "code": "1535362",
                "display": "sodium fluoride 0.0272 MG/MG Oral Gel",
            }
        ],
        "text": "sodium fluoride 0.0272 MG/MG Oral Gel",
    },
    "status": "active",
}


def _bundle_with(*resources_and_urls: tuple[dict, str]) -> dict:
    return {
        "resourceType": "Bundle",
        "entry": [{"fullUrl": url, "resource": res} for res, url in resources_and_urls],
    }


def test_index_medications_by_ref_keys_by_full_url_and_id() -> None:
    bundle = _bundle_with((MEDICATION_RESOURCE, "urn:uuid:med-1"))
    index = index_medications_by_ref(bundle)
    assert index["urn:uuid:med-1"] == MEDICATION_RESOURCE
    assert index["med-1"] == MEDICATION_RESOURCE


def test_index_medications_by_ref_ignores_non_medication_resources() -> None:
    bundle = _bundle_with((MEDICATION_REQUEST_REF, "urn:uuid:mr-1"), (MEDICATION_RESOURCE, "urn:uuid:med-1"))
    index = index_medications_by_ref(bundle)
    assert list(index.values()).count(MEDICATION_REQUEST_REF) == 0
    assert len(index) == 2  # med-1 keyed both ways, nothing else


def test_resolve_medication_code_resolves_full_url_reference() -> None:
    index = index_medications_by_ref(_bundle_with((MEDICATION_RESOURCE, "urn:uuid:med-1")))
    code = resolve_medication_code(MEDICATION_REQUEST_REF, index)
    assert code == MEDICATION_RESOURCE["code"]


def test_resolve_medication_code_returns_none_without_medication_reference() -> None:
    index = index_medications_by_ref(_bundle_with((MEDICATION_RESOURCE, "urn:uuid:med-1")))
    assert resolve_medication_code(MEDICATION_REQUEST_INLINE, index) is None


def test_resolve_medication_code_returns_none_when_unresolved() -> None:
    # Reference points at a Medication that isn't in this Bundle at all.
    empty_index: dict = {}
    assert resolve_medication_code(MEDICATION_REQUEST_REF, empty_index) is None


def test_plan_resource_bead_synthesizes_medication_codeable_concept() -> None:
    bundle = _bundle_with(
        (MEDICATION_REQUEST_REF, "urn:uuid:mr-1"),
        (MEDICATION_RESOURCE, "urn:uuid:med-1"),
    )
    medications_by_ref = index_medications_by_ref(bundle)

    resource = FhirResource(
        resource_type="MedicationRequest",
        fhir_id="mr-1",
        full_url="urn:uuid:mr-1",
        data=MEDICATION_REQUEST_REF,
    )
    planned = plan_resource_bead(resource, "urn:uuid:patient-1", medications_by_ref)

    assert planned.content["medicationCodeableConcept"] == MEDICATION_RESOURCE["code"]
    # Original medicationReference is preserved (provenance).
    assert planned.content["medicationReference"] == {"reference": "urn:uuid:med-1"}


def test_plan_resource_bead_does_not_overwrite_existing_medication_codeable_concept() -> None:
    bundle = _bundle_with(
        (MEDICATION_REQUEST_INLINE, "urn:uuid:mr-2"),
        (MEDICATION_RESOURCE, "urn:uuid:med-1"),
    )
    medications_by_ref = index_medications_by_ref(bundle)

    resource = FhirResource(
        resource_type="MedicationRequest",
        fhir_id="mr-2",
        full_url="urn:uuid:mr-2",
        data=MEDICATION_REQUEST_INLINE,
    )
    planned = plan_resource_bead(resource, "urn:uuid:patient-1", medications_by_ref)

    # Must remain exactly the resource's own inline value, untouched.
    assert planned.content["medicationCodeableConcept"] == MEDICATION_REQUEST_INLINE["medicationCodeableConcept"]


def test_plan_resource_bead_without_medications_by_ref_leaves_content_unchanged() -> None:
    # medications_by_ref defaults to None (e.g. a caller with no Bundle-wide
    # index) — must not crash, must not invent data.
    resource = FhirResource(
        resource_type="MedicationRequest",
        fhir_id="mr-1",
        full_url="urn:uuid:mr-1",
        data=MEDICATION_REQUEST_REF,
    )
    planned = plan_resource_bead(resource, "urn:uuid:patient-1")

    assert "medicationCodeableConcept" not in planned.content
    assert planned.content is MEDICATION_REQUEST_REF  # unchanged, same object


def test_plan_resource_bead_non_medication_request_type_untouched() -> None:
    # medications_by_ref must never be consulted for other resource types.
    condition_data = {
        "resourceType": "Condition",
        "id": "cond-1",
        "encounter": {"reference": "urn:uuid:enc-1"},
        "recordedDate": "2020-01-01T00:00:00+00:00",
    }
    resource = FhirResource(resource_type="Condition", fhir_id="cond-1", full_url="urn:uuid:cond-1", data=condition_data)
    planned = plan_resource_bead(resource, "urn:uuid:patient-1", {"anything": MEDICATION_RESOURCE})
    assert planned.content is condition_data
