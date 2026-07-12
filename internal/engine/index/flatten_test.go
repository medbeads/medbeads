package index

import (
	"strings"
	"testing"

	"github.com/medbeads/medbeads/internal/engine/bead"
)

// TestDefaultFlattener_CollectsNestedStrings checks that DefaultFlattener
// walks nested maps/slices in Content and produces a deterministic,
// order-independent search_text (sorted), plus a non-empty summary.
func TestDefaultFlattener_CollectsNestedStrings(t *testing.T) {
	b := bead.Bead{
		Type:      "fhir_medicationrequest",
		Timestamp: "2026-01-01T00:00:00Z",
		Content: map[string]any{
			"drug": "meropenem",
			"coding": []any{
				map[string]any{"system": "rxnorm", "code": "6919", "display": "Meropenem"},
			},
			"dose": 1.0, // numbers must not appear in search_text
		},
	}

	f := DefaultFlattener{}
	searchText, summary := f.Flatten(b)

	for _, want := range []string{"meropenem", "rxnorm", "6919", "Meropenem"} {
		if !strings.Contains(searchText, want) {
			t.Errorf("search_text = %q, want it to contain %q", searchText, want)
		}
	}
	if strings.Contains(searchText, "1970") || strings.Contains(searchText, "1.0") {
		t.Errorf("search_text = %q, should not contain a stringified numeric dose", searchText)
	}
	if !strings.HasPrefix(summary, b.Type+":") {
		t.Errorf("summary = %q, want prefix %q", summary, b.Type+":")
	}
}

// TestDefaultFlattener_EmptyContent checks the empty-Content edge case does
// not panic and yields an empty search_text with a type-only summary.
func TestDefaultFlattener_EmptyContent(t *testing.T) {
	b := bead.Bead{Type: "patient_registration", Timestamp: "2026-01-01T00:00:00Z"}
	f := DefaultFlattener{}
	searchText, summary := f.Flatten(b)
	if searchText != "" {
		t.Errorf("search_text = %q, want empty", searchText)
	}
	if summary != "patient_registration" {
		t.Errorf("summary = %q, want %q", summary, "patient_registration")
	}
}

// TestDefaultFlattener_ClinicalNote_PreservesWordOrder checks that a
// clinical_note Bead's search_text is content.raw_text verbatim (word order
// preserved, unlike the generic sort.Strings path used for other types), and
// that summary is a meaningful prefix (the note's first non-empty line), not
// a sorted content fragment.
func TestDefaultFlattener_ClinicalNote_PreservesWordOrder(t *testing.T) {
	rawText := "Chief Complaint\nPatient presents with fever and cough.\nHistory of present illness follows."
	b := bead.Bead{
		Type:      "clinical_note",
		Timestamp: "2026-01-01T00:00:00Z",
		Content: map[string]any{
			"raw_text":           rawText,
			"source_system":      "synthea",
			"source_document_id": "docref-1",
			"language":           "en",
			"status":             "current",
			"note_type_code":     "34117-2",
		},
	}

	f := DefaultFlattener{}
	searchText, summary := f.Flatten(b)

	if searchText != rawText {
		t.Errorf("search_text = %q, want raw_text verbatim %q", searchText, rawText)
	}
	// Word order must be preserved, not alphabetically sorted: "Chief" must
	// precede "Complaint" (as in the source), not the reverse.
	if strings.Index(searchText, "Chief") > strings.Index(searchText, "Complaint") {
		t.Errorf("search_text = %q, word order not preserved", searchText)
	}
	wantSummary := "clinical_note: Chief Complaint"
	if summary != wantSummary {
		t.Errorf("summary = %q, want %q", summary, wantSummary)
	}
	// Metadata fields (source_document_id, note_type_code, etc.) must not
	// leak into search_text as extra sorted fragments.
	if strings.Contains(searchText, "docref-1") || strings.Contains(searchText, "34117-2") {
		t.Errorf("search_text = %q, must not contain source metadata fields", searchText)
	}
}

// TestDefaultFlattener_ClinicalNote_PrefersHeadingOverLeadingDateLine checks
// the real-Synthea-shaped edge case (VERIFIED via ~/medbeads-synthea smoke
// ingest): a note's raw_text often opens with a blank line then a bare date
// stamp ("2025-11-05") before its first "# Chief Complaint" heading — the
// summary must skip the meaningless date line and use the heading, not
// literally "the first non-blank line" (which would be the date).
func TestDefaultFlattener_ClinicalNote_PrefersHeadingOverLeadingDateLine(t *testing.T) {
	rawText := "\n2025-11-05\n\n# Chief Complaint\nNo complaints.\n\n# History of Present Illness\nPatient is stable."
	b := bead.Bead{
		Type: "clinical_note",
		Content: map[string]any{
			"raw_text": rawText,
		},
	}

	f := DefaultFlattener{}
	searchText, summary := f.Flatten(b)

	if searchText != rawText {
		t.Errorf("search_text = %q, want raw_text verbatim", searchText)
	}
	wantSummary := "clinical_note: # Chief Complaint"
	if summary != wantSummary {
		t.Errorf("summary = %q, want %q (heading, not the leading date line)", summary, wantSummary)
	}
}

// TestDefaultFlattener_ClinicalNote_NoRawText checks the fallback: a
// clinical_note Bead without content.raw_text degrades to the generic
// DefaultFlattener behavior instead of producing an empty/panicking result.
func TestDefaultFlattener_ClinicalNote_NoRawText(t *testing.T) {
	b := bead.Bead{
		Type:      "clinical_note",
		Timestamp: "2026-01-01T00:00:00Z",
		Content: map[string]any{
			"source_system": "synthea",
		},
	}
	f := DefaultFlattener{}
	searchText, summary := f.Flatten(b)
	if searchText != "synthea" {
		t.Errorf("search_text = %q, want fallback to generic collectStrings output %q", searchText, "synthea")
	}
	if summary != "clinical_note: synthea" {
		t.Errorf("summary = %q, want %q", summary, "clinical_note: synthea")
	}
}

// TestDefaultFlattener_ClinicalNote_Deterministic checks that flattening the
// same clinical_note Content repeatedly always yields the same
// search_text/summary (same requirement as the generic Deterministic test,
// but exercising the raw_text-preserving path instead of collectStrings).
func TestDefaultFlattener_ClinicalNote_Deterministic(t *testing.T) {
	f := DefaultFlattener{}
	var firstSearch, firstSummary string
	for i := 0; i < 20; i++ {
		b := bead.Bead{
			Type: "clinical_note",
			Content: map[string]any{
				"raw_text":           "Assessment and Plan\nContinue current medications.",
				"source_system":      "synthea",
				"source_document_id": "docref-2",
			},
		}
		searchText, summary := f.Flatten(b)
		if i == 0 {
			firstSearch, firstSummary = searchText, summary
			continue
		}
		if searchText != firstSearch || summary != firstSummary {
			t.Fatalf("Flatten not deterministic: run 0 = (%q, %q), run %d = (%q, %q)",
				firstSearch, firstSummary, i, searchText, summary)
		}
	}
}

// TestDefaultFlattener_Deterministic checks that flattening the same
// Content twice (as separate map[string]any literals, so Go's randomized
// map iteration order actually varies between runs of this test in a loop)
// always yields the same search_text — required so Reindex output is
// byte-identical to a manual IndexBead run.
func TestDefaultFlattener_Deterministic(t *testing.T) {
	f := DefaultFlattener{}
	var first string
	for i := 0; i < 20; i++ {
		b := bead.Bead{
			Type: "fhir_observation",
			Content: map[string]any{
				"a": "alpha",
				"b": "bravo",
				"c": "charlie",
				"d": "delta",
			},
		}
		searchText, _ := f.Flatten(b)
		if i == 0 {
			first = searchText
			continue
		}
		if searchText != first {
			t.Fatalf("Flatten not deterministic across map iteration: run 0 = %q, run %d = %q", first, i, searchText)
		}
	}
}

// --- FHIR-aware summary tests -------------------------------------------

// TestDefaultFlattener_FHIRObservation_TextAndValueQuantity checks the
// requirement-2 headline case: an Observation with code.text and
// valueQuantity produces "<label> <value> <unit>", value rounded to a
// sensible number of decimal places (VERIFIED-shaped: this is the real
// store's own Body-temperature Observation content, 37.791 Cel).
func TestDefaultFlattener_FHIRObservation_TextAndValueQuantity(t *testing.T) {
	b := bead.Bead{
		Type: "fhir_observation",
		Content: map[string]any{
			"code": map[string]any{
				"text": "Body temperature",
				"coding": []any{
					map[string]any{"system": "http://loinc.org", "code": "8310-5", "display": "Body temperature"},
				},
			},
			"valueQuantity": map[string]any{
				"value": 37.791,
				"unit":  "Cel",
				"code":  "Cel",
			},
		},
	}
	_, summary := DefaultFlattener{}.Flatten(b)
	want := "fhir_observation: Body temperature 37.791 Cel"
	if summary != want {
		t.Errorf("summary = %q, want %q", summary, want)
	}
}

// TestDefaultFlattener_FHIRObservation_RoundsFloatArtifact checks the
// requirement-2 "do not print 37.791000000001" example directly: a
// valueQuantity.value with floating-point noise past 3 decimal places must
// still render as a short, clean number.
func TestDefaultFlattener_FHIRObservation_RoundsFloatArtifact(t *testing.T) {
	b := bead.Bead{
		Type: "fhir_observation",
		Content: map[string]any{
			"code":          map[string]any{"text": "Body temperature"},
			"valueQuantity": map[string]any{"value": 37.791000000001, "unit": "Cel"},
		},
	}
	_, summary := DefaultFlattener{}.Flatten(b)
	want := "fhir_observation: Body temperature 37.791 Cel"
	if summary != want {
		t.Errorf("summary = %q, want %q (rounded, no float artifact)", summary, want)
	}
}

// TestDefaultFlattener_FHIRObservation_ValueCodeableConcept checks the
// non-numeric-result case (e.g. "Tobacco smoking status" ->
// "Never smoked tobacco (finding)"): valueCodeableConcept.text is appended
// exactly like a valueQuantity would be.
func TestDefaultFlattener_FHIRObservation_ValueCodeableConcept(t *testing.T) {
	b := bead.Bead{
		Type: "fhir_observation",
		Content: map[string]any{
			"code": map[string]any{"text": "Tobacco smoking status"},
			"valueCodeableConcept": map[string]any{
				"text": "Never smoked tobacco (finding)",
			},
		},
	}
	_, summary := DefaultFlattener{}.Flatten(b)
	want := "fhir_observation: Tobacco smoking status Never smoked tobacco (finding)"
	if summary != want {
		t.Errorf("summary = %q, want %q", summary, want)
	}
}

// TestDefaultFlattener_FHIRCondition_ClinicalStatusCodeableConcept is the
// direct regression test for the failure class commit 80b812e already fixed
// once in package projector (fhirCodeString): Condition.clinicalStatus is a
// CodeableConcept in real Synthea data, not a plain string. This test's
// point is narrower — it does not read clinicalStatus for the summary at
// all — but it plants a CodeableConcept-shaped clinicalStatus/
// verificationStatus alongside code to prove flattenFHIRSummary's code.text
// extraction is unaffected by, and does not mis-scan into, sibling fields
// that happen to share the CodeableConcept shape.
func TestDefaultFlattener_FHIRCondition_ClinicalStatusCodeableConcept(t *testing.T) {
	b := bead.Bead{
		Type: "fhir_condition",
		Content: map[string]any{
			"clinicalStatus": map[string]any{
				"coding": []any{map[string]any{"code": "active"}},
			},
			"verificationStatus": map[string]any{
				"coding": []any{map[string]any{"code": "confirmed"}},
			},
			"code": map[string]any{
				"text": "Asthma (disorder)",
				"coding": []any{
					map[string]any{"system": "http://snomed.info/sct", "code": "195967001", "display": "Asthma (disorder)"},
				},
			},
		},
	}
	_, summary := DefaultFlattener{}.Flatten(b)
	want := "fhir_condition: Asthma (disorder)"
	if summary != want {
		t.Errorf("summary = %q, want %q", summary, want)
	}
}

// TestDefaultFlattener_FHIRMedicationRequest_MedicationCodeableConcept
// checks the medication-specific field path (medicationCodeableConcept, not
// code) per requirement 2.
func TestDefaultFlattener_FHIRMedicationRequest_MedicationCodeableConcept(t *testing.T) {
	b := bead.Bead{
		Type: "fhir_medicationrequest",
		Content: map[string]any{
			"medicationCodeableConcept": map[string]any{
				"text": "amLODIPine 2.5 MG Oral Tablet",
				"coding": []any{
					map[string]any{"system": "http://www.nlm.nih.gov/research/umls/rxnorm", "code": "308136", "display": "amLODIPine 2.5 MG Oral Tablet"},
				},
			},
		},
	}
	_, summary := DefaultFlattener{}.Flatten(b)
	want := "fhir_medicationrequest: amLODIPine 2.5 MG Oral Tablet"
	if summary != want {
		t.Errorf("summary = %q, want %q", summary, want)
	}
}

// TestDefaultFlattener_FHIRProcedure_Text checks Procedure's code.text path
// ("Throat culture (procedure)" from the real store).
func TestDefaultFlattener_FHIRProcedure_Text(t *testing.T) {
	b := bead.Bead{
		Type: "fhir_procedure",
		Content: map[string]any{
			"code": map[string]any{"text": "Throat culture (procedure)"},
		},
	}
	_, summary := DefaultFlattener{}.Flatten(b)
	want := "fhir_procedure: Throat culture (procedure)"
	if summary != want {
		t.Errorf("summary = %q, want %q", summary, want)
	}
}

// TestDefaultFlattener_FHIRDiagnosticReport_CodingDisplayFallback checks the
// preference order's second rung: code.text absent, so summary falls back to
// coding[0].display (VERIFIED-shaped: the real store's DiagnosticReport.code
// carries coding[] but no top-level text).
func TestDefaultFlattener_FHIRDiagnosticReport_CodingDisplayFallback(t *testing.T) {
	b := bead.Bead{
		Type: "fhir_diagnosticreport",
		Content: map[string]any{
			"code": map[string]any{
				"coding": []any{
					map[string]any{"system": "http://loinc.org", "code": "34117-2", "display": "History and physical note"},
					map[string]any{"system": "http://loinc.org", "code": "51847-2", "display": "Evaluation + Plan note"},
				},
			},
		},
	}
	_, summary := DefaultFlattener{}.Flatten(b)
	want := "fhir_diagnosticreport: History and physical note"
	if summary != want {
		t.Errorf("summary = %q, want %q", summary, want)
	}
}

// TestDefaultFlattener_FHIREncounter_TypeArray checks Encounter's summary
// path, which (unlike every other type handled here) has no top-level
// "code" at all: the label lives at type[0] (VERIFIED against the real
// store).
func TestDefaultFlattener_FHIREncounter_TypeArray(t *testing.T) {
	b := bead.Bead{
		Type: "fhir_encounter",
		Content: map[string]any{
			"type": []any{
				map[string]any{
					"text": "Encounter for symptom (procedure)",
					"coding": []any{
						map[string]any{"system": "http://snomed.info/sct", "code": "185345009", "display": "Encounter for symptom (procedure)"},
					},
				},
			},
		},
	}
	_, summary := DefaultFlattener{}.Flatten(b)
	want := "fhir_encounter: Encounter for symptom (procedure)"
	if summary != want {
		t.Errorf("summary = %q, want %q", summary, want)
	}
}

// TestDefaultFlattener_FHIRImmunization_VaccineCode checks Immunization's
// vaccineCode.text path.
func TestDefaultFlattener_FHIRImmunization_VaccineCode(t *testing.T) {
	b := bead.Bead{
		Type: "fhir_immunization",
		Content: map[string]any{
			"vaccineCode": map[string]any{"text": "Influenza, split virus, trivalent, PF"},
		},
	}
	_, summary := DefaultFlattener{}.Flatten(b)
	want := "fhir_immunization: Influenza, split virus, trivalent, PF"
	if summary != want {
		t.Errorf("summary = %q, want %q", summary, want)
	}
}

// TestDefaultFlattener_FHIRAllergyIntolerance_Text checks
// AllergyIntolerance's code.text path.
func TestDefaultFlattener_FHIRAllergyIntolerance_Text(t *testing.T) {
	b := bead.Bead{
		Type: "fhir_allergyintolerance",
		Content: map[string]any{
			"code": map[string]any{"text": "Grass pollen (substance)"},
		},
	}
	_, summary := DefaultFlattener{}.Flatten(b)
	want := "fhir_allergyintolerance: Grass pollen (substance)"
	if summary != want {
		t.Errorf("summary = %q, want %q", summary, want)
	}
}

// TestDefaultFlattener_FHIRImagingStudy_ProcedureCodeArray checks
// ImagingStudy's summary path, which (like Encounter) has no top-level
// "code": the label lives at procedureCode[0] (VERIFIED against the real
// store).
func TestDefaultFlattener_FHIRImagingStudy_ProcedureCodeArray(t *testing.T) {
	b := bead.Bead{
		Type: "fhir_imagingstudy",
		Content: map[string]any{
			"procedureCode": []any{
				map[string]any{"text": "Dental plain X-ray bitewing (procedure)"},
			},
		},
	}
	_, summary := DefaultFlattener{}.Flatten(b)
	want := "fhir_imagingstudy: Dental plain X-ray bitewing (procedure)"
	if summary != want {
		t.Errorf("summary = %q, want %q", summary, want)
	}
}

// TestDefaultFlattener_FHIR_MissingCodeFallsBackToGeneric checks the
// requirement-3 degrade-gracefully case: a FHIR-typed Bead whose Content has
// no code field at all (not even the wrong shape — simply absent) must not
// panic and must not produce an empty summary; it falls back to
// DefaultFlattener's fully generic behavior.
func TestDefaultFlattener_FHIR_MissingCodeFallsBackToGeneric(t *testing.T) {
	b := bead.Bead{
		Type: "fhir_observation",
		Content: map[string]any{
			"status": "final",
		},
	}
	_, summary := DefaultFlattener{}.Flatten(b)
	want := "fhir_observation: final"
	if summary != want {
		t.Errorf("summary = %q, want %q (generic fallback, not empty/panic)", summary, want)
	}
}

// TestDefaultFlattener_FHIR_CodeWrongShapeFallsBackToGeneric checks the
// requirement-3 "may be a string" case directly: content.code as a bare
// string (not a CodeableConcept object) must not panic
// (fhirCodeableConceptLabel's map[string]any type assertion fails cleanly)
// and must degrade to the generic fallback rather than an empty summary.
func TestDefaultFlattener_FHIR_CodeWrongShapeFallsBackToGeneric(t *testing.T) {
	b := bead.Bead{
		Type: "fhir_condition",
		Content: map[string]any{
			"code": "not-a-codeable-concept",
		},
	}
	_, summary := DefaultFlattener{}.Flatten(b)
	want := "fhir_condition: not-a-codeable-concept"
	if summary != want {
		t.Errorf("summary = %q, want %q (generic fallback, not empty/panic)", summary, want)
	}
}

// TestDefaultFlattener_FHIR_EmptyCodingFallsBackToGeneric checks the
// requirement-3 "coding present but empty" edge: code.coding == [] and no
// code.text must not panic, and must degrade to the generic fallback (no
// clinical label is extractable) rather than an empty summary.
func TestDefaultFlattener_FHIR_EmptyCodingFallsBackToGeneric(t *testing.T) {
	b := bead.Bead{
		Type: "fhir_procedure",
		Content: map[string]any{
			"code":   map[string]any{"coding": []any{}},
			"status": "completed",
		},
	}
	_, summary := DefaultFlattener{}.Flatten(b)
	want := "fhir_procedure: completed"
	if summary != want {
		t.Errorf("summary = %q, want %q (generic fallback, not empty/panic)", summary, want)
	}
}

// TestDefaultFlattener_FHIR_CodingCodeFallback checks the third-rung
// preference: no text, no display, only coding[0].code.
func TestDefaultFlattener_FHIR_CodingCodeFallback(t *testing.T) {
	b := bead.Bead{
		Type: "fhir_condition",
		Content: map[string]any{
			"code": map[string]any{
				"coding": []any{
					map[string]any{"system": "http://snomed.info/sct", "code": "195967001"},
				},
			},
		},
	}
	_, summary := DefaultFlattener{}.Flatten(b)
	want := "fhir_condition: 195967001"
	if summary != want {
		t.Errorf("summary = %q, want %q", summary, want)
	}
}

// TestDefaultFlattener_FHIR_SearchTextStillGeneric checks that FHIR-aware
// summary derivation does not change search_text's own contract: it must
// still be the sorted, order-independent collectStrings walk (so full-text
// search recall is unaffected by this change), not just the summary label.
func TestDefaultFlattener_FHIR_SearchTextStillGeneric(t *testing.T) {
	b := bead.Bead{
		Type: "fhir_observation",
		Content: map[string]any{
			"code":   map[string]any{"text": "Body temperature"},
			"status": "final",
		},
	}
	searchText, _ := DefaultFlattener{}.Flatten(b)
	for _, want := range []string{"Body temperature", "final"} {
		if !strings.Contains(searchText, want) {
			t.Errorf("search_text = %q, want it to contain %q", searchText, want)
		}
	}
}

// TestDefaultFlattener_FHIR_Deterministic checks that flattening the same
// FHIR Content repeatedly always yields the same summary, mirroring
// TestDefaultFlattener_Deterministic / _ClinicalNote_Deterministic for the
// new FHIR-aware summary path.
func TestDefaultFlattener_FHIR_Deterministic(t *testing.T) {
	f := DefaultFlattener{}
	var firstSummary string
	for i := 0; i < 20; i++ {
		b := bead.Bead{
			Type: "fhir_observation",
			Content: map[string]any{
				"code": map[string]any{
					"text": "Body temperature",
					"coding": []any{
						map[string]any{"system": "http://loinc.org", "code": "8310-5", "display": "Body temperature"},
					},
				},
				"valueQuantity": map[string]any{"value": 37.791, "unit": "Cel"},
				"status":        "final",
				"category": []any{
					map[string]any{"coding": []any{map[string]any{"code": "vital-signs"}}},
				},
			},
		}
		_, summary := f.Flatten(b)
		if i == 0 {
			firstSummary = summary
			continue
		}
		if summary != firstSummary {
			t.Fatalf("Flatten not deterministic: run 0 = %q, run %d = %q", firstSummary, i, summary)
		}
	}
}
