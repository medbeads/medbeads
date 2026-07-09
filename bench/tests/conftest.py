"""Shared fixtures for bench's pytest suite.

- `medbeadsd_binary`: builds cmd/medbeadsd (with the sqlite_fts5 build tag
  index.DB requires — see internal/engine/index/doc.go) once per test
  session into a scratch directory, session-scoped so integration and
  determinism tests don't each pay a fresh `go build`.
- `synthea_fhir_dir`: the real Synthea dataset directory. Tests that need it
  are skipped (not failed) if it is absent, since it lives outside the repo
  (~/medbeads-synthea/output/fhir, per bench/README.md) and is not fetched
  by CI.
"""

from __future__ import annotations

import os
import shutil
import subprocess
from pathlib import Path

import pytest

REPO_ROOT = Path(__file__).resolve().parents[2]
DEFAULT_SYNTHEA_FHIR_DIR = Path(os.environ.get("MEDBEADS_SYNTHEA_FHIR_DIR", "~/medbeads-synthea/output/fhir")).expanduser()


@pytest.fixture(scope="session")
def medbeadsd_binary(tmp_path_factory: pytest.TempPathFactory) -> Path:
    if shutil.which("go") is None:
        pytest.skip("go toolchain not on PATH; cannot build medbeadsd for integration tests")

    out_dir = tmp_path_factory.mktemp("medbeadsd-bin")
    binary_path = out_dir / "medbeadsd"

    result = subprocess.run(
        ["go", "build", "-tags", "sqlite_fts5", "-o", str(binary_path), "./cmd/medbeadsd"],
        cwd=str(REPO_ROOT),
        capture_output=True,
        text=True,
        timeout=180,
    )
    if result.returncode != 0:
        pytest.fail(f"go build ./cmd/medbeadsd failed:\nstdout={result.stdout}\nstderr={result.stderr}")

    return binary_path


@pytest.fixture(scope="session")
def synthea_fhir_dir() -> Path:
    if not DEFAULT_SYNTHEA_FHIR_DIR.is_dir():
        pytest.skip(
            f"Synthea dataset not found at {DEFAULT_SYNTHEA_FHIR_DIR} "
            "(set MEDBEADS_SYNTHEA_FHIR_DIR to override) — integration/determinism "
            "tests require the real dataset per bench/README.md"
        )
    return DEFAULT_SYNTHEA_FHIR_DIR
