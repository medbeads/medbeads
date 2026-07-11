"""FHIR resource -> Bead conversion: type, timestamp, parent-edge rules.

Semantics follow `git show v2.2.0:scripts/import_fhir.py` (v2's Patient ->
patient_registration root, Encounter -> Patient parent, MedicationRequest ->
its Encounter (or Patient if none), everything-else -> its Encounter (or
Patient if none) rule), adapted to the v3 schema:

  - v2 posted directly to a REST endpoint that assigned Bead.type from a
    caller-supplied string; v3's create_bead MCP tool takes the same {type,
    timestamp, author, parents, content} shape (see
    internal/mcpserver/tools_write.go's createBeadIn) but always re-derives
    antigens server-side via antigen.Extract, so this module never sets
    antigens (per the task's explicit instruction and R8.5's "engine を直接
    import しない" — antigen extraction logic is core, not bench, code).
  - v2 used the caller's own in-memory id_map (FHIR id/fullUrl -> Bead hash)
    built up while ingesting one bundle; this module returns a `parent_ref`
    (the FHIR id/fullUrl string of the intended parent) per resource, and
    ingest.py resolves that to an actual Bead ID using the id_map it
    maintains while walking resources in timestamp order — the same
    two-phase structure v2 used, just split across two modules for
    testability (this module has no I/O and no MCP dependency).
  - v2 never encountered a `medicationReference`-shaped MedicationRequest
    (its sample bundle only had `medicationCodeableConcept`); this module
    extends v2's semantics with one reviewer-mandated addition (not a v2
    behavior): plan_resource_bead inline-synthesizes
    `medicationCodeableConcept` from a resolved `medicationReference` (see
    _content_with_resolved_medication) so antigen.Extract's coding[] walk
    still finds the rxnorm code — the code lives on a separately-excluded
    `Medication` side-table resource otherwise, and Extract never sees
    across-Bead content. This still never sets antigens directly (Extract
    remains the sole antigen authority, server-side); it only ensures the
    coding a correctly-shaped MedicationRequest would have carried is
    present in this Bead's own content for Extract to find.
  - U6 (specs/U6_clinical_note.md) adds a second bead_type family:
    DocumentReference -> `clinical_note` (not `fhir_documentreference`), via
    plan_clinical_note_bead. content is base64-decoded raw_text plus a few
    string-only source-metadata fields; it never contains the source
    base64 blob or a raw coding[] structure (see plan_clinical_note_bead's
    own doc comment for why). Parent is `context.encounter[0].reference`
    (nested — DocumentReference has no top-level `encounter` field at all)
    with a Patient-root fallback, same shape as every other type's edge
    rule. Only `status=="current"` DocumentReferences ever reach this
    function (fhir.clinical_resources drops the rest — see its own doc
    comment for the superseded-note rationale).
"""

from __future__ import annotations

import base64
import binascii
from dataclasses import dataclass
from typing import Any

from bench.ingest.fhir import (
    FhirResource,
    resolve_medication_code,
    resource_encounter_reference,
    resource_encounter_reference_multiplicity_warning,
    resource_timestamp,
)

# Fixed author identifier for every Bead this ingest tool creates, so
# ground-truth manifests and audits can always tell a Synthea-ingested Bead
# apart from one created interactively (e.g. via Claude Desktop) — see the
# task's "author は固定文字列".
INGEST_AUTHOR = "synthea-ingest-v1"

PATIENT_REGISTRATION_TYPE = "patient_registration"

# U6 (specs/U6_clinical_note.md): DocumentReference is ingested as this Bead
# type, NOT `fhir_documentreference` (the generic f"fhir_{type.lower()}"
# pattern every other included resource type uses) — clinical_note is a
# distinct, dedicated Bead type per specs/DESIGN_v3.1_draft.md's "新しい Bead
# 型" section.
CLINICAL_NOTE_TYPE = "clinical_note"

# Default language per specs/U6_clinical_note.md's content shape when the
# source attachment carries no `language` field of its own.
_DEFAULT_NOTE_LANGUAGE = "en"


@dataclass(frozen=True)
class PlannedBead:
    """One Bead this module has decided to create, not yet ingested.

    fhir_id/full_url identify the source FHIR resource so ingest.py's
    id_map can be updated once the real Bead ID comes back from create_bead;
    parent_ref is the *FHIR* id/fullUrl of the intended parent (resolved to
    a real Bead ID by ingest.py's id_map, never computed here) or None for
    the Patient root Bead. warning is an optional non-fatal note (e.g. U6's
    "context.encounter[] had >1 entry, used only the first") that
    patient.py surfaces the same way it already surfaces parent_fallback
    warnings — None for the overwhelming majority of Beads that have
    nothing to warn about.
    """

    fhir_type: str
    fhir_id: str
    full_url: str | None
    bead_type: str
    timestamp: str
    content: dict[str, Any]
    parent_ref: str | None
    warning: str | None = None


def _patient_name(patient: dict[str, Any]) -> str:
    """Human-readable name, per v2's import_fhir.py `name_text` construction.

    Synthea's Patient.name[0] uses FHIR R4's plural `given: [str, ...]` /
    singular `family: str` shape (unlike v2's sample fixture, which used an
    older/looser `family: [str]` shape) — both are handled defensively.
    """
    names = patient.get("name") or []
    if not names:
        return "Unknown"
    entry = names[0]
    given = entry.get("given") or [""]
    family = entry.get("family", "")
    if isinstance(family, list):
        family = family[0] if family else ""
    return f"{given[0]} {family}".strip() or "Unknown"


def plan_patient_root(patient_entry: dict[str, Any]) -> PlannedBead:
    """The root patient_registration Bead for one Bundle's Patient entry.

    Per v2's import_fhir.py: timestamp = birthDate (v2's own "生年月日をタイム
    スタンプ代わりに" choice — patients have no separate "registration
    event" timestamp in Synthea data, and birthDate is the one date every
    Patient resource is guaranteed to carry).
    """
    patient = patient_entry["resource"]
    fhir_id = patient.get("id", "")
    full_url = patient_entry.get("fullUrl")
    content = {
        # Full resource retained first (not just the v2 subset) so this
        # Bead's content carries every field antigen.Extract might
        # reasonably want from a Patient resource, and so ground-truth
        # review can inspect the complete source record from the Bead
        # alone. The curated fields below are spread last so they always
        # win over the raw resource's own `name`/`gender` shape (v2's
        # flattened `name_text` string, not FHIR's name[] array).
        **patient,
        "fhir_id": fhir_id,
        "name": _patient_name(patient),
        "gender": patient.get("gender"),
    }
    timestamp = patient.get("birthDate") or "1900-01-01"
    return PlannedBead(
        fhir_type="Patient",
        fhir_id=fhir_id,
        full_url=full_url,
        bead_type=PATIENT_REGISTRATION_TYPE,
        timestamp=timestamp,
        content=content,
        parent_ref=None,
    )


def _content_with_resolved_medication(
    resource: FhirResource, medications_by_ref: dict[str, dict[str, Any]] | None
) -> dict[str, Any]:
    """resource.data, with `medicationCodeableConcept` inline-synthesized
    from a resolved `medicationReference` if resource is a MedicationRequest
    that uses one (reviewer-mandated fix: "medicationReference 形式で
    rxnorm antigen が死ぬ" — ~29.5% of MedicationRequest resources in
    ~/medbeads-synthea/output/fhir/ carry only `medicationReference`, never
    `medicationCodeableConcept`, so antigen.Extract's generic coding[] walk
    (internal/engine/antigen/extract.go's collectCodings) never sees an
    rxnorm coding for these — the rxnorm code lives only on the separately-
    excluded `Medication` resource's own `code.coding[]`).

    Rules (lead decision, verbatim):
      - Only applies to MedicationRequest; every other resource type's
        content passes through unchanged.
      - If `medicationCodeableConcept` is already present, this function
        does not touch it (the ~70% of MedicationRequest resources that
        already inline their code are left alone).
      - The synthesized value is exactly the referenced Medication's `code`
        CodeableConcept (system/code/display/text) — nothing invented.
      - The original `medicationReference` field is preserved in the
        output content (provenance: a reviewer or later pipeline stage can
        still see this Bead's code was resolved, not natively inline).
      - Deterministic: resolution is a pure same-Bundle id_map lookup (see
        fhir.resolve_medication_code) with no wall-clock or I/O dependency,
        so re-ingesting the same Bundle always synthesizes the identical
        content, which is required for content-hash Bead-ID determinism.
      - If medications_by_ref is None (caller has no Bundle-wide index
        available — e.g. a unit test constructing a bare FhirResource) or
        the reference does not resolve, content passes through unchanged
        (no invented data, no crash).
    """
    if resource.resource_type != "MedicationRequest":
        return resource.data
    if "medicationCodeableConcept" in resource.data:
        return resource.data
    if medications_by_ref is None:
        return resource.data

    resolved_code = resolve_medication_code(resource.data, medications_by_ref)
    if resolved_code is None:
        return resource.data

    return {**resource.data, "medicationCodeableConcept": resolved_code}


def _decode_attachment_text(attachment: dict[str, Any]) -> str | None:
    """The base64-decoded, UTF-8 text of a DocumentReference
    content[].attachment.data field, or None if it is absent/not a string/
    not valid base64+UTF-8 (a malformed attachment is skipped by the caller,
    never crashes the whole ingest)."""
    data = attachment.get("data")
    if not isinstance(data, str) or not data:
        return None
    try:
        return base64.b64decode(data).decode("utf-8")
    except (binascii.Error, ValueError, UnicodeDecodeError):
        return None


def plan_clinical_note_bead(resource: FhirResource, patient_ref: str) -> PlannedBead:
    """One `status=="current"` DocumentReference -> a clinical_note
    PlannedBead (U6, specs/U6_clinical_note.md). Callers must have already
    filtered out status != "current" resources (fhir.clinical_resources
    does this) — this function does not re-check status itself, since by
    the time a DocumentReference reaches here it is assumed already
    admitted.

    content is built from scratch (NOT resource.data spread in, unlike every
    other plan_*_bead path) per the two data-reviewer findings that gated
    this unit's GO:
      1. content[0].attachment.data (the raw base64) is never put in
         content — only its *decoded* raw_text is. Putting the base64 blob
         itself in content would pollute collectStrings-based FTS/search
         with a huge non-human-readable string.
      2. type.coding[] (the LOINC document-type coding) is never put in
         content as a coding[] structure — only note_type_code, a plain
         string, is. A raw coding[] here would make antigen.Extract's
         generic coding[] walk mis-tag the *document type itself* (e.g.
         "loinc:34117-2" for "History and physical note") as if it were a
         clinical finding, contradicting U6's untagged-by-default decision
         for note bodies.

    Returns a Bead with content.raw_text == "" (not None, and not a missing
    key) if content[0].attachment.data was absent or failed to decode —
    still ingested (a clinical_note with no readable text is still evidence
    a note existed), never raises.
    """
    fhir_id = resource.fhir_id
    full_url = resource.full_url
    timestamp = resource_timestamp(resource)

    raw_text = ""
    language = _DEFAULT_NOTE_LANGUAGE
    content_entries = resource.data.get("content")
    if isinstance(content_entries, list) and content_entries:
        first_entry = content_entries[0]
        if isinstance(first_entry, dict):
            attachment = first_entry.get("attachment")
            if isinstance(attachment, dict):
                decoded = _decode_attachment_text(attachment)
                if decoded is not None:
                    raw_text = decoded
                attachment_language = attachment.get("language")
                if isinstance(attachment_language, str) and attachment_language:
                    language = attachment_language

    note_type_code: str | None = None
    type_concept = resource.data.get("type")
    if isinstance(type_concept, dict):
        codings = type_concept.get("coding")
        if isinstance(codings, list) and codings:
            first_coding = codings[0]
            if isinstance(first_coding, dict):
                code = first_coding.get("code")
                if isinstance(code, str) and code:
                    note_type_code = code

    content: dict[str, Any] = {
        "raw_text": raw_text,
        "source_system": "synthea",
        "source_document_id": fhir_id,
        "language": language,
        "status": resource.data.get("status", ""),
    }
    if note_type_code is not None:
        content["note_type_code"] = note_type_code

    encounter_ref, from_nested = resource_encounter_reference(resource)
    parent_ref = encounter_ref if encounter_ref else patient_ref
    warning = resource_encounter_reference_multiplicity_warning(resource) if from_nested else None

    return PlannedBead(
        fhir_type=resource.resource_type,
        fhir_id=fhir_id,
        full_url=full_url,
        bead_type=CLINICAL_NOTE_TYPE,
        timestamp=timestamp,
        content=content,
        parent_ref=parent_ref,
        warning=warning,
    )


def plan_resource_bead(
    resource: FhirResource,
    patient_ref: str,
    medications_by_ref: dict[str, dict[str, Any]] | None = None,
) -> PlannedBead:
    """One non-Patient clinical resource -> PlannedBead, per v2's edge rule.

    Edge rule (v2 import_fhir.py, preserved verbatim):
      - Encounter: parent is always the Patient root (an Encounter never
        nests under another Encounter in v2 or in this slice).
      - Every other included type (MedicationRequest explicitly, and by the
        same generic `elif "encounter" in res` branch: Observation,
        DiagnosticReport, Procedure — Synthea populates `encounter` on all
        of these): parent is the resource's own Encounter if resolvable,
        else the Patient root.
      - Condition, Immunization, ImagingStudy: VERIFIED (60-bundle sample)
        every Condition/Immunization/ImagingStudy resource in this dataset
        carries an `encounter` reference, so the same generic branch
        applies and always resolves to an Encounter parent in practice.
      - AllergyIntolerance: VERIFIED (60-bundle sample) never carries an
        `encounter` reference (only `patient`), so it always falls back to
        the Patient root — the same generic branch handles this without a
        special case, since "fall back to Patient root when absent" is
        exactly this situation.
      - DocumentReference: delegated entirely to plan_clinical_note_bead
        (U6) — a distinct Bead type/content shape, not the generic
        f"fhir_{type.lower()}" + resource.data-spread rule below, so it is
        branched out before any of that generic logic runs.

    medications_by_ref (fhir.index_medications_by_ref(bundle), or None) is
    consulted only for MedicationRequest resources to inline-synthesize
    medicationCodeableConcept from medicationReference — see
    _content_with_resolved_medication's doc comment for the full rule.
    """
    if resource.resource_type == "DocumentReference":
        return plan_clinical_note_bead(resource, patient_ref)

    fhir_id = resource.fhir_id
    full_url = resource.full_url
    timestamp = resource_timestamp(resource)
    bead_type = f"fhir_{resource.resource_type.lower()}"

    if resource.resource_type == "Encounter":
        parent_ref: str | None = patient_ref
    else:
        encounter_ref, _from_nested = resource_encounter_reference(resource)
        parent_ref = encounter_ref if encounter_ref else patient_ref

    content = _content_with_resolved_medication(resource, medications_by_ref)

    return PlannedBead(
        fhir_type=resource.resource_type,
        fhir_id=fhir_id,
        full_url=full_url,
        bead_type=bead_type,
        timestamp=timestamp,
        content=content,
        parent_ref=parent_ref,
    )


def sort_key(resource: FhirResource) -> tuple[str, str]:
    """Deterministic sort key: (timestamp, fhir_id).

    Sorting by timestamp alone can tie (Synthea often stamps several same-
    visit resources with an identical instant), so fhir_id is the tie-
    breaker — required for "同一入力 -> 同一 Bead ID 集合" determinism, since
    Python's sort is stable but the *input* iteration order over
    bundle["entry"] is already deterministic (list order in the JSON file);
    the explicit fhir_id tiebreak makes the *documented* contract
    (timestamp, resource id) hold regardless of a Bundle's entry order.
    """
    return (resource_timestamp(resource), resource.fhir_id)
