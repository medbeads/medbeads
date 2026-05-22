"""Tests for the pure helper functions in ai.py (no Gemini calls)."""

import ai


def test_get_fhir_date_encounter():
    bead = {
        "type": "fhir_encounter",
        "timestamp": "fallback-ts",
        "content": {"period": {"start": "2026-03-01"}},
    }
    assert ai.get_fhir_date(bead) == "2026-03-01"


def test_get_fhir_date_medicationrequest():
    bead = {
        "type": "fhir_medicationrequest",
        "timestamp": "fallback-ts",
        "content": {"authoredOn": "2026-02-10"},
    }
    assert ai.get_fhir_date(bead) == "2026-02-10"


def test_get_fhir_date_condition_prefers_recorded_date():
    bead = {
        "type": "fhir_condition",
        "timestamp": "fallback-ts",
        "content": {"recordedDate": "2026-01-05", "onsetDateTime": "2025-12-01"},
    }
    assert ai.get_fhir_date(bead) == "2026-01-05"


def test_get_fhir_date_falls_back_to_timestamp():
    # An unknown type, or missing FHIR date field, falls back to the bead timestamp.
    assert ai.get_fhir_date({"type": "unknown", "timestamp": "ts-1", "content": {}}) == "ts-1"
    assert ai.get_fhir_date({"type": "fhir_encounter", "timestamp": "ts-2", "content": {}}) == "ts-2"


def test_format_context_sorts_chronologically():
    beads = [
        {"type": "fhir_observation", "content": {"effectiveDateTime": "2026-03-01"}},
        {"type": "fhir_observation", "content": {"effectiveDateTime": "2026-01-01"}},
    ]
    out = ai.format_context(beads)
    # The earlier date must appear before the later one.
    assert out.index("2026-01-01") < out.index("2026-03-01")


def test_format_context_empty():
    assert ai.format_context([]) == ""
