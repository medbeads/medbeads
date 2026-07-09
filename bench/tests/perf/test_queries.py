"""Unit tests for bench.perf.queries — deterministic query-text extraction.

Uses small synthetic FHIR Bundle fixtures (not the real Synthea dataset,
which may be absent in CI — see bench/tests/conftest.py's synthea_fhir_dir
skip-if-absent fixture for that concern) so these tests are always runnable.
"""

from __future__ import annotations

import json
from pathlib import Path

from bench.perf.queries import display_names_for_bundle, sample_patient_queries


def _write_bundle(path: Path, entries: list[dict]) -> None:
    path.write_text(json.dumps({"resourceType": "Bundle", "entry": entries}), encoding="utf-8")


def test_display_names_for_bundle_extracts_medication_and_condition_text(tmp_path: Path):
    bundle_path = tmp_path / "patient1.json"
    _write_bundle(
        bundle_path,
        [
            {
                "resource": {
                    "resourceType": "MedicationRequest",
                    "medicationCodeableConcept": {
                        "text": "Acetaminophen 325 MG Oral Tablet",
                        "coding": [{"system": "rxnorm", "code": "123", "display": "Acetaminophen"}],
                    },
                }
            },
            {
                "resource": {
                    "resourceType": "Condition",
                    "code": {
                        "text": "Essential hypertension",
                        "coding": [{"system": "snomed", "code": "456", "display": "Hypertension"}],
                    },
                }
            },
        ],
    )

    names = display_names_for_bundle(bundle_path)
    assert names == [
        "Acetaminophen 325 MG Oral Tablet",
        "Acetaminophen",
        "Essential hypertension",
        "Hypertension",
    ]


def test_display_names_for_bundle_deduplicates_and_skips_excluded_types(tmp_path: Path):
    bundle_path = tmp_path / "patient2.json"
    _write_bundle(
        bundle_path,
        [
            {
                "resource": {
                    "resourceType": "Observation",
                    "code": {"text": "Hemoglobin"},
                }
            },
            {
                # Duplicate display text from a second resource: must appear
                # only once, at its first occurrence position.
                "resource": {
                    "resourceType": "Observation",
                    "code": {"text": "Hemoglobin"},
                }
            },
            {
                # Excluded resource type (not Medication/Condition/
                # Observation): must not contribute any name.
                "resource": {
                    "resourceType": "Encounter",
                    "type": [{"text": "should not appear"}],
                }
            },
        ],
    )

    names = display_names_for_bundle(bundle_path)
    assert names == ["Hemoglobin"]


def test_display_names_for_bundle_prioritizes_medication_and_condition_over_observation(tmp_path: Path):
    bundle_path = tmp_path / "patient3.json"
    _write_bundle(
        bundle_path,
        [
            # Observation appears first in the Bundle, but must still be
            # collected *after* the later MedicationRequest/Condition
            # entries in display_names_for_bundle's output (priority pass
            # before fallback pass — see _PRIORITY_SOURCE_TYPES).
            {"resource": {"resourceType": "Observation", "code": {"text": "Received"}}},
            {"resource": {"resourceType": "Condition", "code": {"text": "Prediabetes"}}},
            {
                "resource": {
                    "resourceType": "MedicationRequest",
                    "medicationCodeableConcept": {"text": "Metformin"},
                }
            },
        ],
    )

    names = display_names_for_bundle(bundle_path)
    assert names == ["Prediabetes", "Metformin", "Received"]


def test_display_names_for_bundle_malformed_file_returns_empty_not_raises(tmp_path: Path):
    bad_path = tmp_path / "broken.json"
    bad_path.write_text("{not valid json", encoding="utf-8")
    assert display_names_for_bundle(bad_path) == []


def test_display_names_for_bundle_missing_file_returns_empty_not_raises(tmp_path: Path):
    assert display_names_for_bundle(tmp_path / "does_not_exist.json") == []


def test_sample_patient_queries_is_deterministic_across_repeated_calls(tmp_path: Path):
    bundle_path = tmp_path / "patient1.json"
    _write_bundle(
        bundle_path,
        [
            {"resource": {"resourceType": "MedicationRequest", "medicationCodeableConcept": {"text": "Metformin 500 MG"}}},
            {"resource": {"resourceType": "Condition", "code": {"text": "Type 2 diabetes mellitus"}}},
            {"resource": {"resourceType": "Observation", "code": {"text": "Glucose"}}},
        ],
    )

    first = sample_patient_queries([bundle_path], queries_per_patient=2)
    second = sample_patient_queries([bundle_path], queries_per_patient=2)
    assert first == second
    # "Metformin 500 MG" -> longest word "Metformin"; "Type 2 diabetes
    # mellitus" -> "diabetes" and "mellitus" tie at 8 letters, first
    # occurrence ("diabetes") wins (see _longest_word's tie-break doc
    # comment).
    assert [pq.query for pq in first] == ["metformin", "diabetes"]


def test_sample_patient_queries_respects_queries_per_patient_cap(tmp_path: Path):
    bundle_path = tmp_path / "patient1.json"
    _write_bundle(
        bundle_path,
        [
            {"resource": {"resourceType": "Condition", "code": {"text": "Zeta prolapse"}}},
            {"resource": {"resourceType": "Condition", "code": {"text": "Beta condition"}}},
            {"resource": {"resourceType": "Condition", "code": {"text": "Gamma condition"}}},
        ],
    )

    out = sample_patient_queries([bundle_path], queries_per_patient=1)
    assert len(out) == 1
    # "Zeta prolapse" -> longest word "prolapse" (8 letters, beats "Zeta"'s 4).
    assert out[0].query == "prolapse"


def test_sample_patient_queries_skips_patients_with_no_usable_names(tmp_path: Path):
    empty_bundle = tmp_path / "empty.json"
    _write_bundle(empty_bundle, [])

    good_bundle = tmp_path / "good.json"
    _write_bundle(good_bundle, [{"resource": {"resourceType": "Condition", "code": {"text": "Something"}}}])

    out = sample_patient_queries([empty_bundle, good_bundle], queries_per_patient=2)
    assert len(out) == 1
    assert out[0].bundle_path == good_bundle
    assert out[0].query == "something"
