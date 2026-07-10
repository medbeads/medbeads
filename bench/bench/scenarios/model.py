"""Scenario dataclass + deterministic YAML (de)serialization.

Per the lead's spec: "YAML 出力(patient_id, question, answer,
evidence_bead_ids, category, reasoning_type)". One YAML document per file is
a list of scenario dicts, sorted by a stable key (see write_scenarios_yaml)
so re-running generation against identical input bytes produces a
byte-identical file — this task's "同一入力→同一 YAML(決定論テスト)"
requirement.
"""

from __future__ import annotations

from dataclasses import asdict, dataclass, field
from pathlib import Path
from typing import Any

import yaml


@dataclass(frozen=True)
class Scenario:
    """One generated clinical question + deterministically-derived ground
    truth, per the lead's four category templates (see generate.py)."""

    patient_id: str
    question: str
    answer: str
    evidence_bead_ids: list[str]
    category: str
    reasoning_type: str
    # scenario_id is a stable, content-derived identifier (see generate.py's
    # _scenario_id) — not part of the lead's minimum required field list,
    # but included because multiple scenarios can share a
    # (patient_id, category) pair and a downstream consumer (bench.metrics,
    # 3b) needs a way to reference "this exact scenario" in a run log
    # without re-deriving one from (patient_id, category, question) tuples.
    scenario_id: str = field(default="")

    def to_json_dict(self) -> dict[str, Any]:
        return {
            "scenario_id": self.scenario_id,
            "patient_id": self.patient_id,
            "question": self.question,
            "answer": self.answer,
            "evidence_bead_ids": list(self.evidence_bead_ids),
            "category": self.category,
            "reasoning_type": self.reasoning_type,
        }


def _sort_key(s: Scenario) -> tuple[str, str, str]:
    """Deterministic ordering for write_scenarios_yaml: (patient_id,
    category, scenario_id) — scenario_id is itself content-derived (see
    generate.py), so this key is a pure function of each Scenario's own
    fields, never of dict/set iteration order or wall-clock generation
    order."""
    return (s.patient_id, s.category, s.scenario_id)


def write_scenarios_yaml(scenarios: list[Scenario], out_path: Path) -> None:
    """Writes scenarios to out_path as one YAML document (a list), sorted by
    _sort_key for determinism, with yaml.safe_dump's own key-sorting
    (default_flow_style=False, sort_keys=True — a fixed dict key order
    within each scenario's own YAML mapping) so re-running generation
    against byte-identical input produces a byte-identical file.
    """
    ordered = sorted(scenarios, key=_sort_key)
    out_path.parent.mkdir(parents=True, exist_ok=True)
    with out_path.open("w", encoding="utf-8") as f:
        yaml.safe_dump(
            [s.to_json_dict() for s in ordered],
            f,
            default_flow_style=False,
            sort_keys=True,
            allow_unicode=True,
        )


def load_scenarios_yaml(path: Path) -> list[Scenario]:
    """Reads a scenarios YAML file back into Scenario objects — used by
    tests (determinism, evidence_bead_ids validation) and will be used by
    bench.metrics (3b) to drive the LLM/judge pipeline."""
    with path.open("r", encoding="utf-8") as f:
        rows = yaml.safe_load(f) or []
    out: list[Scenario] = []
    for row in rows:
        out.append(
            Scenario(
                scenario_id=row.get("scenario_id", ""),
                patient_id=row["patient_id"],
                question=row["question"],
                answer=row["answer"],
                evidence_bead_ids=list(row.get("evidence_bead_ids", [])),
                category=row["category"],
                reasoning_type=row["reasoning_type"],
            )
        )
    return out


# asdict is imported for downstream modules (e.g. future bench.metrics) that
# want a Scenario's full field set as a plain dict without going through
# to_json_dict's evidence_bead_ids copy — re-exported rather than
# unused-import-lint-suppressed.
__all__ = ["Scenario", "write_scenarios_yaml", "load_scenarios_yaml", "asdict"]
