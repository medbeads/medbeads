"""Reads internal/engine/antigen/dictionary.json (a versioned, hand-reviewed
JSON *data* asset — not Go code, see that package's own doc.go for why it is
committed as data rather than generated) to derive rxnorm -> risk: antigen
mappings, for the medication_interaction_surface scenario template's ground
truth (bench.scenarios.generate).

Per R8.5/R8.1, scenario generation is fully offline/deterministic from
--fhir-dir + --manifest alone (no medbeadsd process, no MCP round trip — the
lead's own `bench.scenarios --fhir-dir ... --manifest ... --out ...` CLI
shape has no --medbeadsd/--data-dir flag at all), so this module cannot ask
a running server what antigen.Extract would have computed for one
MedicationRequest; reading the *dictionary data* directly is the narrowest
possible way to replicate just the rxnorm->risk: half of Extract's
deterministic rule (the coding[]-walk half — collectCodings, direct
SNOMED/LOINC/RxNorm extraction — is reimplemented narrowly in generate.py's
own _rxnorm_codes, scoped to exactly MedicationRequest.medicationCodeable
Concept.coding[]/medicationReference-resolved code.coding[], not the full
generic structural walk antigen.Extract does over arbitrary content).

This module never imports anything under internal/engine (Go); it only
reads dictionary.json as data, matching bench.ingest's existing
"engine を直接 import しない" discipline (docs/requirements.md R8.5) — the one
FHIR-day exception documented in this task's lead decisions is Go tests
under internal/mcpserver, not bench/.
"""

from __future__ import annotations

import json
from functools import lru_cache
from pathlib import Path
from typing import Any

# internal/engine/antigen/dictionary.json's location relative to this repo's
# layout — bench/bench/scenarios/antigen_dict.py -> repo root is 4 parents up
# (bench/bench/scenarios/ -> bench/bench/ -> bench/ -> repo root).
_DICTIONARY_RELATIVE_PATH = "internal/engine/antigen/dictionary.json"


def _repo_root() -> Path:
    return Path(__file__).resolve().parents[3]


def _dictionary_path() -> Path:
    return _repo_root() / _DICTIONARY_RELATIVE_PATH


@lru_cache(maxsize=1)
def _load_dictionary() -> dict[str, Any]:
    path = _dictionary_path()
    with path.open("r", encoding="utf-8") as f:
        return json.load(f)


def risk_antigens_for_rxnorm(code: str) -> list[str]:
    """The risk: antigens dictionary.json's rxnorm[code] entry carries, or []
    if code is not in the dictionary (mirrors antigen.deriveFromRxNorm's own
    "unknown code -> no derived antigens, never an error" contract)."""
    entry = _load_dictionary().get("rxnorm", {}).get(code)
    if not isinstance(entry, dict):
        return []
    risk = entry.get("risk")
    if not isinstance(risk, list):
        return []
    return [r for r in risk if isinstance(r, str)]
