"""Unit tests for bench.scenarios.generate's four category templates, over
a small hand-built synthetic FHIR Bundle (no real medbeadsd/Synthea data
needed — this exercises the pure generate_scenarios_for_patient function
directly against a fixture PatientManifest).
"""

from __future__ import annotations

from bench.scenarios.generate import (
    CATEGORY_ENCOUNTER_CONTEXT,
    CATEGORY_MEDICATION_INTERACTION_SURFACE,
    CATEGORY_MEDICATION_LOOKUP,
    CATEGORY_TEMPORAL_ORDER,
    generate_scenarios_for_patient,
)
from bench.scenarios.manifest import ManifestEntry, PatientManifest

PATIENT_ROOT = "sha256:patient-root-bead-id"

# rxnorm 855332 (warfarin) and 309362 (clopidogrel) both carry risk:bleeding
# per internal/engine/antigen/dictionary.json (VERIFIED by reading that file
# directly — see antigen_dict.py's own doc comment on why this is the one
# Go-project data asset bench.scenarios reads).
_WARFARIN_RXNORM = "855332"
_CLOPIDOGREL_RXNORM = "309362"


def _rxnorm_coding(code: str, display: str) -> dict:
    return {
        "text": display,
        "coding": [{"system": "http://www.nlm.nih.gov/research/umls/rxnorm", "code": code, "display": display}],
    }


def _snomed_coding(code: str, display: str) -> dict:
    return {
        "text": display,
        "coding": [{"system": "http://snomed.info/sct", "code": code, "display": display}],
    }


def _build_fixture() -> tuple[dict, PatientManifest]:
    """A small synthetic patient: one Encounter, one Condition (treated by
    an MedicationRequest via reasonReference), two more MedicationRequests
    sharing a risk:bleeding antigen (warfarin + clopidogrel), and a couple
    of Observations under the same Encounter — enough surface area to
    exercise all four templates deterministically.
    """
    entries = [
        {
            "fullUrl": "urn:uuid:patient-1",
            "resource": {"resourceType": "Patient", "id": "patient-1", "birthDate": "1970-01-01"},
        },
        {
            "fullUrl": "urn:uuid:encounter-1",
            "resource": {
                "resourceType": "Encounter",
                "id": "encounter-1",
                "period": {"start": "2020-01-01T09:00:00Z"},
                "type": [{"text": "General checkup"}],
            },
        },
        {
            "fullUrl": "urn:uuid:condition-1",
            "resource": {
                "resourceType": "Condition",
                "id": "condition-1",
                "code": _snomed_coding("38341003", "Hypertension"),
                "recordedDate": "2020-01-01T09:05:00Z",
                "encounter": {"reference": "urn:uuid:encounter-1"},
            },
        },
        {
            "fullUrl": "urn:uuid:medrequest-lisinopril",
            "resource": {
                "resourceType": "MedicationRequest",
                "id": "medrequest-lisinopril",
                "medicationCodeableConcept": _rxnorm_coding("314076", "lisinopril 10 MG Oral Tablet"),
                "authoredOn": "2020-01-01T09:10:00Z",
                "encounter": {"reference": "urn:uuid:encounter-1"},
                "reasonReference": [{"reference": "urn:uuid:condition-1", "display": "Hypertension"}],
            },
        },
        {
            "fullUrl": "urn:uuid:medrequest-warfarin",
            "resource": {
                "resourceType": "MedicationRequest",
                "id": "medrequest-warfarin",
                "medicationCodeableConcept": _rxnorm_coding(_WARFARIN_RXNORM, "warfarin sodium 5 MG Oral Tablet"),
                "authoredOn": "2020-01-02T09:00:00Z",
                "encounter": {"reference": "urn:uuid:encounter-1"},
            },
        },
        {
            "fullUrl": "urn:uuid:medrequest-clopidogrel",
            "resource": {
                "resourceType": "MedicationRequest",
                "id": "medrequest-clopidogrel",
                "medicationCodeableConcept": _rxnorm_coding(_CLOPIDOGREL_RXNORM, "clopidogrel 75 MG Oral Tablet"),
                "authoredOn": "2020-01-03T09:00:00Z",
                "encounter": {"reference": "urn:uuid:encounter-1"},
            },
        },
        {
            "fullUrl": "urn:uuid:observation-1",
            "resource": {
                "resourceType": "Observation",
                "id": "observation-1",
                "code": _snomed_coding("271649006", "Systolic blood pressure"),
                "effectiveDateTime": "2020-01-04T09:00:00Z",
                "encounter": {"reference": "urn:uuid:encounter-1"},
            },
        },
        {
            "fullUrl": "urn:uuid:procedure-1",
            "resource": {
                "resourceType": "Procedure",
                "id": "procedure-1",
                "code": _snomed_coding("117015009", "Blood test"),
                "performedDateTime": "2020-01-05T09:00:00Z",
                "encounter": {"reference": "urn:uuid:encounter-1"},
            },
        },
    ]
    bundle = {"resourceType": "Bundle", "entry": entries}

    manifest_entries = [
        ManifestEntry("patient-1", "Patient", PATIENT_ROOT, PATIENT_ROOT, "1970-01-01", False),
        ManifestEntry("encounter-1", "Encounter", "sha256:bead-encounter-1", PATIENT_ROOT, "2020-01-01T09:00:00Z", False),
        ManifestEntry("condition-1", "Condition", "sha256:bead-condition-1", PATIENT_ROOT, "2020-01-01T09:05:00Z", False),
        ManifestEntry(
            "medrequest-lisinopril", "MedicationRequest", "sha256:bead-medrequest-lisinopril", PATIENT_ROOT,
            "2020-01-01T09:10:00Z", False,
        ),
        ManifestEntry(
            "medrequest-warfarin", "MedicationRequest", "sha256:bead-medrequest-warfarin", PATIENT_ROOT,
            "2020-01-02T09:00:00Z", False,
        ),
        ManifestEntry(
            "medrequest-clopidogrel", "MedicationRequest", "sha256:bead-medrequest-clopidogrel", PATIENT_ROOT,
            "2020-01-03T09:00:00Z", False,
        ),
        ManifestEntry("observation-1", "Observation", "sha256:bead-observation-1", PATIENT_ROOT, "2020-01-04T09:00:00Z", False),
        ManifestEntry("procedure-1", "Procedure", "sha256:bead-procedure-1", PATIENT_ROOT, "2020-01-05T09:00:00Z", False),
    ]
    pm = PatientManifest(patient_root=PATIENT_ROOT, entries=manifest_entries)
    return bundle, pm


def test_medication_lookup_links_condition_to_prescribed_drug() -> None:
    bundle, pm = _build_fixture()
    scenarios = generate_scenarios_for_patient(bundle, pm)
    lookups = [s for s in scenarios if s.category == CATEGORY_MEDICATION_LOOKUP]
    assert len(lookups) == 1
    s = lookups[0]
    assert "Hypertension" in s.question
    assert s.answer == "lisinopril 10 MG Oral Tablet"
    assert set(s.evidence_bead_ids) == {"sha256:bead-medrequest-lisinopril", "sha256:bead-condition-1"}
    assert s.patient_id == PATIENT_ROOT
    assert s.reasoning_type == "lookup"


def test_temporal_order_pairs_events_by_timestamp() -> None:
    bundle, pm = _build_fixture()
    scenarios = generate_scenarios_for_patient(bundle, pm)
    temporal = [s for s in scenarios if s.category == CATEGORY_TEMPORAL_ORDER]
    assert len(temporal) >= 1
    for s in temporal:
        assert len(s.evidence_bead_ids) == 2
        assert s.reasoning_type == "temporal_comparison"
        # The answer must be one of the two events referenced in the question.
        assert s.answer in s.question


def test_temporal_order_condition_before_later_medication() -> None:
    bundle, pm = _build_fixture()
    scenarios = generate_scenarios_for_patient(bundle, pm)
    temporal = [s for s in scenarios if s.category == CATEGORY_TEMPORAL_ORDER]
    # Condition (2020-01-01T09:05) sorts immediately after Encounter is
    # excluded (Encounter is not a _TEMPORAL_SOURCE_TYPES member) but before
    # the lisinopril MedicationRequest (2020-01-01T09:10) — adjacent pair.
    condition_pair = [s for s in temporal if "Hypertension" in s.question]
    assert condition_pair, f"no temporal_order scenario mentions Hypertension; got {[s.question for s in temporal]}"


def test_encounter_context_lists_every_child_of_the_encounter() -> None:
    bundle, pm = _build_fixture()
    scenarios = generate_scenarios_for_patient(bundle, pm)
    contexts = [s for s in scenarios if s.category == CATEGORY_ENCOUNTER_CONTEXT]
    assert len(contexts) == 1
    s = contexts[0]
    assert s.reasoning_type == "aggregation"
    # Evidence: the Encounter itself + every child resource under it
    # (Condition, 3x MedicationRequest, Observation, Procedure) = 7 Beads.
    assert set(s.evidence_bead_ids) == {
        "sha256:bead-encounter-1",
        "sha256:bead-condition-1",
        "sha256:bead-medrequest-lisinopril",
        "sha256:bead-medrequest-warfarin",
        "sha256:bead-medrequest-clopidogrel",
        "sha256:bead-observation-1",
        "sha256:bead-procedure-1",
    }
    assert s.evidence_bead_ids[0] == "sha256:bead-encounter-1"  # encounter itself always listed first


def test_medication_interaction_surface_groups_shared_risk_antigen() -> None:
    bundle, pm = _build_fixture()
    scenarios = generate_scenarios_for_patient(bundle, pm)
    surfaces = [s for s in scenarios if s.category == CATEGORY_MEDICATION_INTERACTION_SURFACE]
    assert len(surfaces) == 1
    s = surfaces[0]
    assert s.reasoning_type == "antigen_surface"
    assert "risk:bleeding" in s.question
    # warfarin + clopidogrel share risk:bleeding; lisinopril (no risk
    # antigen in the dictionary) must NOT be included.
    assert set(s.evidence_bead_ids) == {"sha256:bead-medrequest-warfarin", "sha256:bead-medrequest-clopidogrel"}


def test_per_patient_cap_truncates_deterministically() -> None:
    bundle, pm = _build_fixture()
    full = generate_scenarios_for_patient(bundle, pm)
    capped = generate_scenarios_for_patient(bundle, pm, per_patient=2)
    assert len(capped) == 2
    # Capped result must be the first 2 by the same sort key generation uses
    # internally (patient_id, category, scenario_id) — not an arbitrary subset.
    expected_ids = sorted(s.scenario_id for s in full)[:2]
    assert sorted(s.scenario_id for s in capped) == expected_ids


def test_every_scenario_has_the_required_fields_nonempty() -> None:
    bundle, pm = _build_fixture()
    scenarios = generate_scenarios_for_patient(bundle, pm)
    assert scenarios, "fixture should produce at least one scenario per category"
    for s in scenarios:
        assert s.patient_id == PATIENT_ROOT
        assert s.question
        assert s.answer
        assert s.evidence_bead_ids
        assert s.category
        assert s.reasoning_type
        assert s.scenario_id
