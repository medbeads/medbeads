"""Fixtures for bench.run's pipeline tests: reuses the same fake (socket-
bound) embedding sidecar tests/retrieval/test_4arm_integration.py already
relies on (see tests/retrieval/conftest.py's own docstring for why it is a
real HTTP server, not an in-process TestClient) plus a small (2-patient)
scratch ingest helper shared by every test in this package.
"""

from __future__ import annotations

import asyncio
from pathlib import Path

import pytest

from bench.ingest.mcp_client import MedBeadsClient
from bench.ingest.run import run_ingest
from bench.scenarios.generate import generate_scenarios
from bench.scenarios.model import write_scenarios_yaml
from tests.retrieval.conftest import fake_embed_sidecar_url  # noqa: F401 - re-exported fixture

SCRATCH_PATIENT_LIMIT = 2


def _run_embed_backfill_sync(medbeadsd_binary: Path, data_dir: Path, embedder_url: str) -> None:
    import subprocess

    result = subprocess.run(
        [str(medbeadsd_binary), "embed", "-data", str(data_dir), "-embedder", embedder_url],
        capture_output=True,
        text=True,
        timeout=60,
    )
    assert result.returncode == 0, f"medbeadsd embed failed:\nstdout={result.stdout}\nstderr={result.stderr}"


@pytest.fixture
def scratch_run_fixture(
    tmp_path: Path, medbeadsd_binary: Path, synthea_fhir_dir: Path, fake_embed_sidecar_url: str
) -> tuple[Path, Path, Path]:
    """Ingests SCRATCH_PATIENT_LIMIT patients, backfills (fake) embeddings,
    and generates a scenarios YAML from that same scratch data — returns
    (data_dir, manifest_path, scenarios_yaml_path), the three inputs
    bench.run.pipeline.run_bench needs (via RunConfig).
    """
    data_dir = tmp_path / "data"
    # manifest.jsonl lives inside data_dir (bench.ingest.__main__'s own
    # default location — see bench/bench/ingest/__main__.py) since
    # bench.run.pipeline._dataset_fingerprint reads data_dir/manifest.jsonl
    # by that same convention (R8.4's "データセット指紋"), not an
    # arbitrary tmp_path-relative path.
    manifest_path = data_dir / "manifest.jsonl"
    run_manifest_path = data_dir / "ingest_run_manifest.json"

    async def _ingest() -> None:
        summary = await run_ingest(
            fhir_dir=synthea_fhir_dir,
            data_dir=data_dir,
            medbeadsd_path=medbeadsd_binary,
            manifest_path=manifest_path,
            run_manifest_path=run_manifest_path,
            limit=SCRATCH_PATIENT_LIMIT,
        )
        assert summary.ok_patients == SCRATCH_PATIENT_LIMIT, f"scratch ingest: {summary.failures}"

    asyncio.run(_ingest())

    _run_embed_backfill_sync(medbeadsd_binary, data_dir, fake_embed_sidecar_url)

    scenarios = generate_scenarios(fhir_dir=synthea_fhir_dir, manifest_path=manifest_path, per_patient=2)
    assert scenarios, "scratch ingest (2 patients) should produce at least one scenario"
    scenarios_path = tmp_path / "scenarios.yaml"
    write_scenarios_yaml(scenarios, scenarios_path)

    return data_dir, manifest_path, scenarios_path
