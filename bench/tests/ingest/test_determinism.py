"""Determinism test: `--limit 5` ingested twice, into two independent data
dirs, must yield the same set of Bead IDs in the manifest — Bead.ID is a
content hash (see internal/engine/bead/bead.go's ComputeID), so a
deterministic FHIR->Bead conversion must reproduce identical IDs on rerun
regardless of data directory, process instance, or wall-clock time (author
is a fixed string, per bench.ingest.beads.INGEST_AUTHOR, so timestamps in
content never leak wall-clock nondeterminism into the hash either).
"""

from __future__ import annotations

import asyncio
import json
from pathlib import Path

from bench.ingest.run import run_ingest


def test_limit_5_twice_yields_identical_bead_id_sets(
    tmp_path: Path, medbeadsd_binary: Path, synthea_fhir_dir: Path
) -> None:
    asyncio.run(_run(tmp_path, medbeadsd_binary, synthea_fhir_dir))


async def _run(tmp_path: Path, medbeadsd_binary: Path, synthea_fhir_dir: Path) -> None:
    bead_id_sets = []
    manifest_row_counts = []

    for i in (1, 2):
        data_dir = tmp_path / f"data-{i}"
        manifest_path = tmp_path / f"manifest-{i}.jsonl"
        run_manifest_path = tmp_path / f"run_manifest-{i}.json"

        summary = await run_ingest(
            fhir_dir=synthea_fhir_dir,
            data_dir=data_dir,
            medbeadsd_path=medbeadsd_binary,
            manifest_path=manifest_path,
            run_manifest_path=run_manifest_path,
            limit=5,
        )
        assert summary.failed_patients == 0, summary.failures

        rows = [json.loads(line) for line in manifest_path.read_text(encoding="utf-8").splitlines()]
        bead_id_sets.append({row["bead_id"] for row in rows})
        manifest_row_counts.append(len(rows))

    assert manifest_row_counts[0] == manifest_row_counts[1]
    assert manifest_row_counts[0] > 0
    assert bead_id_sets[0] == bead_id_sets[1], (
        "same --limit 5 input must yield the same Bead ID set on rerun "
        f"(counts: {manifest_row_counts}, "
        f"symmetric_difference size: {len(bead_id_sets[0] ^ bead_id_sets[1])})"
    )
