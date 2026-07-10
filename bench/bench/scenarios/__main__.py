"""CLI: `uv run python -m bench.scenarios --fhir-dir <dir> --manifest <path> \
--out <path> [--patients N] [--per-patient M]`

Deterministically generates clinical question scenarios (see
bench/bench/scenarios/generate.py) from an already-completed
`bench.ingest` run's manifest.jsonl + the source FHIR Bundles, writing a
single YAML file. Same input bytes always produce the same output bytes
(this module's determinism contract — see tests/scenarios/test_determinism.py).
"""

from __future__ import annotations

import argparse
import sys
from pathlib import Path

from bench.scenarios.generate import generate_scenarios
from bench.scenarios.model import write_scenarios_yaml


def _parse_args(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(prog="python -m bench.scenarios", description=__doc__)
    parser.add_argument("--fhir-dir", required=True, type=Path, help="Synthea FHIR Bundle directory (*.json)")
    parser.add_argument("--manifest", required=True, type=Path, help="bench.ingest manifest.jsonl path")
    parser.add_argument("--out", required=True, type=Path, help="output scenarios YAML path")
    parser.add_argument(
        "--patients",
        type=int,
        default=None,
        help="generate scenarios for only the first N patients (patient_root-sorted) — for pilot runs",
    )
    parser.add_argument(
        "--per-patient",
        type=int,
        default=None,
        help="cap the number of scenarios generated per patient (deterministic truncation, see generate.py)",
    )
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    args = _parse_args(sys.argv[1:] if argv is None else argv)

    scenarios = generate_scenarios(
        fhir_dir=args.fhir_dir,
        manifest_path=args.manifest,
        patients=args.patients,
        per_patient=args.per_patient,
    )

    write_scenarios_yaml(scenarios, args.out)

    by_category: dict[str, int] = {}
    for s in scenarios:
        by_category[s.category] = by_category.get(s.category, 0) + 1
    print(f"generated {len(scenarios)} scenario(s) -> {args.out}", file=sys.stderr)
    for category, count in sorted(by_category.items()):
        print(f"  {category}: {count}", file=sys.stderr)

    return 0


if __name__ == "__main__":
    raise SystemExit(main())
