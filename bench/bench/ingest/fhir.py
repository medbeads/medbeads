"""Synthea FHIR Bundle loading and clinical-resource filtering.

Resource universe observed in a 30-bundle sample of
~/medbeads-synthea/output/fhir/ (VERIFIED by direct inspection, not just the
mapping docs, since Synthea's own resource mix can drift between generator
versions): AllergyIntolerance, CarePlan, CareTeam, Claim, Condition, Device,
DiagnosticReport, DocumentReference, Encounter, ExplanationOfBenefit,
ImagingStudy, Immunization, Medication, MedicationAdministration,
MedicationRequest, Observation, Patient, Procedure, Provenance,
SupplyDelivery.

INCLUDED_RESOURCE_TYPES below is the intersection of "clinically meaningful"
(docs/fhir_timeline_mapping.md's 10 timeline-visible types plus
AllergyIntolerance, which v2's mapping docs list as a recognized clinical
resource even though the v2 sample bundle happened to have none) with what
this task explicitly asks to keep. Excluded, and why:

  - Claim, ExplanationOfBenefit: billing/insurance, not clinical
    (docs/fhir_timeline_mapping.md's own "非表示" list; the task's ground
    truth manifest cares about answerable clinical questions).
  - CarePlan, CareTeam: administrative care-coordination metadata, not a
    discrete clinical event with a single timestamp/finding.
  - Device, SupplyDelivery: equipment/logistics records, not patient
    clinical history.
  - Provenance: FHIR meta-provenance about the Bundle itself, not a clinical
    fact.
  - Medication, MedicationAdministration: Synthea emits `Medication` only as
    a side-table referenced by MedicationRequest.medicationReference in some
    versions, and MedicationAdministration duplicates MedicationRequest's
    clinical content without adding a distinct timestamp semantic v2 mapped;
    kept out to match v2's mapped set exactly (docs/mapping.md lists
    MedicationRequest, not these two).
  - ImagingStudy: present in Synthea output and listed as a "clinically
    important, timeline-visible" type in docs/mapping.md ("今回追加"), so it
    IS included below despite carrying no evidence/BLOB handling in this M1
    slice (content is just study/series metadata JSON, no pixel data).

DocumentReference is now INCLUDED (U6, specs/U6_clinical_note.md), reversing
the earlier M1-slice decision to treat it as non-clinical: it is ingested as
a dedicated `clinical_note` Bead (see bench.ingest.beads), not a generic
`fhir_documentreference` Bead — its base64 narrative is decoded to
raw_text, so the earlier "same fact appears twice, once structured once as
prose" ambiguity concern is superseded by treating the note as its own
distinct clinical artifact (the free-text/assessment narrative a discrete
FHIR resource cannot carry), not a duplicate of the structured resources.
Only `status == "current"` DocumentReferences are ingested (see
clinical_resources below) — Synthea reissues a new cumulative
DocumentReference at almost every encounter and marks the prior one
`superseded` (VERIFIED: 97% of DocumentReference resources in a
~/medbeads-synthea/output/fhir/ sample are `superseded`), so ingesting every
status would produce ~37K near-duplicate notes across the corpus and defeat
FTS/retrieve/judge ground-truth attribution. This is a user ruling
(docs/decisions.md 2026-07-11 U6 entry): past-narrative history is
discarded, not preserved as an amends chain, in this unit.

Patient is handled separately (ingest.py's root-Bead special case), not
through this filter.
"""

from __future__ import annotations

import json
from dataclasses import dataclass
from pathlib import Path
from typing import Any

# Clinical resource types ingested as Beads (Patient excluded: handled
# separately as the root patient_registration Bead). DocumentReference is
# ingested as a `clinical_note` Bead (bench.ingest.beads), not
# `fhir_documentreference` like the rest of this set — see this module's
# docstring for the U6 status=="current" filter that additionally applies to
# it (clinical_resources below).
INCLUDED_RESOURCE_TYPES: frozenset[str] = frozenset(
    {
        "Encounter",
        "Condition",
        "Observation",
        "MedicationRequest",
        "Procedure",
        "DiagnosticReport",
        "Immunization",
        "AllergyIntolerance",
        "ImagingStudy",
        "DocumentReference",
    }
)

# Explicitly excluded, non-clinical/administrative resource types (documented
# above). Not consulted at runtime — INCLUDED_RESOURCE_TYPES is the only
# filter that matters — but kept as a named constant so a reviewer or future
# maintainer can see the exclusion list without re-deriving it from prose.
EXCLUDED_RESOURCE_TYPES: frozenset[str] = frozenset(
    {
        "Claim",
        "ExplanationOfBenefit",
        "CarePlan",
        "CareTeam",
        "Device",
        "SupplyDelivery",
        "Provenance",
        "Medication",
        "MedicationAdministration",
    }
)


@dataclass(frozen=True)
class FhirResource:
    """One FHIR resource entry from a Bundle, with its Bundle-local identity.

    fhir_id is the resource's own `id` field (used for cross-references
    within the id_map); full_url is the Bundle entry's `fullUrl` (Synthea
    uses `urn:uuid:<id>` there, which is also how other resources'
    `reference` fields point at this one — see docs' "IDマップ" discussion
    and v2's import_fhir.py, which indexes id_map by both forms).
    """

    resource_type: str
    fhir_id: str
    full_url: str | None
    data: dict[str, Any]


def load_bundle(path: Path) -> dict[str, Any]:
    """Load one Synthea FHIR Bundle JSON file."""
    with path.open("r", encoding="utf-8") as f:
        return json.load(f)


def is_patient_bundle_file(path: Path) -> bool:
    """True unless path is one of Synthea's non-patient sidecar files.

    Synthea's fhir/ output directory also contains exactly two
    non-patient files per run: hospitalInformation<ts>.json and
    practitionerInformation<ts>.json (VERIFIED: `ls` against the real
    ~/medbeads-synthea/output/fhir/ directory showed precisely these two
    names out of 1,137 files, matching the task's "hospital*/practitioner*
    は患者でない" note and yielding exactly 1,135 patient bundles).
    """
    name = path.name
    return not (name.startswith("hospitalInformation") or name.startswith("practitionerInformation"))


def iter_patient_bundle_files(fhir_dir: Path) -> list[Path]:
    """Every patient Bundle file directly under fhir_dir, sorted by name.

    Sorting by filename gives a stable, deterministic file processing order
    (Synthea filenames embed a per-patient UUID, so alphabetical order is
    arbitrary but 100% reproducible across runs/machines).
    """
    return sorted(p for p in fhir_dir.glob("*.json") if is_patient_bundle_file(p))


def find_patient_entry(bundle: dict[str, Any]) -> dict[str, Any] | None:
    """The Bundle entry whose resource is the Patient, or None."""
    for entry in bundle.get("entry", []):
        if entry.get("resource", {}).get("resourceType") == "Patient":
            return entry
    return None


def clinical_resources(bundle: dict[str, Any]) -> list[FhirResource]:
    """Every entry in bundle whose resourceType is in INCLUDED_RESOURCE_TYPES.

    A DocumentReference entry with status != "current" is dropped here
    (never appears in the returned list at all) — the U6 superseded-note
    filter (see this module's docstring: Synthea reissues a cumulative
    DocumentReference at nearly every encounter and marks the prior one
    superseded, so an unconditional include would produce ~37K near-duplicate
    notes across the corpus). Use count_dropped_superseded_document_references
    on the same bundle for the "サイレント禁止" drop count ingest.py surfaces
    in its per-patient stats — kept as a separate pass rather than a tuple
    return here so every existing caller that only wants the resource list
    (bench.scenarios.generate, tests) is unaffected by this filter's addition.

    Patient is deliberately excluded here too (ingest.py treats it as the
    root Bead via find_patient_entry, not via this generic list).
    """
    out: list[FhirResource] = []
    for entry in bundle.get("entry", []):
        resource = entry.get("resource", {})
        rtype = resource.get("resourceType")
        if rtype not in INCLUDED_RESOURCE_TYPES:
            continue
        if rtype == "DocumentReference" and resource.get("status") != "current":
            continue
        fhir_id = resource.get("id", "")
        out.append(
            FhirResource(
                resource_type=rtype,
                fhir_id=fhir_id,
                full_url=entry.get("fullUrl"),
                data=resource,
            )
        )
    return out


def count_dropped_superseded_document_references(bundle: dict[str, Any]) -> int:
    """The number of DocumentReference entries in bundle that
    clinical_resources drops for being status != "current" — the U6
    GO/NO-GO stat (docs/decisions.md 2026-07-11 U6 entry: "サイレントに捨て
    ない", must be counted in ingest stats, not silently dropped). A separate
    pass over bundle rather than a clinical_resources side-channel, so
    callers that don't need this count (the common case) pay no API-shape
    cost for it.
    """
    dropped = 0
    for entry in bundle.get("entry", []):
        resource = entry.get("resource", {})
        if resource.get("resourceType") == "DocumentReference" and resource.get("status") != "current":
            dropped += 1
    return dropped


def index_medications_by_ref(bundle: dict[str, Any]) -> dict[str, dict[str, Any]]:
    """Every `Medication` resource entry in bundle, keyed by both its
    `fullUrl` and its bare `id` (Synthea/Medication is a "side-table"
    resource: a MedicationRequest with `medicationReference` points at one
    of these by `urn:uuid:<id>` — the same both-forms convention this
    module already uses for its own id_map, so a `medicationReference`
    lookup can use either the bare or `urn:uuid:`-prefixed form directly).

    Medication itself is never turned into a Bead (see the module
    docstring's exclusion list — it is a definitional side-table entry with
    no clinical timestamp of its own) — this index exists only so
    patient.py can inline-resolve a MedicationRequest's medication code at
    Bead-creation time (fix for the reviewer's "rxnorm antigen dies on
    medicationReference-shaped MedicationRequest" finding).
    """
    out: dict[str, dict[str, Any]] = {}
    for entry in bundle.get("entry", []):
        resource = entry.get("resource", {})
        if resource.get("resourceType") != "Medication":
            continue
        full_url = entry.get("fullUrl")
        fhir_id = resource.get("id")
        if full_url:
            out[full_url] = resource
        if fhir_id:
            out[fhir_id] = resource
    return out


def resolve_medication_code(
    medication_request: dict[str, Any], medications_by_ref: dict[str, dict[str, Any]]
) -> dict[str, Any] | None:
    """The referenced Medication resource's `code` (a CodeableConcept), for
    a MedicationRequest that uses `medicationReference` instead of an
    inline `medicationCodeableConcept`, or None if there is no
    `medicationReference`, or it does not resolve within this Bundle.

    VERIFIED (200-file sample of ~/medbeads-synthea/output/fhir/): ~29.5%
    of MedicationRequest resources use `medicationReference` exclusively
    (no `medicationCodeableConcept` at all), and every sampled reference
    resolves to a `Medication` entry in the *same* Bundle carrying a
    `code.coding[]` with an rxnorm system URI — Synthea always inlines the
    referenced Medication in the same export file, so no cross-Bundle
    fetch is ever required (a cross-Bundle reference would simply fail to
    resolve here, returning None, rather than this module reaching outside
    its one input file).
    """
    ref = medication_request.get("medicationReference")
    if not isinstance(ref, dict):
        return None
    reference = ref.get("reference")
    if not isinstance(reference, str) or not reference:
        return None
    medication = medications_by_ref.get(reference)
    if medication is None:
        return None
    code = medication.get("code")
    if not isinstance(code, dict):
        return None
    return code


# Per-resource-type ordered list of date fields to try, following v2's
# import_fhir.py date-field heuristic (`effectiveDateTime` / `period.start` /
# `authoredOn`) generalized per docs/mapping.md's "詳細マッピング" section (§2-8,
# one canonical date field per resource type) so every included type has a
# deterministic, documented priority instead of v2's single generic fallback
# chain. Nested paths use "." separators (only one level deep is needed here).
_DATE_FIELD_PRIORITY: dict[str, tuple[str, ...]] = {
    "Encounter": ("period.start",),
    "Condition": ("onsetDateTime", "recordedDate"),
    "Observation": ("effectiveDateTime", "effectivePeriod.start"),
    "MedicationRequest": ("authoredOn",),
    "Procedure": ("performedDateTime", "performedPeriod.start"),
    "DiagnosticReport": ("effectiveDateTime", "issued"),
    "Immunization": ("occurrenceDateTime",),
    "AllergyIntolerance": ("recordedDate", "onsetDateTime"),
    "ImagingStudy": ("started",),
    "DocumentReference": ("date",),
}

# Sentinel timestamp for a resource with no usable date field at all, so it
# sorts last (deterministically) rather than crashing the ingest. Chosen to
# match v2's own fallback literal ("2099-01-01") for continuity with the
# documented v2 semantics.
_NO_DATE_SENTINEL = "2099-01-01"


def _dig(data: dict[str, Any], path: str) -> Any:
    node: Any = data
    for part in path.split("."):
        if not isinstance(node, dict):
            return None
        node = node.get(part)
    return node


def resource_timestamp(resource: FhirResource) -> str:
    """The clinical timestamp for resource, per _DATE_FIELD_PRIORITY.

    Falls back to _NO_DATE_SENTINEL if none of the type's candidate fields
    are present, so sorting/ingestion never raises on a sparse resource.
    """
    for field in _DATE_FIELD_PRIORITY.get(resource.resource_type, ()):
        value = _dig(resource.data, field)
        if isinstance(value, str) and value:
            return value
    return _NO_DATE_SENTINEL


def resource_encounter_reference(resource: FhirResource) -> tuple[str | None, bool]:
    """The Encounter reference for resource, and whether it came from the
    nested context.encounter[] path (True) rather than the top-level
    `encounter` field (False/None case).

    Most included resource types carry a top-level `encounter.reference`
    (checked first, unchanged from the original single-field behavior).
    DocumentReference instead nests it under `context.encounter[0].reference`
    (U6, specs/U6_clinical_note.md: VERIFIED 983/983 real DocumentReferences
    resolve their Encounter parent this way, always exactly one entry) — this
    nested path is only consulted when the top-level field is absent, so a
    resource carrying both would keep the top-level value (there is no
    real-data case observed where both are present and disagree, but
    top-level-wins keeps the original behavior's priority for every
    non-DocumentReference caller unchanged).

    Returns (None, False) if neither path yields a usable reference.
    """
    encounter = resource.data.get("encounter")
    if isinstance(encounter, dict):
        ref = encounter.get("reference")
        if isinstance(ref, str) and ref:
            return ref, False

    context = resource.data.get("context")
    if isinstance(context, dict):
        nested = context.get("encounter")
        if isinstance(nested, list) and nested:
            first = nested[0]
            if isinstance(first, dict):
                ref = first.get("reference")
                if isinstance(ref, str) and ref:
                    return ref, True

    return None, False


def resource_encounter_reference_multiplicity_warning(resource: FhirResource) -> str | None:
    """A warning string if resource's nested context.encounter[] carries more
    than one entry (only the first is ever used as the parent — see
    resource_encounter_reference), or None if there is nothing to warn about.
    VERIFIED (real Synthea sample): every DocumentReference's context.encounter
    has exactly one entry, so this should never fire in practice, but the
    lead's "サイレント禁止" ruling requires it be counted/logged if it ever
    does rather than silently taking [0] and moving on.
    """
    context = resource.data.get("context")
    if not isinstance(context, dict):
        return None
    nested = context.get("encounter")
    if isinstance(nested, list) and len(nested) > 1:
        return (
            f"{resource.resource_type} {resource.fhir_id}: context.encounter has "
            f"{len(nested)} entries, using only the first as parent"
        )
    return None
