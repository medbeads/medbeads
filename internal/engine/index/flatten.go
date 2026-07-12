package index

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/medbeads/medbeads/internal/engine/bead"
)

// Flattener turns a Bead into the two derived text fields index.db stores
// alongside it: search_text (indexed by beads_fts, R3.3) and summary (the
// one-line, machine-generated L1 token-budget text, R3.3 / DESIGN §8).
type Flattener interface {
	Flatten(b bead.Bead) (searchText, summary string)
}

// DefaultFlattener is the project's one production Flattener. Its summary
// output is type-aware for clinical_note (see flattenClinicalNote) and for
// the FHIR resource types listed in fhirSummaryFields (see
// flattenFHIRSummary — "Body temperature 37.8 Cel", "amLODIPine 2.5 MG Oral
// Tablet", not a raw code or timestamp); every other type, and any of the
// above whose Content lacks the field the type-aware path needs, falls back
// to a fully generic search_text/summary derivation: it recursively
// concatenates every string value found in b.Content (map/slice values
// included) into search_text, and derives summary as b.Type plus the first
// such string, truncated. This generic fallback is what makes Flatten total
// (defined and non-empty for every Bead, never a panic) regardless of how
// many FHIR resource shapes it does or does not know about.
type DefaultFlattener struct{}

// maxSummaryLen bounds DefaultFlattener's generated summary length so a
// single huge Content string value can't blow up beads.summary (which is
// meant to be an L1, ~40-token budget field per DESIGN §8).
const maxSummaryLen = 200

// Flatten implements Flattener.
func (DefaultFlattener) Flatten(b bead.Bead) (searchText, summary string) {
	if b.Type == clinicalNoteType {
		if searchText, summary, ok := flattenClinicalNote(b); ok {
			return searchText, summary
		}
		// content.raw_text absent or not a string: fall through to the
		// generic behavior below rather than producing an empty index entry.
	}

	if fhirSummaryFields[b.Type] {
		if summary, ok := flattenFHIRSummary(b); ok {
			// Only summary is FHIR-aware; search_text keeps using the
			// generic collectStrings walk below (it already surfaces every
			// coding/display/text string in Content, which is what full-text
			// search wants — FHIR-aware summary is strictly about producing
			// a short, human-meaningful L1 line, a different goal from
			// search recall).
			var parts []string
			collectStrings(b.Content, &parts)
			sort.Strings(parts)
			return strings.Join(parts, " "), summary
		}
		// No usable clinical field found (e.g. code/coding entirely absent):
		// fall through to the fully generic behavior below rather than
		// producing an empty or panicking summary.
	}

	var parts []string
	collectStrings(b.Content, &parts)
	// collectStrings walks a map, whose key iteration order Go randomizes;
	// sort collected values so search_text/summary are deterministic across
	// runs (important for Reindex-vs-manual-IndexBead byte-for-byte
	// comparison in tests).
	sort.Strings(parts)

	searchText = strings.Join(parts, " ")

	summary = b.Type
	if len(parts) > 0 {
		first := parts[0]
		if len(first) > maxSummaryLen {
			first = first[:maxSummaryLen]
		}
		summary = fmt.Sprintf("%s: %s", b.Type, first)
	}
	return searchText, summary
}

// clinicalNoteType is the Bead.Type value for ingested Synthea
// DocumentReference notes (specs/U6_clinical_note.md).
const clinicalNoteType = "clinical_note"

// flattenClinicalNote implements the clinical_note-specific flattening rule
// (specs/U6_clinical_note.md 合意点2): search_text is content.raw_text
// verbatim, order-preserved (no sort.Strings — unlike DefaultFlattener's
// generic path, word order in a clinical note carries meaning), and summary
// is the note's first heading line / first non-empty line, truncated to
// maxSummaryLen. It reports ok=false if b.Content["raw_text"] is missing or
// not a string, so the caller can fall back to the generic behavior instead
// of indexing an empty note.
func flattenClinicalNote(b bead.Bead) (searchText, summary string, ok bool) {
	rawText, isString := b.Content["raw_text"].(string)
	if !isString || rawText == "" {
		return "", "", false
	}

	searchText = rawText
	summary = fmt.Sprintf("%s: %s", b.Type, summaryLine(rawText))
	return searchText, summary, true
}

// summaryLine picks the note-body prefix used in a clinical_note's summary:
// the first Markdown heading line ("# ..." / "## ...", Synthea's own
// DocumentReference narrative format — VERIFIED real sample: notes open
// with a bare date line, e.g. "2025-11-05", before their first "# Chief
// Complaint" heading) if one exists, else the first non-blank line
// (leading/trailing whitespace trimmed). Preferring a heading over the
// literal first line matters here specifically because that literal first
// line is frequently just a date stamp, not human-meaningful text — a
// heading is a much more useful few-dozen-char summary of what the note
// actually is. Truncated to maxSummaryLen. Returns "" if s has no non-blank
// line at all, so the caller's summary degrades to "clinical_note: " rather
// than panicking or indexing whitespace.
func summaryLine(s string) string {
	lines := strings.Split(s, "\n")

	firstNonEmpty := ""
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if firstNonEmpty == "" {
			firstNonEmpty = trimmed
		}
		if strings.HasPrefix(trimmed, "#") {
			return truncateSummary(trimmed)
		}
	}
	return truncateSummary(firstNonEmpty)
}

// truncateSummary bounds s to maxSummaryLen bytes, matching DefaultFlattener's
// own truncation rule for its generic summary field.
func truncateSummary(s string) string {
	if len(s) > maxSummaryLen {
		return s[:maxSummaryLen]
	}
	return s
}

// --- FHIR-aware summary (R3.3 follow-up, docs/reviews/2026-07-10_scheme_critique_A_internal.md §1) ---
//
// Before this, every Bead's summary (FHIR or not) went through the fully
// generic "sort every string in Content, take the first one" path above,
// which for FHIR Beads produces near-meaningless summaries dominated by
// whatever string sorts first — usually an ISO timestamp or a bare SNOMED/
// LOINC/RxNorm code (e.g. "fhir_condition: 195967001"), never the clinical
// text ("Asthma (disorder)") that is also right there in Content. The
// functions below extract that clinical text for a known set of FHIR
// resource types (fhirSummaryFields), while leaving search_text on the
// existing generic/order-independent path — full-text search still wants
// every coding/display string indexed, not just the one chosen for summary.

// fhirSummaryFields is the set of Bead.Type values ("fhir_<resourcetype>",
// per docs/fhir_timeline_mapping.md's naming convention) flattenFHIRSummary
// knows how to summarize. Any other type (including non-FHIR types, and FHIR
// types not yet listed here) uses DefaultFlattener's fully generic fallback
// instead — adding a new resource type here requires adding a matching case
// to flattenFHIRSummary's switch, not just this set.
var fhirSummaryFields = map[string]bool{
	"fhir_observation":        true,
	"fhir_condition":          true,
	"fhir_medicationrequest":  true,
	"fhir_procedure":          true,
	"fhir_diagnosticreport":   true,
	"fhir_encounter":          true,
	"fhir_immunization":       true,
	"fhir_allergyintolerance": true,
	"fhir_imagingstudy":       true,
}

// flattenFHIRSummary derives a short, clinically meaningful summary line for
// a FHIR-typed Bead: "<label>" or "<label> <value> <unit>" when a
// valueQuantity/valueCodeableConcept is also present, prefixed with b.Type
// exactly like every other DefaultFlattener summary (e.g.
// "fhir_observation: Body temperature 37.8 Cel"). ok is false if no usable
// label could be found (missing/malformed field, wrong shape, or a resource
// type flattenFHIRSummary's switch does not (yet) special-case), so the
// caller falls back to the fully generic behavior rather than emitting an
// empty or misleading summary.
//
// Every b.Content read here goes through fhirCodeableConceptLabel/
// fhirObservationValueSuffix, never a bare ".(string)"/".(map[string]any)"
// assertion at the call site — this codebase has already been bitten once by
// assuming a
// FHIR field's shape from one corpus (demo_data) and having it differ in the
// real Synthea store (commit 80b812e: Condition.clinicalStatus is a
// CodeableConcept in real data, not the plain string demo_data happened to
// use). Every helper here tolerates a missing key, wrong type, or empty
// array by degrading to "", never panicking.
func flattenFHIRSummary(b bead.Bead) (summary string, ok bool) {
	var label, valueSuffix string

	switch b.Type {
	case "fhir_observation":
		label = fhirCodeableConceptLabel(b.Content["code"])
		valueSuffix = fhirObservationValueSuffix(b.Content)
	case "fhir_condition", "fhir_allergyintolerance":
		label = fhirCodeableConceptLabel(b.Content["code"])
	case "fhir_medicationrequest":
		label = fhirCodeableConceptLabel(b.Content["medicationCodeableConcept"])
	case "fhir_procedure", "fhir_diagnosticreport":
		label = fhirCodeableConceptLabel(b.Content["code"])
	case "fhir_encounter":
		// Encounter has no top-level "code": the resource-defining
		// CodeableConcept is "type[0]" (VERIFIED against the real store —
		// see this task's before/after report), unlike every other type
		// handled here.
		label = fhirCodeableConceptLabel(fhirFirstArrayElem(b.Content["type"]))
	case "fhir_immunization":
		label = fhirCodeableConceptLabel(b.Content["vaccineCode"])
	case "fhir_imagingstudy":
		// ImagingStudy also has no top-level "code": the closest analog is
		// "procedureCode[0]" (an array of CodeableConcept, VERIFIED against
		// the real store), not a "code.coding[]" shape.
		label = fhirCodeableConceptLabel(fhirFirstArrayElem(b.Content["procedureCode"]))
	default:
		return "", false
	}

	if label == "" {
		return "", false
	}
	if valueSuffix != "" {
		label = label + " " + valueSuffix
	}
	return fmt.Sprintf("%s: %s", b.Type, truncateSummary(label)), true
}

// fhirFirstArrayElem returns v[0] if v is a non-empty []any, else nil — used
// for FHIR fields that are themselves arrays of CodeableConcept
// (Encounter.type, ImagingStudy.procedureCode) rather than a single
// CodeableConcept object.
func fhirFirstArrayElem(v any) any {
	arr, ok := v.([]any)
	if !ok || len(arr) == 0 {
		return nil
	}
	return arr[0]
}

// fhirCodeableConceptLabel extracts the best available human-readable label
// from a FHIR CodeableConcept-shaped value v, preferring (per this task's
// requirement 2) v.text, then the first coding[].display, then the first
// coding[].code. Any other shape (missing, wrong type, empty coding, no
// display/code) returns "". v is typically b.Content["code"] or similar —
// always a map[string]any after a Pod-frame JSON round-trip, following the
// same shape convention as fhirCodeString/collectCodings (see
// projector.fhirCodeString and antigen.collectCodings's doc comments).
func fhirCodeableConceptLabel(v any) string {
	cc, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	if text, ok := cc["text"].(string); ok && text != "" {
		return text
	}

	coding, ok := cc["coding"].([]any)
	if !ok || len(coding) == 0 {
		return ""
	}
	first, ok := coding[0].(map[string]any)
	if !ok {
		return ""
	}
	if display, ok := first["display"].(string); ok && display != "" {
		return display
	}
	if code, ok := first["code"].(string); ok && code != "" {
		return code
	}
	return ""
}

// fhirObservationValueSuffix returns the "<value> <unit>" suffix an
// Observation's summary should carry when the measured value is present:
// content.valueQuantity ({"value": 37.791, "unit": "Cel"}, VERIFIED against
// the real store) formatted via fhirRoundedNumber so "37.791" prints as
// "37.791" rather than a long float artifact, or content.valueCodeableConcept
// (a finding like "Never smoked tobacco (finding)") via
// fhirCodeableConceptLabel when no valueQuantity is present. Returns "" if
// neither is present/well-formed, so the caller's summary degrades to the
// bare label rather than panicking or appending garbage.
func fhirObservationValueSuffix(content map[string]any) string {
	if vq, ok := content["valueQuantity"].(map[string]any); ok {
		numStr := ""
		if num, ok := vq["value"].(float64); ok {
			numStr = fhirRoundedNumber(num)
		}
		unit, _ := vq["unit"].(string)
		switch {
		case numStr != "" && unit != "":
			return numStr + " " + unit
		case numStr != "":
			return numStr
		}
	}
	if vcc, ok := content["valueCodeableConcept"].(map[string]any); ok {
		return fhirCodeableConceptLabel(vcc)
	}
	return ""
}

// fhirRoundedNumber formats a JSON-decoded float64 (every FHIR numeric
// value: Go's encoding/json always decodes JSON numbers into float64 for a
// map[string]any target) rounded to at most 3 decimal places, trailing zeros
// trimmed, so a value like 37.791 prints as "37.791" and a floating-point
// artifact like 37.791000000001 also prints as "37.791" — not
// "37.791000000001", the requirement's explicit "do not print
// 37.791000000001" example — while a whole number like 5 prints as "5" (not
// "5.000"). 3 decimal places comfortably covers vital-sign/lab precision in
// this corpus (temperature, weight, lab panel values) without a fourth digit
// ever mattering for a one-line summary.
func fhirRoundedNumber(v float64) string {
	const scale = 1000 // 3 decimal places
	rounded := math.Round(v*scale) / scale
	return strconv.FormatFloat(rounded, 'f', -1, 64)
}

// collectStrings recursively appends every string value reachable from v
// (map values, slice/array elements; map keys are not included since they
// are typically FHIR field names, not clinical content) into out.
func collectStrings(v any, out *[]string) {
	switch val := v.(type) {
	case string:
		if val != "" {
			*out = append(*out, val)
		}
	case map[string]any:
		for _, elem := range val {
			collectStrings(elem, out)
		}
	case []any:
		for _, elem := range val {
			collectStrings(elem, out)
		}
	default:
		// Numbers, bools, nil: not indexed as search text.
	}
}
