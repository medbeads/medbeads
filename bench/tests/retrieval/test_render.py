"""Unit tests for bench.retrieval.render.render_l0 — mirrors
internal/engine/graph/context.go's renderL0/collectContentStrings."""

from __future__ import annotations

from bench.retrieval.render import render_l0


def test_no_string_values_falls_back_to_bare_type() -> None:
    assert render_l0("fhir_observation", {}) == "fhir_observation"
    assert render_l0("fhir_observation", {"n": 42, "flag": True}) == "fhir_observation"


def test_single_string_value() -> None:
    assert render_l0("fhir_observation", {"note": "hello"}) == "fhir_observation: hello"


def test_multiple_string_values_sorted_deterministically() -> None:
    content = {"z_field": "zebra", "a_field": "apple", "m_field": "mango"}
    # Sorted by *value*, not by key (matches Go's collectContentStrings,
    # which discards keys entirely and sorts the flat list of string values).
    assert render_l0("fhir_observation", content) == "fhir_observation: apple mango zebra"


def test_nested_dicts_and_lists_are_walked_recursively() -> None:
    content = {
        "coding": [
            {"system": "http://loinc.org", "code": "1234", "display": "banana test"},
            {"system": "http://snomed.info", "code": "5678", "display": "apple test"},
        ],
        "note": "top level note",
    }
    got = render_l0("fhir_observation", content)
    # Every string value reachable anywhere in content, sorted.
    expected_parts = sorted(
        [
            "http://loinc.org",
            "1234",
            "banana test",
            "http://snomed.info",
            "5678",
            "apple test",
            "top level note",
        ]
    )
    assert got == "fhir_observation: " + " ".join(expected_parts)


def test_empty_strings_are_excluded() -> None:
    assert render_l0("fhir_observation", {"note": ""}) == "fhir_observation"


def test_deterministic_across_repeated_calls() -> None:
    content = {"c": "3", "a": "1", "b": "2"}
    first = render_l0("fhir_observation", content)
    second = render_l0("fhir_observation", content)
    assert first == second
