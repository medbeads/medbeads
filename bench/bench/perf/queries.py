"""Deterministic per-patient FTS query generation for the retrieve perf harness.

Per this task's requirement ("クエリはデータから決定論的に生成(患者の実在
medication/condition の表示名から作る等 — 手法を報告)"): a query is a
substring of a real display name pulled from that same patient's own FHIR
Bundle — MedicationRequest.medicationCodeableConcept.{text,coding[].display}
and Condition.code.{text,coding[].display} — rather than a fixed static list
(internal/engine/perf_bench_test.go's Go FTS test uses a fixed representative
list instead, since it has no per-patient FHIR source to draw from at that
layer; this Python harness does, via bench.ingest.fhir, so it uses the more
realistic per-patient approach).

Determinism: for a given patient Bundle file, _display_names() always walks
bundle["entry"] in the same (file) order and returns names in that same
order; picking the first N (after de-duplication) makes query selection a
pure function of the Bundle's own byte content, matching bench.ingest's
existing "same input -> same output" discipline (see bench/bench/ingest/beads.py
sort_key's doc comment for the precedent this follows).
"""

from __future__ import annotations

import re
from dataclasses import dataclass
from pathlib import Path
from typing import Any

from bench.ingest.fhir import load_bundle

# Resource types this module draws query text from, in priority order:
# MedicationRequest and Condition are the two families this task calls out
# explicitly ("薬剤名・症状"); Observation is included last, as a fallback
# fill only, for the "検査名" (lab test) family — Synthea also stamps social-
# history Observations (housing/education/employment questionnaire items)
# with clinically-uninteresting code.text like "Received" or "Full-time",
# so Observation entries are only used once a patient's Medication/Condition
# names are exhausted (see display_names_for_bundle's two-pass collection
# below), keeping generated queries weighted toward drug/diagnosis names.
_PRIORITY_SOURCE_TYPES = ("MedicationRequest", "Condition")
_FALLBACK_SOURCE_TYPES = ("Observation",)

# A query word must be a single run of ASCII letters (no digits, hyphens,
# apostrophes, or other punctuation): SQLite FTS5's default (unicode61)
# query-string parser gives special meaning to `-` (NOT), `"` (phrase), `:`
# (column filter), and other bareword-adjacent punctuation — a raw
# multi-word display name like "Full-time employment (finding)" fed
# unquoted to MATCH can be misparsed as a column filter or boolean
# expression and error out (VERIFIED: "full-time" as a bare retrieve(query=)
# argument produced "index: search \"full-time\": no such column: time" from
# a real medbeadsd instance during this harness's own smoke test — the fix
# applied here is to only ever select a plain alphabetic substring as this
# harness's own query text, not to change index.Search's MATCH-string
# escaping, which is core code out of this task's scope).
_WORD_RE = re.compile(r"[A-Za-z]+")

# Words shorter than this are usually noise (units, articles, "mg"-adjacent
# fragments) rather than a meaningful partial-name substring.
_MIN_WORD_LEN = 4


def _codeable_concept_names(concept: Any) -> list[str]:
    """Every human-readable name on one FHIR CodeableConcept: its own `text`
    (if any) first, then each coding[].display (if any), in that order —
    `text` is usually the more specific/human phrase Synthea emits (e.g. drug
    strength/form), coding[].display is the coarser terminology display name.
    """
    if not isinstance(concept, dict):
        return []
    names: list[str] = []
    text = concept.get("text")
    if isinstance(text, str) and text.strip():
        names.append(text.strip())
    for coding in concept.get("coding") or []:
        if not isinstance(coding, dict):
            continue
        display = coding.get("display")
        if isinstance(display, str) and display.strip():
            names.append(display.strip())
    return names


def _resource_names(resource: dict[str, Any]) -> list[str]:
    rtype = resource.get("resourceType")
    if rtype == "MedicationRequest":
        # Same medicationCodeableConcept-or-medicationReference duality
        # bench.ingest.beads._content_with_resolved_medication handles at
        # ingest time; medicationReference-only requests are simply skipped
        # here (their code lives on a separate Medication resource this
        # module does not cross-reference — acceptable since this is query
        # *sampling*, not an exhaustive per-patient index, and every patient
        # has multiple MedicationRequest/Condition/Observation resources to
        # draw from in practice).
        return _codeable_concept_names(resource.get("medicationCodeableConcept"))
    if rtype == "Condition":
        return _codeable_concept_names(resource.get("code"))
    if rtype == "Observation":
        return _codeable_concept_names(resource.get("code"))
    return []


def _extract_names(bundle: dict[str, Any], source_types: tuple[str, ...], seen: set[str]) -> list[str]:
    """Every not-yet-seen display name from bundle's entries whose
    resourceType is in source_types, in Bundle entry order. seen is mutated
    (shared across the priority/fallback passes in display_names_for_bundle
    so a name already collected in the priority pass is never duplicated by
    the fallback pass too).
    """
    out: list[str] = []
    for entry in bundle.get("entry", []):
        resource = entry.get("resource")
        if not isinstance(resource, dict):
            continue
        if resource.get("resourceType") not in source_types:
            continue
        for name in _resource_names(resource):
            if name not in seen:
                seen.add(name)
                out.append(name)
    return out


def display_names_for_bundle(bundle_path: Path) -> list[str]:
    """Every de-duplicated display name found in bundle_path's clinical
    resources, in two passes: MedicationRequest/Condition names first (see
    _PRIORITY_SOURCE_TYPES), then Observation names as a fallback fill (see
    _FALLBACK_SOURCE_TYPES) — both passes walk bundle["entry"] in the same
    (file) order, so the overall result is a deterministic function of the
    Bundle's own byte content. Returns [] (never raises) for a Bundle this
    module can't parse, since a single malformed patient file should not
    abort query generation for the rest of the sample.
    """
    try:
        bundle = load_bundle(bundle_path)
    except Exception:  # noqa: BLE001 - best-effort query source, never fatal
        return []

    seen: set[str] = set()
    out = _extract_names(bundle, _PRIORITY_SOURCE_TYPES, seen)
    out.extend(_extract_names(bundle, _FALLBACK_SOURCE_TYPES, seen))
    return out


def _longest_word(name: str) -> str | None:
    """The longest (ties broken by first occurrence) run of ASCII letters in
    name, lowercased, or None if name has no word of at least _MIN_WORD_LEN
    letters.

    FTS5 trigram matching (internal/engine/index/migrations/0001_init.sql:
    `tokenize='trigram'`) matches on substrings, not whole-word tokens, so a
    single representative word (rather than the full multi-word display
    string, which would require an exact-phrase-shaped MATCH query this
    harness does not attempt to construct) is a deterministic, realistic
    "partial string" query per this task's "薬剤名・検査名・症状の部分文字列"
    wording. The *longest* word (not simply the first) is chosen because
    Synthea's display names often lead with a generic/short word ("Loss",
    "Only", "Received" on social-history-style text, or dosage-form words
    like "Oral") that is a weak, low-selectivity anchor query compared to the
    drug/condition name itself, which is usually the longest word present.
    Restricting to a pure `[A-Za-z]+` run (see _WORD_RE's doc comment) also
    sidesteps FTS5 query-string special characters entirely, rather than
    trying to quote/escape a word that might contain them.
    """
    words = _WORD_RE.findall(name)
    candidates = [w for w in words if len(w) >= _MIN_WORD_LEN]
    if not candidates:
        return None
    return max(candidates, key=len).lower()


@dataclass(frozen=True)
class PatientQuery:
    """One (patient, query) pair sampled by sample_patient_queries."""

    bundle_path: Path
    query: str


def sample_patient_queries(
    bundle_paths: list[Path], *, queries_per_patient: int = 2
) -> list[PatientQuery]:
    """Up to queries_per_patient deterministic queries per Bundle in
    bundle_paths (already sorted/limited by the caller — see
    bench.ingest.fhir.iter_patient_bundle_files, which this harness's CLI
    reuses to stay consistent with how the same patients were ingested).

    A patient contributing zero usable display names (e.g. every
    MedicationRequest uses medicationReference and there are no Condition/
    Observation resources — vanishingly rare in the Synthea corpus, but
    possible for an edge-case Bundle) simply contributes zero queries rather
    than raising, so one patient's sparse data cannot abort the whole harness
    run.
    """
    out: list[PatientQuery] = []
    for path in bundle_paths:
        names = display_names_for_bundle(path)
        words: list[str] = []
        seen_words: set[str] = set()
        for name in names:
            word = _longest_word(name)
            if word and word not in seen_words:
                seen_words.add(word)
                words.append(word)
            if len(words) >= queries_per_patient:
                break
        for word in words:
            out.append(PatientQuery(bundle_path=path, query=word))
    return out
