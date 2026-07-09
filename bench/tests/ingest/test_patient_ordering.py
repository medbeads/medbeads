"""Unit tests for the reviewer-mandated two-pass ingest order
(bench.ingest.patient.ingest_patient_bundle: all Encounters first, then
everything else) and the "サイレント禁止" parent-reference-fallback flag,
using a fake MedBeadsClient (no real medbeadsd process) so these are fast,
deterministic unit tests independent of the real Synthea corpus.
test_integration.py's real-medbeadsd test covers the end-to-end
parents[0]-is-the-Encounter-Bead-ID assertion the reviewer specifically
asked for, using real data.
"""

from __future__ import annotations

import asyncio
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
