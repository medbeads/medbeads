"""Integration test: spawn a real medbeadsd, ingest real Synthea patients,
verify via list_patients/get_timeline/get_bead (MCP round-trips, not just
"the ingest script exited 0") and cross-check the manifest against what
medbeadsd itself reports it holds.

Requires the `go` toolchain (to build medbeadsd) and the real Synthea
dataset at ~/medbeads-synthea/output/fhir (see tests/conftest.py) — skipped
automatically if either is unavailable.
"""

from __future__ import annotations

import asyncio
import json
from pathlib import Path

import pytest

from bench.ingest.fhir import clinical_resources, find_patient_entry, iter_patient_bundle_files, load_bundle
from bench.ingest.mcp_client import MedBeadsClient
from bench.ingest.patient import ingest_patient_bundle
from bench.ingest.run import run_ingest


def test_limit_2_ingest_then_list_patients_and_get_timeline(
    tmp_path: Path, medbeadsd_binary: Path, synthea_fhir_dir: Path
) -> None:
    asyncio.run(_run(tmp_path, medbeadsd_binary, synthea_fhir_dir))


async def _run(tmp_path: Path, medbeadsd_binary: Path, synthea_fhir_dir: Path) -> None:
    data_dir = tmp_path / "data"
    manifest_path = tmp_path / "manifest.jsonl"
    run_manifest_path = tmp_path / "run_manifest.json"

    summary = await run_ingest(
        fhir_dir=synthea_fhir_dir,
        data_dir=data_dir,
        medbeadsd_path=medbeadsd_binary,
        manifest_path=manifest_path,
        run_manifest_path=run_manifest_path,
        limit=2,
    )

    assert summary.total_patients == 2
    assert summary.ok_patients == 2
    assert summary.failed_patients == 0
    assert summary.failures == []

    manifest_lines = manifest_path.read_text(encoding="utf-8").splitlines()
    assert len(manifest_lines) == summary.total_beads > 0
    manifest_rows = [json.loads(line) for line in manifest_lines]
    # Every manifest row has the task's required shape (plus the
    # reviewer-mandated parent_fallback flag).
    for row in manifest_rows:
        assert set(row) == {
            "fhir_resource_id",
            "fhir_type",
            "bead_id",
            "patient_root",
            "timestamp",
            "parent_fallback",
        }
        assert row["bead_id"].startswith("sha256:")
        assert row["patient_root"].startswith("sha256:")
        assert isinstance(row["parent_fallback"], bool)

    run_manifest = json.loads(run_manifest_path.read_text(encoding="utf-8"))
    assert run_manifest["total_patients"] == 2
    assert run_manifest["ok_patients"] == 2
    assert run_manifest["limit"] == 2
    assert run_manifest["selection_order"] == "filename_ascending"
    assert run_manifest["selected_bundles"] == sorted(run_manifest["selected_bundles"])
    assert len(run_manifest["selected_bundles"]) == 2
    assert run_manifest["git_commit"]  # non-empty: either a real hash or "unknown"
    assert "parent_fallback_warnings" in run_manifest  # sampled real data: this run should also be empty
    assert run_manifest["parent_fallback_warnings"] == []

    # Now verify via a *fresh* MCP session (not the one ingest used) that
    # medbeadsd itself reports the same picture: exactly 2 patients, and
    # get_timeline for one of them returns a chronologically sorted set of
    # Beads whose count matches this patient's manifest rows.
    async with MedBeadsClient(medbeadsd_binary, data_dir) as client:
        patients = await client.list_patients()
        assert len(patients) == 2

        patient_root_ids = {row["patient_root"] for row in manifest_rows}
        assert {p["id"] for p in patients} == patient_root_ids

        for patient in patients:
            timeline = await client.get_timeline(patient["id"])
            expected_rows = [r for r in manifest_rows if r["patient_root"] == patient["id"]]
            assert len(timeline) == len(expected_rows)

            timestamps = [b["timestamp"] for b in timeline]
            assert timestamps == sorted(timestamps), "get_timeline must return chronological order"

            bead_ids_in_timeline = {b["id"] for b in timeline}
            bead_ids_in_manifest = {r["bead_id"] for r in expected_rows}
            assert bead_ids_in_timeline == bead_ids_in_manifest

        # Reviewer-mandated blind spot: assert a child Bead's parents[0] is
        # literally its Encounter's real Bead ID (not merely "some parent
        # exists", and not the Patient root by accident/fallback) — cross
        # -check by loading the exact 2 ingested Bundle files and following
        # the FHIR `encounter.reference` ourselves (keyed by fullUrl, since
        # that is the form `encounter.reference` actually uses in this
        # dataset — a bare fhir_id would never match), then confirming
        # medbeadsd's own get_bead agrees.
        # fullUrl (the "urn:uuid:..." form encounter.reference uses) ->
        # real Bead ID, built from the same 2 bundles run_ingest just
        # processed (manifest_rows alone only carries the bare fhir_id,
        # not full_url, so this map is rebuilt from the raw Bundles here).
        bead_id_by_full_url: dict[str, str] = {}
        checked_at_least_one_encounter_child = False

        for bundle_path in iter_patient_bundle_files(synthea_fhir_dir)[:2]:
            bundle = load_bundle(bundle_path)
            patient_entry = find_patient_entry(bundle)
            assert patient_entry is not None
            patient_fhir_id = patient_entry["resource"].get("id", "")
            patient_row = next(r for r in manifest_rows if r["fhir_type"] == "Patient" and r["fhir_resource_id"] == patient_fhir_id)
            if patient_entry.get("fullUrl"):
                bead_id_by_full_url[patient_entry["fullUrl"]] = patient_row["bead_id"]

            resources = clinical_resources(bundle)
            fhir_id_to_row = {
                r["fhir_resource_id"]: r
                for r in manifest_rows
                if r["patient_root"] == patient_row["bead_id"] and r["fhir_resource_id"]
            }
            for resource in resources:
                if resource.full_url and resource.fhir_id in fhir_id_to_row:
                    bead_id_by_full_url[resource.full_url] = fhir_id_to_row[resource.fhir_id]["bead_id"]

            for resource in resources:
                encounter = resource.data.get("encounter")
                encounter_ref = encounter.get("reference") if isinstance(encounter, dict) else None
                if not encounter_ref or encounter_ref not in bead_id_by_full_url:
                    continue
                child_row = fhir_id_to_row.get(resource.fhir_id)
                if child_row is None:
                    continue

                expected_parent_bead_id = bead_id_by_full_url[encounter_ref]
                child_bead = await client.get_bead(child_row["bead_id"])
                assert child_bead["parents"] == [expected_parent_bead_id], (
                    f"{resource.resource_type} {resource.fhir_id}: parents={child_bead['parents']!r}, "
                    f"want [{expected_parent_bead_id!r}] (its own Encounter's Bead ID, "
                    "not a silent Patient-root fallback)"
                )
                checked_at_least_one_encounter_child = True
                break  # one confirmed example per patient bundle is enough

        assert checked_at_least_one_encounter_child, (
            "found no Encounter-referencing child resource among the --limit 2 patients "
            "to verify parents[0] against — test fixture assumption broke"
        )


def test_medication_reference_bead_gets_rxnorm_antigen(
    tmp_path: Path, medbeadsd_binary: Path, synthea_fhir_dir: Path
) -> None:
    """Reviewer-mandated fix verification: ingest one real Bundle known to
    contain a `medicationReference`-shaped MedicationRequest (no inline
    `medicationCodeableConcept`), then confirm via search_tags that its
    server-computed tags (index.IndexBead's antigen.Extract, run at
    projection time — v3.1 moved this off create_bead entirely, see
    get_bead's doc comment) include an `rxnorm:` entry — i.e. this
    module's inline medication-code synthesis actually reaches
    antigen.Extract, not just that the synthesized content looks right in
    isolation (test_medication_reference.py already covers that in
    isolation without a real server).
    """
    asyncio.run(_run_medication_reference(tmp_path, medbeadsd_binary, synthea_fhir_dir))


async def _run_medication_reference(tmp_path: Path, medbeadsd_binary: Path, synthea_fhir_dir: Path) -> None:
    bundle_path = _find_bundle_with_medication_reference(synthea_fhir_dir)
    if bundle_path is None:
        pytest.skip("no medicationReference-shaped MedicationRequest found in the Synthea dataset sample scanned")

    data_dir = tmp_path / "data"
    data_dir.mkdir(parents=True, exist_ok=True)

    async with MedBeadsClient(medbeadsd_binary, data_dir) as client:
        result = await ingest_patient_bundle(client, bundle_path)
        assert result.ok, result.error

        bundle = load_bundle(bundle_path)
        medication_request_fhir_ids = [
            r.fhir_id
            for r in clinical_resources(bundle)
            if r.resource_type == "MedicationRequest"
            and "medicationCodeableConcept" not in r.data
            and "medicationReference" in r.data
        ]
        assert medication_request_fhir_ids, "fixture bundle no longer has a medicationReference MedicationRequest"

        bead_id_by_fhir_id = {row.fhir_resource_id: row.bead_id for row in result.manifest_rows}

        found_rxnorm = False
        for fhir_id in medication_request_fhir_ids:
            bead_id = bead_id_by_fhir_id[fhir_id]
            bead = await client.get_bead(bead_id)
            content = bead["content"]
            assert "medicationCodeableConcept" in content, (
                f"MedicationRequest {fhir_id}'s Bead content is missing the synthesized "
                "medicationCodeableConcept"
            )

            # Derive the exact rxnorm: tag antigen.Extract should have
            # produced from this Bead's own synthesized coding[], then
            # confirm the projection (bead_tags, via search_tags) actually
            # carries it for this bead_id — a precise round-trip check, not
            # merely "get_bead returned something antigen-shaped" (which is
            # no longer possible post-v3.1: get_bead's Bead never carries
            # tags at all).
            codings = content["medicationCodeableConcept"].get("coding", [])
            rxnorm_codes = [
                c["code"]
                for c in codings
                if isinstance(c, dict) and c.get("system") == "http://www.nlm.nih.gov/research/umls/rxnorm"
            ]
            if not rxnorm_codes:
                continue
            for code in rxnorm_codes:
                hits = await client.search_tags(f"rxnorm:{code}")
                if any(h["id"] == bead["id"] for h in hits):
                    found_rxnorm = True

        assert found_rxnorm, (
            "no medicationReference-shaped MedicationRequest Bead's rxnorm: tag was found via "
            "search_tags after inline medication-code synthesis"
        )


def _find_bundle_with_medication_reference(fhir_dir: Path) -> Path | None:
    for bundle_path in sorted(fhir_dir.glob("*.json")):
        if bundle_path.name.startswith(("hospitalInformation", "practitionerInformation")):
            continue
        bundle = load_bundle(bundle_path)
        for resource in clinical_resources(bundle):
            if (
                resource.resource_type == "MedicationRequest"
                and "medicationCodeableConcept" not in resource.data
                and "medicationReference" in resource.data
            ):
                return bundle_path
    return None
