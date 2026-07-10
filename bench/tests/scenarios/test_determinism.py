"""Integration test: real ingest (scratch data, --limit N) -> scenario
generation run twice -> byte-identical YAML output (the lead's "同一入力→
同一 YAML(決定論テスト)" requirement) and every evidence_bead_id resolves to
a real manifest Bead ID.

Requires the `go` toolchain and the real Synthea dataset (see
tests/conftest.py) — skipped automatically if either is unavailable.
"""

from __future__ import annotations

import asyncio
import json
from pathlib import Path

from bench.ingest.run import run_ingest
from bench.scenarios.generate import generate_scenarios
from bench.scenarios.model import write_scenarios_yaml


def test_scenario_generation_is_deterministic_across_runs(
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
        limit=5,
    )
    assert summary.ok_patients == 5

    scenarios_a = generate_scenarios(fhir_dir=synthea_fhir_dir, manifest_path=manifest_path)
    scenarios_b = generate_scenarios(fhir_dir=synthea_fhir_dir, manifest_path=manifest_path)

    assert len(scenarios_a) > 0, "real 5-patient scratch ingest should produce at least one scenario"
    assert [s.to_json_dict() for s in scenarios_a] == [s.to_json_dict() for s in scenarios_b]

    out_a = tmp_path / "scenarios_a.yaml"
    out_b = tmp_path / "scenarios_b.yaml"
    write_scenarios_yaml(scenarios_a, out_a)
    write_scenarios_yaml(scenarios_b, out_b)
    assert out_a.read_bytes() == out_b.read_bytes(), "identical input must produce byte-identical YAML"

    # Every evidence_bead_id must resolve to a real Bead ID in this same
    # ingest run's manifest (not just "some sha256:-shaped string") —
    # cross-check against manifest.jsonl directly, independent of
    # bench.scenarios.manifest's own loader.
    manifest_bead_ids: set[str] = set()
    with manifest_path.open("r", encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            manifest_bead_ids.add(json.loads(line)["bead_id"])

    for scenario in scenarios_a:
        for bead_id in scenario.evidence_bead_ids:
            assert bead_id in manifest_bead_ids, (
                f"scenario {scenario.scenario_id} evidence_bead_id {bead_id!r} not found in "
                "this run's manifest.jsonl"
            )
        assert scenario.patient_id in manifest_bead_ids, (
            f"scenario {scenario.scenario_id} patient_id {scenario.patient_id!r} not a real manifest Bead ID"
        )


def test_scenario_generation_with_patients_and_per_patient_caps(
    tmp_path: Path, medbeadsd_binary: Path, synthea_fhir_dir: Path
) -> None:
    asyncio.run(_run_caps(tmp_path, medbeadsd_binary, synthea_fhir_dir))


async def _run_caps(tmp_path: Path, medbeadsd_binary: Path, synthea_fhir_dir: Path) -> None:
    data_dir = tmp_path / "data"
    manifest_path = tmp_path / "manifest.jsonl"
    run_manifest_path = tmp_path / "run_manifest.json"

    summary = await run_ingest(
        fhir_dir=synthea_fhir_dir,
        data_dir=data_dir,
        medbeadsd_path=medbeadsd_binary,
        manifest_path=manifest_path,
        run_manifest_path=run_manifest_path,
        limit=5,
    )
    assert summary.ok_patients == 5

    all_scenarios = generate_scenarios(fhir_dir=synthea_fhir_dir, manifest_path=manifest_path)
    limited_patients = generate_scenarios(fhir_dir=synthea_fhir_dir, manifest_path=manifest_path, patients=2)
    capped_per_patient = generate_scenarios(
        fhir_dir=synthea_fhir_dir, manifest_path=manifest_path, per_patient=1
    )

    limited_patient_ids = {s.patient_id for s in limited_patients}
    assert len(limited_patient_ids) <= 2

    all_patient_ids = {s.patient_id for s in all_scenarios}
    for patient_id in all_patient_ids:
        count = sum(1 for s in capped_per_patient if s.patient_id == patient_id)
        assert count <= 1, f"--per-patient 1 should cap {patient_id} to <=1 scenario, got {count}"
