"""Orchestration: spawn `medbeadsd serve -role viewer`, run N `retrieve`
calls against a deterministic (patient, query) sample, and report the
latency distribution against docs/requirements.md §7's "context bundle p95
<500ms" target.

Patient/query sampling, in order:

  1. Read data_dir/manifest.jsonl (bench.ingest's ground-truth manifest,
     already written by whatever `python -m bench.ingest` run populated
     data_dir) and collect every row with fhir_type == "Patient" — each
     row's `patient_root` is the Bead ID `retrieve(patient_id=...)` expects,
     and its `fhir_resource_id` is the original FHIR Patient.id, which this
     module joins back to that patient's *source* Bundle file under
     --fhir-dir (VERIFIED: bench.ingest.beads.plan_patient_root sets
     fhir_id = patient.get("id", "") on the same Patient resource
     bench.ingest.fhir.find_patient_entry locates, and Synthea's own bundle
     filenames also embed this same UUID — see bench/README.md's dataset
     note — so a glob on the UUID reliably finds the one matching file
     without assuming the manifest's row order lines up positionally with
     any particular `iter_patient_bundle_files` call).
  2. For each matched (patient_root, bundle_path) pair, bench.perf.queries
     derives up to `--queries-per-patient` deterministic FTS query strings
     from that patient's own real Medication/Condition/Observation display
     names (see queries.py's module doc for why, and why this is more
     realistic than one dataset-wide static query list).
  3. Every (patient_root, query) pair is called via retrieve(query=...,
     patient_id=patient_root, token_budget=...) in a fixed, deterministic
     round-robin order, repeated until `--queries` total calls have been
     timed (a small sample is cycled, not re-sampled, so `--queries 100`
     against a 20-patient/2-queries-per-patient sample runs the same 40
     distinct calls 2.5x over — deterministic and reproducible, at the cost
     of the greedy-packing warm-vs-cold-cache distinction not mattering here
     since retrieve has no cross-call cache to warm in this build).

One long-lived MedBeadsClient session (role="viewer") serves every call,
matching bench.ingest's own "one spawn, many calls" rationale (spawning a
fresh medbeadsd process per call would multiply process-start overhead by
call count for no benefit — see bench.ingest.run's identical reasoning).
"""

from __future__ import annotations

import json
import subprocess
import time
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

from bench.ingest.mcp_client import MedBeadsClient
from bench.perf.queries import PatientQuery, sample_patient_queries
from bench.perf.stats import LatencyStats, compute_latency_stats

DEFAULT_QUERIES_PER_PATIENT = 2
DEFAULT_TOKEN_BUDGET = 2000
DEFAULT_TOTAL_QUERIES = 100

# docs/requirements.md §7's "context bundle p95 <500ms" target.
TARGET_P95_S = 0.5


@dataclass
class PerfCall:
    """One timed retrieve() call's result, for the raw per-call log."""

    patient_root: str
    query: str
    elapsed_s: float
    anchor_count: int
    used_tokens: int


@dataclass
class PerfReport:
    data_dir: str
    medbeadsd_role: str
    token_budget: int
    total_calls: int
    distinct_patients: int
    distinct_queries: int
    started_at: str
    finished_at: str
    git_commit: str
    stats: LatencyStats
    target_p95_s: float
    target_met: bool
    calls: list[PerfCall] = field(default_factory=list)

    def to_json_dict(self) -> dict[str, Any]:
        return {
            "data_dir": self.data_dir,
            "medbeadsd_role": self.medbeadsd_role,
            "token_budget": self.token_budget,
            "total_calls": self.total_calls,
            "distinct_patients": self.distinct_patients,
            "distinct_queries": self.distinct_queries,
            "started_at": self.started_at,
            "finished_at": self.finished_at,
            "git_commit": self.git_commit,
            "target_p95_s": self.target_p95_s,
            "target_met": self.target_met,
            "stats": self.stats.to_json_dict(),
            "calls": [
                {
                    "patient_root": c.patient_root,
                    "query": c.query,
                    "elapsed_s": c.elapsed_s,
                    "anchor_count": c.anchor_count,
                    "used_tokens": c.used_tokens,
                }
                for c in self.calls
            ],
        }

    def human_summary(self) -> str:
        status = "PASS" if self.target_met else "FAIL"
        return (
            f"retrieve p95: n={self.stats.n} median={self.stats.median_s * 1000:.1f}ms "
            f"p95={self.stats.p95_s * 1000:.1f}ms p99={self.stats.p99_s * 1000:.1f}ms "
            f"max={self.stats.max_s * 1000:.1f}ms "
            f"(target: p95 < {self.target_p95_s * 1000:.0f}ms) [{status}]\n"
            f"  {self.distinct_patients} distinct patient(s), {self.distinct_queries} distinct "
            f"quer(y/ies), {self.total_calls} total call(s), token_budget={self.token_budget}"
        )


def _git_commit(repo_dir: Path) -> str:
    """Mirrors bench.ingest.run._git_commit: best-effort provenance, never fatal."""
    try:
        out = subprocess.run(
            ["git", "-C", str(repo_dir), "rev-parse", "HEAD"],
            capture_output=True,
            text=True,
            check=True,
            timeout=5,
        )
        return out.stdout.strip()
    except Exception:  # noqa: BLE001 - provenance best-effort, never blocks the perf run
        return "unknown"


def _read_patient_roots(manifest_path: Path) -> list[dict[str, str]]:
    """Every {fhir_resource_id, patient_root} pair from manifest_path's
    Patient rows, in file order (bench.ingest.run writes one row per Bead as
    it goes, so Patient rows appear in the same order patients were
    ingested).
    """
    out: list[dict[str, str]] = []
    with manifest_path.open("r", encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            row = json.loads(line)
            if row.get("fhir_type") == "Patient":
                out.append({"fhir_resource_id": row["fhir_resource_id"], "patient_root": row["patient_root"]})
    return out


def _find_bundle_path(fhir_dir: Path, fhir_resource_id: str) -> Path | None:
    """The Synthea Bundle file under fhir_dir whose filename embeds
    fhir_resource_id (Synthea's own naming convention embeds the Patient's
    FHIR id as the filename's trailing UUID — see this module's doc comment
    for the VERIFIED join-key rationale), or None if no such file is found
    (e.g. data_dir's manifest was produced against a different --fhir-dir
    than the one passed to this harness).
    """
    matches = sorted(fhir_dir.glob(f"*{fhir_resource_id}*.json"))
    return matches[0] if matches else None


def build_patient_queries(
    manifest_path: Path, fhir_dir: Path, *, queries_per_patient: int = DEFAULT_QUERIES_PER_PATIENT
) -> list[tuple[str, PatientQuery]]:
    """[(patient_root, PatientQuery)] for every patient in manifest_path that
    still has a resolvable source Bundle under fhir_dir. Patients whose
    Bundle can no longer be found (moved/renamed fhir_dir) are silently
    skipped — this is query *sampling*, not a completeness guarantee over
    the whole ingested dataset.
    """
    out: list[tuple[str, PatientQuery]] = []
    for row in _read_patient_roots(manifest_path):
        bundle_path = _find_bundle_path(fhir_dir, row["fhir_resource_id"])
        if bundle_path is None:
            continue
        for pq in sample_patient_queries([bundle_path], queries_per_patient=queries_per_patient):
            out.append((row["patient_root"], pq))
    return out


async def run_perf(
    *,
    data_dir: Path,
    fhir_dir: Path,
    medbeadsd_path: Path,
    manifest_path: Path | None = None,
    total_queries: int = DEFAULT_TOTAL_QUERIES,
    queries_per_patient: int = DEFAULT_QUERIES_PER_PATIENT,
    token_budget: int = DEFAULT_TOKEN_BUDGET,
    repo_dir: Path | None = None,
) -> PerfReport:
    manifest_path = manifest_path or (data_dir / "manifest.jsonl")
    if not manifest_path.is_file():
        raise FileNotFoundError(
            f"perf: manifest not found at {manifest_path} — run `python -m bench.ingest` "
            "against data_dir first (see bench/bench/ingest/__main__.py)"
        )

    sample = build_patient_queries(manifest_path, fhir_dir, queries_per_patient=queries_per_patient)
    if not sample:
        raise RuntimeError(
            f"perf: no (patient, query) pairs derived from {manifest_path} against --fhir-dir "
            f"{fhir_dir} — check --fhir-dir matches the directory data_dir was ingested from"
        )

    started_at = datetime.now(timezone.utc).isoformat()
    calls: list[PerfCall] = []

    # role="viewer": retrieve is a read-only tool (docs/requirements.md R6.3
    # reserves write access to the system role), so this harness never
    # requests more privilege than it uses.
    async with MedBeadsClient(medbeadsd_path, data_dir, role="viewer") as client:
        for i in range(total_queries):
            patient_root, pq = sample[i % len(sample)]
            start = time.perf_counter()
            out = await client.retrieve(query=pq.query, patient_id=patient_root, token_budget=token_budget)
            elapsed = time.perf_counter() - start
            # retrieveOut's AnchorIDs (internal/mcpserver/retrieve.go) is a Go
            # nil slice when a query matches no anchors, which the JSON
            # encoder emits as `null`, not `[]` — the MCP Python client thus
            # hands this back as None, not an empty list, so `or []` is
            # required here (a plain out.get("anchor_ids", []) default is
            # not enough, since the key IS present, just null-valued).
            anchor_ids = out.get("anchor_ids") or []
            calls.append(
                PerfCall(
                    patient_root=patient_root,
                    query=pq.query,
                    elapsed_s=elapsed,
                    anchor_count=len(anchor_ids),
                    used_tokens=out.get("used_tokens", 0),
                )
            )

    finished_at = datetime.now(timezone.utc).isoformat()

    stats = compute_latency_stats([c.elapsed_s for c in calls])
    target_met = stats.p95_s < TARGET_P95_S

    return PerfReport(
        data_dir=str(data_dir),
        medbeadsd_role="viewer",
        token_budget=token_budget,
        total_calls=len(calls),
        distinct_patients=len({p for p, _ in sample}),
        distinct_queries=len({pq.query for _, pq in sample}),
        started_at=started_at,
        finished_at=finished_at,
        git_commit=_git_commit(repo_dir or Path(__file__).resolve().parents[3]),
        stats=stats,
        target_p95_s=TARGET_P95_S,
        target_met=target_met,
        calls=calls,
    )
