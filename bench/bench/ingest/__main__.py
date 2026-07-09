"""CLI: `uv run python -m bench.ingest --fhir-dir <dir> --data-dir <dir> \
--medbeadsd <path> [--limit N]`

Ingests Synthea FHIR Bundles into a medbeadsd data directory via MCP
(create_bead, system role), writing a ground-truth ID-map manifest
(manifest.jsonl) and a run manifest (run_manifest.json) into --data-dir.
"""

from __future__ import annotations

import argparse
import asyncio
import logging
import sys
from pathlib import Path

from bench.ingest.run import run_ingest

# Ensure bench.ingest.run's logger.warning(...) calls (parent-reference
# fallback warnings — reviewer's "サイレント禁止" requirement) actually reach
# stderr when this module is run as a CLI, not just as a library import
# under pytest (where a test can inspect RunSummary.parent_fallback_warnings
# directly without relying on logging config at all).
logging.basicConfig(level=logging.WARNING, format="%(levelname)s %(name)s: %(message)s", stream=sys.stderr)


def _parse_args(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(prog="python -m bench.ingest", description=__doc__)
    parser.add_argument("--fhir-dir", required=True, type=Path, help="Synthea FHIR Bundle directory (*.json)")
    parser.add_argument("--data-dir", required=True, type=Path, help="medbeadsd data directory to ingest into")
    parser.add_argument("--medbeadsd", required=True, type=Path, help="path to the medbeadsd binary")
    parser.add_argument(
        "--limit",
        type=int,
        default=None,
        help="ingest only the first N patients (filename-sorted) — for validation runs",
    )
    parser.add_argument(
        "--manifest",
        type=Path,
        default=None,
        help="ground-truth ID-map manifest JSONL path (default: <data-dir>/manifest.jsonl)",
    )
    parser.add_argument(
        "--run-manifest",
        type=Path,
        default=None,
        help="run manifest JSON path (default: <data-dir>/run_manifest.json)",
    )
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    args = _parse_args(sys.argv[1:] if argv is None else argv)

    manifest_path = args.manifest or (args.data_dir / "manifest.jsonl")
    run_manifest_path = args.run_manifest or (args.data_dir / "run_manifest.json")

    summary = asyncio.run(
        run_ingest(
            fhir_dir=args.fhir_dir,
            data_dir=args.data_dir,
            medbeadsd_path=args.medbeadsd,
            manifest_path=manifest_path,
            run_manifest_path=run_manifest_path,
            limit=args.limit,
        )
    )

    print(
        f"ingested {summary.ok_patients}/{summary.total_patients} patients, "
        f"{summary.total_beads} beads, {summary.failed_patients} failed",
        file=sys.stderr,
    )
    for failure in summary.failures:
        print(f"  FAILED {failure['bundle']}: {failure['error']}", file=sys.stderr)
    if summary.parent_fallback_warnings:
        print(
            f"  {len(summary.parent_fallback_warnings)} parent-reference fallback(s) "
            "(encounter reference not found in id_map; attached to patient root instead — "
            "see manifest.jsonl's parent_fallback column and run_manifest.json for detail)",
            file=sys.stderr,
        )

    return 0 if summary.failed_patients == 0 else 1


if __name__ == "__main__":
    raise SystemExit(main())
