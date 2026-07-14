"""Run orchestration: walk every patient Bundle, ingest via MCP, write the
manifest JSONL + run manifest JSON (R8.4's "run manifest" primitive, scoped
down for this M1 slice to what ingest can honestly report: git commit,
target dataset dir, patient count, start/end time — config hash/model
version/dataset Merkle fingerprint are M2's `bench run` concern once
retrieval arms exist to configure).
"""

from __future__ import annotations

import json
import logging
import subprocess
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path

from bench.ingest.fhir import iter_patient_bundle_files
from bench.ingest.mcp_client import MedBeadsClient
from bench.ingest.patient import PatientIngestResult, ingest_patient_bundle

logger = logging.getLogger(__name__)


@dataclass
class RunSummary:
    total_patients: int
    ok_patients: int
    failed_patients: int
    total_beads: int
    started_at: str
    finished_at: str
    failures: list[dict[str, str]]
    # Every per-Bead "encounter reference didn't resolve, fell back to
    # patient root" warning across the whole run (reviewer-mandated
    # "サイレント禁止": these must be visible here and in run_manifest.json,
    # not just as a per-row parent_fallback flag in manifest.jsonl).
    parent_fallback_warnings: list[str] = field(default_factory=list)
    # U6 GO/NO-GO stat, summed across every patient in this run: total
    # DocumentReference resources dropped for status != "current" (see
    # bench.ingest.fhir.clinical_resources / count_dropped_superseded_
    # document_references and docs/decisions.md's 2026-07-11 U6 entry).
    dropped_superseded_document_references: int = 0


def _git_commit(repo_dir: Path) -> str:
    """The current commit hash of repo_dir, or "unknown" if not a git repo
    (e.g. an ad hoc checkout) — best-effort provenance, never fatal.
    """
    try:
        out = subprocess.run(
            ["git", "-C", str(repo_dir), "rev-parse", "HEAD"],
            capture_output=True,
            text=True,
            check=True,
            timeout=5,
        )
        return out.stdout.strip()
    except Exception:  # noqa: BLE001 - provenance best-effort, never blocks ingest
        return "unknown"


async def run_ingest(
    *,
    fhir_dir: Path,
    data_dir: Path,
    medbeadsd_path: Path,
    manifest_path: Path,
    run_manifest_path: Path,
    limit: int | None = None,
    repo_dir: Path | None = None,
) -> RunSummary:
    """Ingest every patient Bundle under fhir_dir (or the first `limit`, in
    filename-sorted order) into data_dir via one long-lived medbeadsd
    stdio session, writing manifest_path (JSONL, one row per Bead) and
    run_manifest_path (JSON, one object) as it goes.

    One MedBeadsClient session serves every patient (not one spawn per
    patient) — spawning a fresh process per patient would multiply process-
    start overhead by patient count for no benefit, since create_bead calls
    are already serialized per the task's "1患者ずつ順に呼ぶ".
    """
    bundle_files = iter_patient_bundle_files(fhir_dir)
    if limit is not None:
        bundle_files = bundle_files[:limit]

    started_at = datetime.now(timezone.utc).isoformat()

    ok_count = 0
    failed: list[PatientIngestResult] = []
    total_beads = 0
    parent_fallback_warnings: list[str] = []
    dropped_superseded_document_references = 0

    data_dir.mkdir(parents=True, exist_ok=True)
    manifest_path.parent.mkdir(parents=True, exist_ok=True)

    with manifest_path.open("w", encoding="utf-8") as manifest_file:
        async with MedBeadsClient(medbeadsd_path, data_dir) as client:
            for bundle_path in bundle_files:
                result = await ingest_patient_bundle(client, bundle_path)
                for row in result.manifest_rows:
                    manifest_file.write(json.dumps(row.to_json_dict(), sort_keys=True))
                    manifest_file.write("\n")
                manifest_file.flush()
                total_beads += result.bead_count
                dropped_superseded_document_references += result.dropped_superseded_document_references
                if result.ok:
                    ok_count += 1
                else:
                    failed.append(result)
                for warning in result.warnings:
                    tagged = f"{bundle_path.name}: {warning}"
                    logger.warning(tagged)
                    parent_fallback_warnings.append(tagged)

    finished_at = datetime.now(timezone.utc).isoformat()

    summary = RunSummary(
        total_patients=len(bundle_files),
        ok_patients=ok_count,
        failed_patients=len(failed),
        total_beads=total_beads,
        started_at=started_at,
        finished_at=finished_at,
        failures=[{"bundle": str(r.bundle_path), "error": r.error or ""} for r in failed],
        parent_fallback_warnings=parent_fallback_warnings,
        dropped_superseded_document_references=dropped_superseded_document_references,
    )

    run_manifest = {
        "git_commit": _git_commit(repo_dir or Path(__file__).resolve().parents[3]),
        "fhir_dir": str(fhir_dir),
        "data_dir": str(data_dir),
        "limit": limit,
        # The exact Bundle selection is part of dataset provenance. --limit is
        # deterministic only together with iter_patient_bundle_files' filename
        # ordering; recording both makes a 10-patient demo independently
        # reproducible and auditable without re-deriving that implementation
        # detail from source code.
        "selection_order": "filename_ascending",
        "selected_bundles": [path.name for path in bundle_files],
        "total_patients": summary.total_patients,
        "ok_patients": summary.ok_patients,
        "failed_patients": summary.failed_patients,
        "total_beads": summary.total_beads,
        "started_at": summary.started_at,
        "finished_at": summary.finished_at,
        "failures": summary.failures,
        "parent_fallback_warnings": summary.parent_fallback_warnings,
        "dropped_superseded_document_references": summary.dropped_superseded_document_references,
    }
    run_manifest_path.parent.mkdir(parents=True, exist_ok=True)
    with run_manifest_path.open("w", encoding="utf-8") as f:
        json.dump(run_manifest, f, indent=2, sort_keys=True)
        f.write("\n")

    return summary
