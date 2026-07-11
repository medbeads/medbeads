"""Unit tests for the reviewer-mandated two-pass ingest order
(bench.ingest.patient.ingest_patient_bundle: all Encounters first, then
everything else) and the "サイレント禁止" parent-reference-fallback flag,
using a fake MedBeadsClient (no real medbeadsd process) so these are fast,
deterministic unit tests independent of the real Synthea corpus.
test_integration.py's real-medbeadsd test covers the end-to-end
parents[0]-is-the-Encounter-Bead-ID assertion the reviewer specifically
asked for, using real data.

Also covers U6's DocumentReference -> clinical_note end-to-end ingest path
(specs/U6_clinical_note.md): status=="current" -> ingested as clinical_note
with decoded raw_text and an Encounter parent; status=="superseded" ->
dropped entirely, counted in dropped_superseded_document_references. All
fixtures here are hand-written synthetic text, never real-store note
content (PHI rule).
"""

from __future__ import annotations

import asyncio
import base64
import hashlib
import json
from pathlib import Path
from typing import Any

from bench.ingest.patient import ingest_patient_bundle


class FakeMedBeadsClient:
    """A minimal stand-in for bench.ingest.mcp_client.MedBeadsClient that
    assigns deterministic content-hash-like IDs (sha256 of the create_bead
    call's JSON payload) without spawning any process, and records the
    exact sequence + arguments of every create_bead call so tests can
    assert on call order and parents.
    """

    def __init__(self) -> None:
        self.calls: list[dict[str, Any]] = []

    async def create_bead(
        self,
        *,
        bead_type: str,
        timestamp: str,
        author: str,
        parents: list[str],
        content: dict[str, Any],
    ) -> str:
        payload = json.dumps(
            {"type": bead_type, "timestamp": timestamp, "author": author, "parents": sorted(parents), "content": content},
            sort_keys=True,
            default=str,
        )
        bead_id = "sha256:" + hashlib.sha256(payload.encode("utf-8")).hexdigest()
        self.calls.append(
            {
                "bead_type": bead_type,
                "timestamp": timestamp,
                "parents": list(parents),
                "content": content,
                "bead_id": bead_id,
            }
        )
        return bead_id


def _write_bundle(tmp_path: Path, bundle: dict) -> Path:
    path = tmp_path / "patient.json"
    path.write_text(json.dumps(bundle), encoding="utf-8")
    return path


def _patient_entry() -> dict:
    return {
        "fullUrl": "urn:uuid:patient-1",
        "resource": {
            "resourceType": "Patient",
            "id": "patient-1",
            "birthDate": "1990-01-01",
            "name": [{"given": ["Test"], "family": "Patient"}],
        },
    }


def test_all_encounters_ingested_before_any_non_encounter(tmp_path: Path) -> None:
    # Two Encounters and one Observation per Encounter, all stamped with
    # the *same* instant as their own Encounter (the reviewer's failure
    # mode: naive (timestamp, fhir_id) sort can interleave an Observation
    # before its own Encounter when fhir_id happens to sort first).
    same_instant = "2020-06-01T00:00:00+00:00"
    bundle = {
        "resourceType": "Bundle",
        "entry": [
            _patient_entry(),
            {
                "fullUrl": "urn:uuid:enc-zzz",  # sorts AFTER its own Observation's id below
                "resource": {
                    "resourceType": "Encounter",
                    "id": "enc-zzz",
                    "period": {"start": same_instant},
                },
            },
            {
                "fullUrl": "urn:uuid:obs-aaa",  # sorts BEFORE "enc-zzz" lexicographically
                "resource": {
                    "resourceType": "Observation",
                    "id": "obs-aaa",
                    "status": "final",
                    "code": {"text": "test"},
                    "effectiveDateTime": same_instant,
                    "encounter": {"reference": "urn:uuid:enc-zzz"},
                },
            },
        ],
    }
    bundle_path = _write_bundle(tmp_path, bundle)
    client = FakeMedBeadsClient()

    result = asyncio.run(ingest_patient_bundle(client, bundle_path))

    assert result.ok, result.error
    bead_types_in_order = [c["bead_type"] for c in client.calls]
    assert bead_types_in_order == ["patient_registration", "fhir_encounter", "fhir_observation"], (
        "Encounter must be ingested before the Observation that references it, "
        f"even though 'obs-aaa' < 'enc-zzz' lexicographically; got order {bead_types_in_order}"
    )

    # And the Observation's parent must be the Encounter's real Bead ID
    # (the reviewer's specific "child Bead's parents[0] == Encounter Bead
    # ID" assertion), not a silent fallback to the Patient root.
    encounter_bead_id = client.calls[1]["bead_id"]
    observation_call = client.calls[2]
    assert observation_call["parents"] == [encounter_bead_id]

    # And the manifest row for this Observation must NOT be flagged as a
    # fallback (its Encounter resolved correctly).
    obs_row = next(r for r in result.manifest_rows if r.fhir_type == "Observation")
    assert obs_row.parent_fallback is False


def test_unresolved_encounter_reference_is_flagged_not_silent(tmp_path: Path) -> None:
    # An Observation references an Encounter FHIR id that does not exist
    # anywhere in this Bundle at all (a malformed/partial export) — must
    # fall back to the Patient root AND be flagged (parent_fallback=True +
    # a recorded warning), never silently.
    bundle = {
        "resourceType": "Bundle",
        "entry": [
            _patient_entry(),
            {
                "fullUrl": "urn:uuid:obs-orphan",
                "resource": {
                    "resourceType": "Observation",
                    "id": "obs-orphan",
                    "status": "final",
                    "code": {"text": "test"},
                    "effectiveDateTime": "2020-06-01T00:00:00+00:00",
                    "encounter": {"reference": "urn:uuid:does-not-exist"},
                },
            },
        ],
    }
    bundle_path = _write_bundle(tmp_path, bundle)
    client = FakeMedBeadsClient()

    result = asyncio.run(ingest_patient_bundle(client, bundle_path))

    assert result.ok, result.error
    obs_row = next(r for r in result.manifest_rows if r.fhir_type == "Observation")
    assert obs_row.parent_fallback is True
    assert len(result.warnings) == 1
    assert "does-not-exist" in result.warnings[0]

    # Fallback still attaches to the patient root (never drops the Bead).
    patient_bead_id = client.calls[0]["bead_id"]
    observation_call = client.calls[1]
    assert observation_call["parents"] == [patient_bead_id]


def test_allergy_intolerance_patient_root_parent_is_not_flagged_as_fallback(tmp_path: Path) -> None:
    # AllergyIntolerance has no `encounter` field at all in real Synthea
    # data (VERIFIED, see bench.ingest.beads' edge-rule doc comment) — this
    # is the *designed* Patient-root edge, not an unresolved reference, so
    # it must NOT be flagged.
    bundle = {
        "resourceType": "Bundle",
        "entry": [
            _patient_entry(),
            {
                "fullUrl": "urn:uuid:allergy-1",
                "resource": {
                    "resourceType": "AllergyIntolerance",
                    "id": "allergy-1",
                    "patient": {"reference": "urn:uuid:patient-1"},
                    "recordedDate": "2020-06-01T00:00:00+00:00",
                },
            },
        ],
    }
    bundle_path = _write_bundle(tmp_path, bundle)
    client = FakeMedBeadsClient()

    result = asyncio.run(ingest_patient_bundle(client, bundle_path))

    assert result.ok, result.error
    allergy_row = next(r for r in result.manifest_rows if r.fhir_type == "AllergyIntolerance")
    assert allergy_row.parent_fallback is False
    assert result.warnings == []


def _b64(text: str) -> str:
    return base64.b64encode(text.encode("utf-8")).decode("ascii")


def test_current_document_reference_ingested_as_clinical_note_with_encounter_parent(tmp_path: Path) -> None:
    """U6: a status=="current" DocumentReference must be ingested as a
    clinical_note Bead with base64-decoded raw_text and no base64/coding[]
    in content, parented to its nested context.encounter[0]."""
    note_text = "Assessment\nSynthetic note: patient stable, no acute findings."
    bundle = {
        "resourceType": "Bundle",
        "entry": [
            _patient_entry(),
            {
                "fullUrl": "urn:uuid:enc-1",
                "resource": {
                    "resourceType": "Encounter",
                    "id": "enc-1",
                    "period": {"start": "2024-06-01T09:00:00+00:00"},
                },
            },
            {
                "fullUrl": "urn:uuid:doc-current",
                "resource": {
                    "resourceType": "DocumentReference",
                    "id": "doc-current",
                    "status": "current",
                    "date": "2024-06-01T10:00:00+00:00",
                    "type": {
                        "coding": [
                            {"system": "http://loinc.org", "code": "34117-2", "display": "History and physical note"}
                        ]
                    },
                    "context": {"encounter": [{"reference": "urn:uuid:enc-1"}]},
                    "content": [{"attachment": {"contentType": "text/plain", "data": _b64(note_text)}}],
                },
            },
        ],
    }
    bundle_path = _write_bundle(tmp_path, bundle)
    client = FakeMedBeadsClient()

    result = asyncio.run(ingest_patient_bundle(client, bundle_path))

    assert result.ok, result.error
    assert result.dropped_superseded_document_references == 0

    note_calls = [c for c in client.calls if c["bead_type"] == "clinical_note"]
    assert len(note_calls) == 1
    note_call = note_calls[0]

    assert note_call["content"]["raw_text"] == note_text
    assert note_call["content"]["status"] == "current"
    assert note_call["content"]["source_system"] == "synthea"
    assert note_call["content"]["note_type_code"] == "34117-2"

    # Never carries the raw base64 or a coding[] structure.
    raw_b64 = _b64(note_text)
    for value in note_call["content"].values():
        assert value != raw_b64
    assert "coding" not in note_call["content"]
    assert "type" not in note_call["content"]

    # Parent is the Encounter Bead, not the Patient root.
    encounter_bead_id = next(c["bead_id"] for c in client.calls if c["bead_type"] == "fhir_encounter")
    assert note_call["parents"] == [encounter_bead_id]

    note_row = next(r for r in result.manifest_rows if r.fhir_type == "DocumentReference")
    assert note_row.parent_fallback is False


def test_superseded_document_reference_is_dropped_entirely(tmp_path: Path) -> None:
    """U6 user ruling (docs/decisions.md 2026-07-11 U6 entry): a
    status=="superseded" DocumentReference must never become a Bead at all,
    and must be counted in dropped_superseded_document_references."""
    bundle = {
        "resourceType": "Bundle",
        "entry": [
            _patient_entry(),
            {
                "fullUrl": "urn:uuid:doc-old",
                "resource": {
                    "resourceType": "DocumentReference",
                    "id": "doc-old",
                    "status": "superseded",
                    "date": "2023-01-01T00:00:00+00:00",
                    "content": [{"attachment": {"contentType": "text/plain", "data": _b64("stale synthetic note")}}],
                },
            },
        ],
    }
    bundle_path = _write_bundle(tmp_path, bundle)
    client = FakeMedBeadsClient()

    result = asyncio.run(ingest_patient_bundle(client, bundle_path))

    assert result.ok, result.error
    assert result.dropped_superseded_document_references == 1
    assert not any(c["bead_type"] == "clinical_note" for c in client.calls)
    assert not any(r.fhir_type == "DocumentReference" for r in result.manifest_rows)
