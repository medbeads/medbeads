package antigen

import (
	"sort"
)

// Coding system URIs recognized for direct namespace-prefix extraction
// (specs/DESIGN_v3.md §6, specs/MEDBEADS_SPECIFICATION_v2.1.md §6.5). These
// are the three systems actually present as `coding[].system` values in the
// Synthea FHIR sample corpus (FHIR_sample/*.json) alongside a handful of
// systems this package intentionally does not map (observation-category,
// v3/Race, v3/Ethnicity, identifier-type, v2/0203, v3/MaritalStatus,
// ValueSet/languages, ValueSet/organization-type, cvx, ncimeta) because they
// are not part of the R4.4 antigen taxonomy.
const (
	systemSNOMED = "http://snomed.info/sct"
	systemLOINC  = "http://loinc.org"
	systemRxNorm = "http://www.nlm.nih.gov/research/umls/rxnorm"
)

// Antigen namespace prefixes (specs/MEDBEADS_SPECIFICATION_v2.1.md §6.3).
const (
	prefixSNOMED   = "snomed:"
	prefixLOINC    = "loinc:"
	prefixRxNorm   = "rxnorm:"
	prefixTemporal = "temporal:"
)

// beadTypeTemporal maps a FHIR-ingest beadType (the "fhir_<resourcetype>"
// naming convention documented in docs/fhir_timeline_mapping.md) to the
// temporal: antigen it implies. This is deliberately the minimal FHIR-only
// subset of specs/MEDBEADS_SPECIFICATION_v2.1.md §6.5's type-based rules —
// that spec's larger EMR-CSV table (type=="surgery", "pre_op_record", ...)
// describes the pre-v3 EMR-CSV ingest path, which is out of scope for R4.4
// (FHIR coding extraction only). Extend this table only alongside a matching
// requirements-doc update, not silently.
var beadTypeTemporal = map[string]string{
	"fhir_encounter": prefixTemporal + "encounter",
}

// Extract returns the deterministic list of antigens for a Bead of the
// given beadType and content, per specs/DESIGN_v3.md §6 and
// docs/requirements.md R4.4:
//
//  1. Direct extraction: every FHIR coding object found anywhere in content
//     (content.code.coding[], content.component[].code.coding[],
//     content.category[].coding[], content.type[].coding[],
//     content.reason.coding[], content.vaccineCode.coding[],
//     content.extension[].valueCodeableConcept.coding[], and so on — see
//     collectCodings's doc comment for why this is a generic structural
//     walk rather than a fixed field-path list) whose system is a
//     recognized URI contributes "<namespace>:<code>".
//  2. Dictionary derivation: every rxnorm:<code> found in step 1 is looked
//     up in the embedded static dictionary (dictionary.json) for
//     atc:/organ:/risk: antigens.
//  3. Type-based rules: beadType is looked up in beadTypeTemporal for a
//     temporal: antigen.
//
// The result is deduplicated and sorted lexicographically (the same
// dedup+sort convention as bead.Normalize's Antigens handling), so Extract's
// output is stable regardless of map/JSON traversal order — the same
// (beadType, content) always yields the byte-identical slice. Extract never
// panics: coding-less, empty, or non-FHIR (free-text) content simply yields
// fewer (possibly zero) antigens.
//
// Extract is pure and side-effect-free (beyond the one-time package-init
// dictionary load); it does not read or write any Bead, and callers are
// responsible for calling it exactly once, before a Bead's ID is computed
// (see doc.go).
func Extract(beadType string, content map[string]any) []string {
	var out []string

	codes := collectCodings(content)
	for _, c := range codes {
		switch c.system {
		case systemSNOMED:
			out = append(out, prefixSNOMED+c.code)
		case systemLOINC:
			out = append(out, prefixLOINC+c.code)
		case systemRxNorm:
			antigen := prefixRxNorm + c.code
			out = append(out, antigen)
			out = append(out, deriveFromRxNorm(c.code)...)
		}
	}

	if t, ok := beadTypeTemporal[beadType]; ok {
		out = append(out, t)
	}

	return normalize(out)
}

// coding is one FHIR Coding object's system+code pair, as found nested
// anywhere inside a Bead's content (see collectCodings). display and other
// Coding fields are not needed for extraction and are not carried here.
type coding struct {
	system string
	code   string
}

// collectCodings walks v (content, or any value reachable from it)
// depth-first and returns every FHIR Coding entry it finds: any JSON object
// with a "coding" key whose value is an array of objects, where each
// element's "system" and "code" fields are both non-empty strings.
//
// This is a generic structural walk rather than a fixed list of field paths
// (content.code.coding[], content.component[].code.coding[], ...) because
// FHIR_sample/*.json (measured directly against this repo's sample corpus)
// shows coding[] nested under a different parent field per resource type:
// code (AllergyIntolerance, Condition, DiagnosticReport, Medication,
// Procedure, Observation, and Observation.component[].code), vaccineCode
// (Immunization), type[] (Encounter, Organization),
// category[] (CarePlan, Observation), reason (Encounter),
// activity[].detail.code (CarePlan), and extension[].valueCodeableConcept
// (MedicationRequest, Patient, Procedure). A generic walk finds all of
// these in one pass, including resource types not yet enumerated by hand,
// without requiring this package to special-case every FHIR resource
// shape; it also means the same walk keeps working for any new resource
// type added to the FHIR ingest pipeline later without a code change here.
//
// The walk order is deterministic for a single fixed input (Go decodes JSON
// arrays into []any preserving array order, and encoding/json object keys
// become map[string]any — map iteration order is randomized, but Extract
// dedups+sorts the final result, so collectCodings's own traversal order
// never leaks into Extract's output).
func collectCodings(v any) []coding {
	var out []coding
	switch val := v.(type) {
	case map[string]any:
		if arr, ok := val["coding"].([]any); ok {
			for _, item := range arr {
				obj, ok := item.(map[string]any)
				if !ok {
					continue
				}
				system, _ := obj["system"].(string)
				code, _ := obj["code"].(string)
				if system == "" || code == "" {
					continue
				}
				out = append(out, coding{system: system, code: code})
			}
		}
		for _, child := range val {
			out = append(out, collectCodings(child)...)
		}
	case []any:
		for _, item := range val {
			out = append(out, collectCodings(item)...)
		}
	}
	return out
}

// normalize deduplicates ss and sorts it lexicographically, mirroring
// bead.Normalize's "parents / antigens は重複除去 + 辞書順ソート" convention
// (specs/DESIGN_v3.md §4) so that Extract's output can be assigned directly
// to Bead.Antigens without a further normalization pass. A nil/empty input
// yields nil (not an empty non-nil slice): unlike bead.Normalize, Extract's
// result is an intermediate value the caller merges into a Bead alongside
// any other manually-supplied antigens, not the final JSON-serialized field
// itself, so there is no null-vs-[] JCS concern here.
func normalize(ss []string) []string {
	if len(ss) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(ss))
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// --- Normalization policy (lower-casing) --------------------------------
//
// This package does not lower-case FHIR codes before namespacing them.
// SNOMED/LOINC/RxNorm codes are numeric strings (no case to normalize) in
// every real-world and FHIR_sample-observed coding, so lower-casing them
// would be a no-op for direct extraction. Dictionary keys (dictionary.json's
// "rxnorm" map) and derived atc:/organ:/risk: values are hand-authored
// lower-case already (e.g. "atc:c09aa03", "organ:renal"), so
// dictionary-derived antigens are lower-case by construction, not by a
// runtime normalization step. If a future coding system ever supplies
// alphabetic codes, add an explicit strings.ToLower at the point of
// namespace-prefixing in Extract (not a blanket post-hoc pass over the
// result), so the policy stays visible at the call site that needs it.
