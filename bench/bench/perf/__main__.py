"""CLI: `uv run python -m bench.perf --data-dir <dir> --fhir-dir <dir> \
--medbeadsd <path> [--queries N] [--queries-per-patient N] [--token-budget N] \
[--out <path>]`

Measures MCP `retrieve` latency (docs/requirements.md §7's "context bundle
p95 <500ms" target) against an already-ingested medbeadsd data directory
(see `python -m bench.ingest`, which must have been run against --data-dir
first — this tool reads its manifest.jsonl, it does not ingest anything
itself). Prints a human-readable summary to stderr and writes the full
JSON report (distribution + per-call log) to --out (default:
bench/perf_results/retrieve_<timestamp>.json) for M1 evidence commits.
"""

from __future__ import annotations

import argparse
import asyncio
import json
import sys
from datetime import datetime, timezone
from pathlib import Path

from bench.perf.run import (
    DEFAULT_QUERIES_PER_PATIENT,
    DEFAULT_TOKEN_BUDGET,
    DEFAULT_TOTAL_QUERIES,
    run_perf,
)

REPO_ROOT = Path(__file__).resolve().parents[3]
DEFAULT_RESULTS_DIR = REPO_ROOT / "bench" / "perf_results"


def _parse_args(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(prog="python -m bench.perf", description=__doc__)
    parser.add_argument("--data-dir", required=True, type=Path, help="already-ingested medbeadsd data directory")
    parser.add_argument(
        "--fhir-dir",
        required=True,
        type=Path,
        help="Synthea FHIR Bundle directory data_dir was ingested from (for query-text sampling)",
    )
    parser.add_argument("--medbeadsd", required=True, type=Path, help="path to the medbeadsd binary")
    parser.add_argument(
        "--queries",
        type=int,
        default=DEFAULT_TOTAL_QUERIES,
        help=f"total timed retrieve() calls (default {DEFAULT_TOTAL_QUERIES})",
    )
    parser.add_argument(
        "--queries-per-patient",
        type=int,
        default=DEFAULT_QUERIES_PER_PATIENT,
        help=f"distinct queries sampled per patient (default {DEFAULT_QUERIES_PER_PATIENT})",
    )
    parser.add_argument(
        "--token-budget",
        type=int,
        default=DEFAULT_TOKEN_BUDGET,
        help=f"retrieve's token_budget argument (default {DEFAULT_TOKEN_BUDGET})",
    )
    parser.add_argument(
        "--manifest",
        type=Path,
        default=None,
        help="ground-truth manifest path (default: <data-dir>/manifest.jsonl)",
    )
    parser.add_argument(
        "--out",
        type=Path,
        default=None,
        help="JSON report output path (default: bench/perf_results/retrieve_<UTC timestamp>.json)",
    )
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    args = _parse_args(sys.argv[1:] if argv is None else argv)

    out_path = args.out
    if out_path is None:
        ts = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")
        out_path = DEFAULT_RESULTS_DIR / f"retrieve_{ts}.json"

    report = asyncio.run(
        run_perf(
            data_dir=args.data_dir,
            fhir_dir=args.fhir_dir,
            medbeadsd_path=args.medbeadsd,
            manifest_path=args.manifest,
            total_queries=args.queries,
            queries_per_patient=args.queries_per_patient,
            token_budget=args.token_budget,
        )
    )

    out_path.parent.mkdir(parents=True, exist_ok=True)
    with out_path.open("w", encoding="utf-8") as f:
        json.dump(report.to_json_dict(), f, indent=2, sort_keys=True)
        f.write("\n")

    print(report.human_summary(), file=sys.stderr)
    print(f"wrote {out_path}", file=sys.stderr)

    return 0 if report.target_met else 1


if __name__ == "__main__":
    raise SystemExit(main())
