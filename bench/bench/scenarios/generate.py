"""Deterministic scenario generation: one patient's (source FHIR Bundle,
PatientManifest) pair -> 0..N Scenario objects per category template
(R8.1's "ingest 時に ground-truth(正解根拠 Bead 集合)を決定論的に同時生成" —
generation here happens from the *already-ingested* manifest + the original
Bundle bytes, not during ingest itself, since bench.scenarios is a separate
CLI stage per the lead's spec; the determinism guarantee is unaffected
either way, since both inputs are themselves fixed, already-written files).

Four pilot categories (lead spec, each a distinct reasoning_type):

  1. medication_lookup: "患者の condition X に対して処方された薬は?"
     answer = the MedicationRequest's display name, evidence = the Bead(s)
     for that MedicationRequest (+ the Condition it treats, so an agent/
     judge can verify the *reason* the medication was prescribed, not just
     recite a drug name with no link back to the asked-about condition).
     Ground truth source: MedicationRequest.reasonReference/reasonCode ->
     Condition (Synthea's own encoded link; see this module's docstring
     note on reasonReference coverage — VERIFIED 200-file sample: ~72% of
     MedicationRequest resources carry reasonReference).

  2. temporal_order: "X と Y はどちらが先か?" answer = which of two same-
     patient clinical events (drawn from Condition/Procedure/
     MedicationRequest/Observation, chosen for a real timestamp gap) has
     the earlier timestamp; evidence = both Beads.

  3. encounter_context: "encounter E で行われた処置/検査は?" evidence = every
     Bead whose manifest row's patient_root matches and whose *source FHIR
     resource* carries an `encounter` reference resolving to E (Procedure/
     Observation/DiagnosticReport — the FHIR types that structurally carry
     an encounter link, matching bench.ingest.beads' own "everything but
     Encounter parents to its own Encounter" edge rule, so this scenario's
     evidence set lines up with what a correct DAG traversal from E's own
     Bead should actually surface as descendants).

  4. medication_interaction_surface: "患者の薬剤リスク(出血等)に関連する処方
     は?" evidence = every MedicationRequest Bead sharing a risk: antigen
     (via antigen_dict.risk_antigens_for_rxnorm on each MedicationRequest's
     resolved rxnorm code) with at least one other MedicationRequest in the
     same patient. (Historical note: this category was originally designed
     to show up most clearly in a dag_full-vs-dag_nosib arm comparison, via
     APC's sibling_link mechanism over this same risk:/organ: antigen
     family. U5a removed sibling_link/APC entirely and U6 consolidated
     dag_full/dag_nosib into a single `dag` arm — see bench.retrieval.dag's
     docstring — so this category's evidence set is now scored the same as
     any other category's, not via a since-removed sibling-link path.)

Every template is a pure function of (bundle dict, PatientManifest) — same
bytes in, same Scenario list out (this module's own determinism contract;
see tests/scenarios/test_determinism.py for the enforced ×2 comparison).
"""

from __future__ import annotations

import hashlib
from pathlib import Path
from typing import Any

from bench.ingest.fhir import clinical_resources, load_bundle, resolve_medication_code
from bench.scenarios.antigen_dict import risk_antigens_for_rxnorm
from bench.scenarios.manifest import ManifestEntry, PatientManifest, group_by_patient, load_manifest
from bench.scenarios.model import Scenario

CATEGORY_MEDICATION_LOOKUP = "medication_lookup"
CATEGORY_TEMPORAL_ORDER = "temporal_order"
CATEGORY_ENCOUNTER_CONTEXT = "encounter_context"
CATEGORY_MEDICATION_INTERACTION_SURFACE = "medication_interaction_surface"

_REASONING_TYPE_BY_CATEGORY = {
    CATEGORY_MEDICATION_LOOKUP: "lookup",
    CATEGORY_TEMPORAL_ORDER: "temporal_comparison",
    CATEGORY_ENCOUNTER_CONTEXT: "aggregation",
    CATEGORY_MEDICATION_INTERACTION_SURFACE: "antigen_surface",
}


def _scenario_id(patient_id: str, category: str, discriminator: str) -> str:
    """A short, stable, content-derived scenario ID: sha256 of
    (patient_id, category, discriminator) truncated to 16 hex chars — long
    enough to be practically collision-free at pilot scale (a few hundred
    scenarios), short enough to stay readable in a YAML file and a run log.
    discriminator is category-specific (see each _generate_* function) and
    is itself a deterministic function of the source data, never a running
    counter (which would depend on dict/set iteration order elsewhere in
    this module and break the determinism contract).
    """
    digest = hashlib.sha256(f"{patient_id}|{category}|{discriminator}".encode("utf-8")).hexdigest()
    return digest[:16]


def _codeable_concept_text(concept: Any) -> str | None:
    if not isinstance(concept, dict):
        return None
    text = concept.get("text")
    if isinstance(text, str) and text.strip():
        return text.strip()
    for coding in concept.get("coding") or []:
        if not isinstance(coding, dict):
            continue
        display = coding.get("display")
        if isinstance(display, str) and display.strip():
            return display.strip()
    return None


def _medication_display_name(resource: dict[str, Any], medications_by_ref: dict[str, dict[str, Any]]) -> str | None:
    """The human-readable drug name for a MedicationRequest resource, per
    the same medicationCodeableConcept-or-medicationReference duality
    bench.ingest.beads._content_with_resolved_medication resolves at ingest
    time (mirrored narrowly here — this module never imports that private
    function, to keep scenario generation's own dependency surface
    independent of ingest's internals, but the resolution rule is
    intentionally identical: display text should read the same way whether
    it comes from an already-ingested Bead's content or straight from the
    source Bundle)."""
    concept = resource.get("medicationCodeableConcept")
    text = _codeable_concept_text(concept)
    if text:
        return text
    ref = resource.get("medicationReference")
    if isinstance(ref, dict):
        reference = ref.get("reference")
        if isinstance(reference, str):
            medication = medications_by_ref.get(reference)
            if isinstance(medication, dict):
                return _codeable_concept_text(medication.get("code"))
    return None


def _medication_rxnorm_code(resource: dict[str, Any], medications_by_ref: dict[str, dict[str, Any]]) -> str | None:
    """The rxnorm code for a MedicationRequest resource (inline or resolved
    via medicationReference -> Medication.code), for antigen_dict lookups —
    mirrors bench.ingest.beads._content_with_resolved_medication's
    resolution path, narrowed to just the rxnorm coding[] entry.
    """
    concept = resource.get("medicationCodeableConcept")
    if not isinstance(concept, dict):
        resolved = resolve_medication_code(resource, medications_by_ref)
        concept = resolved if isinstance(resolved, dict) else None
    if not isinstance(concept, dict):
        return None
    for coding in concept.get("coding") or []:
        if not isinstance(coding, dict):
            continue
        if coding.get("system") == "http://www.nlm.nih.gov/research/umls/rxnorm":
            code = coding.get("code")
            if isinstance(code, str) and code:
                return code
    return None


def _resource_reason_reference_ids(resource: dict[str, Any]) -> list[str]:
    """Every fullUrl/id reasonReference[] on resource points at, in array
    order (MedicationRequest.reasonReference is a list of Reference
    objects — Synthea always uses the urn:uuid: fullUrl form, per this
    module's docstring's VERIFIED sample)."""
    out: list[str] = []
    for ref in resource.get("reasonReference") or []:
        if isinstance(ref, dict):
            reference = ref.get("reference")
            if isinstance(reference, str) and reference:
                out.append(reference)
    return out


def _generate_medication_lookup(
    bundle: dict[str, Any], pm: PatientManifest, by_fhir_id: dict[str, ManifestEntry]
) -> list[Scenario]:
    resources = clinical_resources(bundle)
    medications_by_ref = _index_medications_by_ref(bundle)

    # full_url -> resource, for resolving reasonReference targets (Condition
    # entries) regardless of resource-type ordering in the Bundle.
    by_full_url: dict[str, dict[str, Any]] = {}
    for entry in bundle.get("entry", []):
        full_url = entry.get("fullUrl")
        if isinstance(full_url, str):
            by_full_url[full_url] = entry.get("resource", {})

    out: list[Scenario] = []
    for resource in resources:
        if resource.resource_type != "MedicationRequest":
            continue
        med_row = by_fhir_id.get(resource.fhir_id)
        if med_row is None:
            continue
        drug_name = _medication_display_name(resource.data, medications_by_ref)
        if not drug_name:
            continue

        reason_refs = _resource_reason_reference_ids(resource.data)
        condition_row: ManifestEntry | None = None
        condition_name: str | None = None
        for ref in reason_refs:
            reason_resource = by_full_url.get(ref)
            if not isinstance(reason_resource, dict) or reason_resource.get("resourceType") != "Condition":
                continue
            condition_fhir_id = reason_resource.get("id")
            if not isinstance(condition_fhir_id, str):
                continue
            row = by_fhir_id.get(condition_fhir_id)
            if row is None:
                continue
            condition_row = row
            condition_name = _codeable_concept_text(reason_resource.get("code"))
            break
        if condition_row is None or not condition_name:
            continue

        question = f"患者の condition '{condition_name}' に対して処方された薬は?"
        answer = drug_name
        evidence = [med_row.bead_id, condition_row.bead_id]
        out.append(
            Scenario(
                scenario_id=_scenario_id(pm.patient_root, CATEGORY_MEDICATION_LOOKUP, resource.fhir_id),
                patient_id=pm.patient_root,
                question=question,
                answer=answer,
                evidence_bead_ids=evidence,
                category=CATEGORY_MEDICATION_LOOKUP,
                reasoning_type=_REASONING_TYPE_BY_CATEGORY[CATEGORY_MEDICATION_LOOKUP],
            )
        )
    return out


# Resource types temporal_order draws its two-event pairs from — clinical
# events with a single, comparable point-in-time timestamp (Encounter is
# deliberately excluded: it is the *container* most of these events happen
# inside, so an Encounter-vs-its-own-child pairing would be a trivially
# "always true" comparison, not an interesting temporal question).
_TEMPORAL_SOURCE_TYPES = ("Condition", "Procedure", "MedicationRequest", "Observation")


def _generate_temporal_order(
    bundle: dict[str, Any], pm: PatientManifest, by_fhir_id: dict[str, ManifestEntry]
) -> list[Scenario]:
    resources = [r for r in clinical_resources(bundle) if r.resource_type in _TEMPORAL_SOURCE_TYPES]

    # (fhir_id, display_name, timestamp, ManifestEntry) in Bundle entry
    # order — display_name reused from queries.py's own resource-name
    # convention would create a cross-package dependency for one helper;
    # inlined narrowly here instead (medicationCodeableConcept/code.text-or-
    # coding[0].display, the same rule _codeable_concept_text already
    # implements).
    events: list[tuple[str, str, str]] = []  # (fhir_id, name, timestamp)
    for resource in resources:
        row = by_fhir_id.get(resource.fhir_id)
        if row is None or not row.timestamp:
            continue
        name = _event_display_name(resource.data)
        if not name:
            continue
        events.append((resource.fhir_id, name, row.timestamp))

    # Deterministic pairing: sort events by (timestamp, fhir_id) — same tie-
    # break convention as bench.ingest.beads.sort_key — then pair adjacent
    # *distinct-timestamp* events (i, i+1) so every pair has an unambiguous
    # "earlier" answer (a same-timestamp pair has no single correct
    # ordering, so those are skipped rather than guessed).
    events.sort(key=lambda e: (e[2], e[0]))

    out: list[Scenario] = []
    for i in range(len(events) - 1):
        earlier = events[i]
        later = events[i + 1]
        if earlier[2] == later[2]:
            continue  # tie: no unambiguous "which is first" answer
        earlier_row = by_fhir_id[earlier[0]]
        later_row = by_fhir_id[later[0]]
        question = f"'{earlier[1]}' と '{later[1]}' はどちらが先か?"
        answer = earlier[1]
        out.append(
            Scenario(
                scenario_id=_scenario_id(
                    pm.patient_root, CATEGORY_TEMPORAL_ORDER, f"{earlier[0]}|{later[0]}"
                ),
                patient_id=pm.patient_root,
                question=question,
                answer=answer,
                evidence_bead_ids=[earlier_row.bead_id, later_row.bead_id],
                category=CATEGORY_TEMPORAL_ORDER,
                reasoning_type=_REASONING_TYPE_BY_CATEGORY[CATEGORY_TEMPORAL_ORDER],
            )
        )
    return out


def _event_display_name(resource: dict[str, Any]) -> str | None:
    rtype = resource.get("resourceType")
    if rtype == "MedicationRequest":
        text = _codeable_concept_text(resource.get("medicationCodeableConcept"))
        if text:
            return text
        # medicationReference-only requests have no *inline* display name to
        # show in a question string without a second resource hop this
        # function does not have (no medications_by_ref here) — skipped by
        # the caller instead (name is None).
        return None
    if rtype in ("Condition", "Procedure", "Observation"):
        return _codeable_concept_text(resource.get("code"))
    return None


def _generate_encounter_context(
    bundle: dict[str, Any], pm: PatientManifest, by_fhir_id: dict[str, ManifestEntry]
) -> list[Scenario]:
    resources = clinical_resources(bundle)
    encounters = [r for r in resources if r.resource_type == "Encounter"]

    # full_url -> fhir_id, so a child resource's `encounter.reference`
    # (always the urn:uuid: fullUrl form in this dataset) can be matched
    # back to the Encounter's own bare fhir_id key in by_fhir_id.
    encounter_full_url_to_fhir_id: dict[str, str] = {}
    for entry in bundle.get("entry", []):
        resource = entry.get("resource", {})
        if resource.get("resourceType") == "Encounter" and isinstance(entry.get("fullUrl"), str):
            encounter_full_url_to_fhir_id[entry["fullUrl"]] = resource.get("id", "")

    children_by_encounter_fhir_id: dict[str, list[tuple[str, ManifestEntry]]] = {}
    for resource in resources:
        if resource.resource_type == "Encounter":
            continue
        encounter = resource.data.get("encounter")
        encounter_ref = encounter.get("reference") if isinstance(encounter, dict) else None
        if not isinstance(encounter_ref, str):
            continue
        encounter_fhir_id = encounter_full_url_to_fhir_id.get(encounter_ref)
        if not encounter_fhir_id:
            continue
        row = by_fhir_id.get(resource.fhir_id)
        if row is None:
            continue
        name = _event_display_name(resource.data) or resource.resource_type
        children_by_encounter_fhir_id.setdefault(encounter_fhir_id, []).append((name, row))

    out: list[Scenario] = []
    for encounter in encounters:
        encounter_row = by_fhir_id.get(encounter.fhir_id)
        if encounter_row is None:
            continue
        children = children_by_encounter_fhir_id.get(encounter.fhir_id, [])
        if not children:
            continue
        # Deterministic order: by (timestamp, bead_id) of each child row,
        # matching bench.ingest.beads.sort_key's own tie-break shape.
        children.sort(key=lambda c: (c[1].timestamp, c[1].bead_id))

        encounter_name = _codeable_concept_text(_encounter_type_concept(encounter.data)) or "encounter"
        question = f"encounter '{encounter_name}' ({encounter.fhir_id}) で行われた処置/検査は?"
        answer = ", ".join(name for name, _ in children)
        evidence = [encounter_row.bead_id] + [row.bead_id for _, row in children]
        out.append(
            Scenario(
                scenario_id=_scenario_id(pm.patient_root, CATEGORY_ENCOUNTER_CONTEXT, encounter.fhir_id),
                patient_id=pm.patient_root,
                question=question,
                answer=answer,
                evidence_bead_ids=evidence,
                category=CATEGORY_ENCOUNTER_CONTEXT,
                reasoning_type=_REASONING_TYPE_BY_CATEGORY[CATEGORY_ENCOUNTER_CONTEXT],
            )
        )
    return out


def _encounter_type_concept(encounter_resource: dict[str, Any]) -> dict[str, Any] | None:
    types = encounter_resource.get("type")
    if isinstance(types, list) and types:
        first = types[0]
        if isinstance(first, dict):
            return first
    return None


def _generate_medication_interaction_surface(
    bundle: dict[str, Any], pm: PatientManifest, by_fhir_id: dict[str, ManifestEntry]
) -> list[Scenario]:
    resources = clinical_resources(bundle)
    medications_by_ref = _index_medications_by_ref(bundle)

    # antigen -> [(fhir_id, drug_name, ManifestEntry)] in Bundle entry
    # order, restricted to MedicationRequest resources whose resolved
    # rxnorm code carries at least one risk: antigen.
    by_risk_antigen: dict[str, list[tuple[str, str, ManifestEntry]]] = {}
    for resource in resources:
        if resource.resource_type != "MedicationRequest":
            continue
        row = by_fhir_id.get(resource.fhir_id)
        if row is None:
            continue
        code = _medication_rxnorm_code(resource.data, medications_by_ref)
        if not code:
            continue
        risks = risk_antigens_for_rxnorm(code)
        if not risks:
            continue
        drug_name = _medication_display_name(resource.data, medications_by_ref) or resource.fhir_id
        for risk in risks:
            by_risk_antigen.setdefault(risk, []).append((resource.fhir_id, drug_name, row))

    out: list[Scenario] = []
    for risk in sorted(by_risk_antigen):
        members = by_risk_antigen[risk]
        if len(members) < 2:
            continue  # need at least 2 MedicationRequests sharing this risk to form a "surface"
        # Deterministic order: by fhir_id (stable regardless of dict
        # insertion order, which is itself already Bundle-entry-order-
        # derived, but fhir_id sort keeps this independent of that too).
        members = sorted(members, key=lambda m: m[0])
        drug_names = ", ".join(name for _, name, _ in members)
        question = f"患者の薬剤リスク('{risk}')に関連する処方は?"
        answer = drug_names
        evidence = [row.bead_id for _, _, row in members]
        out.append(
            Scenario(
                scenario_id=_scenario_id(pm.patient_root, CATEGORY_MEDICATION_INTERACTION_SURFACE, risk),
                patient_id=pm.patient_root,
                question=question,
                answer=answer,
                evidence_bead_ids=evidence,
                category=CATEGORY_MEDICATION_INTERACTION_SURFACE,
                reasoning_type=_REASONING_TYPE_BY_CATEGORY[CATEGORY_MEDICATION_INTERACTION_SURFACE],
            )
        )
    return out


def _index_medications_by_ref(bundle: dict[str, Any]) -> dict[str, dict[str, Any]]:
    """Local re-derivation of bench.ingest.fhir.index_medications_by_ref
    (that function itself is reused directly, not re-implemented — this
    wrapper exists only so every _generate_* function above can call one
    name without repeating the import at each call site)."""
    from bench.ingest.fhir import index_medications_by_ref

    return index_medications_by_ref(bundle)


def generate_scenarios_for_patient(
    bundle: dict[str, Any],
    pm: PatientManifest,
    *,
    per_patient: int | None = None,
) -> list[Scenario]:
    """Every scenario this module's four templates produce for one patient
    (bundle dict already loaded, PatientManifest already grouped — see
    generate_scenarios for the file-loading entry point), in a fixed
    category order (medication_lookup, temporal_order, encounter_context,
    medication_interaction_surface) — that order itself does not matter for
    correctness (write_scenarios_yaml sorts the final output), but keeping
    it fixed here makes per_patient truncation (if applied) deterministic
    and reviewable rather than an artifact of dict iteration order.

    per_patient, if given, caps the *total* scenario count returned for
    this patient (applied after generating every category, by
    Scenario._sort_key's ordering, so truncation is deterministic and not
    just "whichever category ran first keeps all its scenarios").
    """
    by_fhir_id = pm.by_fhir_id()

    scenarios: list[Scenario] = []
    scenarios.extend(_generate_medication_lookup(bundle, pm, by_fhir_id))
    scenarios.extend(_generate_temporal_order(bundle, pm, by_fhir_id))
    scenarios.extend(_generate_encounter_context(bundle, pm, by_fhir_id))
    scenarios.extend(_generate_medication_interaction_surface(bundle, pm, by_fhir_id))

    if per_patient is not None and len(scenarios) > per_patient:
        scenarios = sorted(scenarios, key=lambda s: (s.patient_id, s.category, s.scenario_id))[:per_patient]

    return scenarios


def _find_bundle_path(fhir_dir: Path, fhir_resource_id: str) -> Path | None:
    """Mirrors bench.perf.run._find_bundle_path exactly (same VERIFIED join
    key: Synthea's own bundle filenames embed the Patient's FHIR id as the
    trailing UUID) — duplicated rather than imported from bench.perf.run
    since that module's own docstring frames it as perf-harness-specific,
    and this scenario generator has no other dependency on bench.perf.
    """
    matches = sorted(fhir_dir.glob(f"*{fhir_resource_id}*.json"))
    return matches[0] if matches else None


def generate_scenarios(
    *,
    fhir_dir: Path,
    manifest_path: Path,
    patients: int | None = None,
    per_patient: int | None = None,
) -> list[Scenario]:
    """Top-level entry point: manifest_path + fhir_dir -> every generated
    Scenario across every patient in the manifest (or the first `patients`,
    in manifest/ingest order — deterministic, same convention as
    bench.ingest's own --limit).

    A patient whose source Bundle can no longer be found under fhir_dir
    (moved/renamed since ingest) is silently skipped, same as
    bench.perf.run.build_patient_queries's identical case — this is scenario
    *generation* from whatever data is actually available, not a
    completeness guarantee over the full manifest.
    """
    entries = load_manifest(manifest_path)
    by_patient = group_by_patient(entries)

    # Deterministic patient order: by patient_root (manifest/ingest order
    # would also be deterministic, but grouping into a dict already loses
    # that within group_by_patient — sorting here makes the ordering
    # explicit and independent of dict internals).
    patient_roots = sorted(by_patient)
    if patients is not None:
        patient_roots = patient_roots[:patients]

    out: list[Scenario] = []
    for root in patient_roots:
        pm = by_patient[root]
        try:
            patient_row = pm.patient_root_entry()
        except ValueError:
            continue
        bundle_path = _find_bundle_path(fhir_dir, patient_row.fhir_resource_id)
        if bundle_path is None:
            continue
        bundle = load_bundle(bundle_path)
        out.extend(generate_scenarios_for_patient(bundle, pm, per_patient=per_patient))

    return out
