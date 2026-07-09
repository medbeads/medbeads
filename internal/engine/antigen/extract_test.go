package antigen

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// loadFixture reads a real Synthea FHIR resource JSON from testdata/ (synthetic
// data, no PHI — see testdata/*.json provenance comment in this file) and
// decodes it the same way a FHIR ingest pipeline would: into a bare
// map[string]any, exactly what Extract's content parameter expects.
func loadFixture(t *testing.T, name string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read testdata/%s: %v", name, err)
	}
	var content map[string]any
	if err := json.Unmarshal(raw, &content); err != nil {
		t.Fatalf("unmarshal testdata/%s: %v", name, err)
	}
	return content
}

// The testdata/*.json fixtures below are unmodified excerpts of individual
// FHIR resources taken from FHIR_sample/*.json (Synthea-generated synthetic
// patient bundles distributed with this repo). Synthea data is entirely
// synthetic — no real patient, so there is no PHI concern in committing
// these as test fixtures.

// TestExtract_MedicationRxNorm exercises direct rxnorm: extraction plus
// dictionary-derived atc:/organ:/risk: antigens, using a real Medication
// resource (FHIR_sample/Steuber892_Roman975_79.json) whose RxNorm code
// (309362, clopidogrel) is a dictionary.json entry.
func TestExtract_MedicationRxNorm(t *testing.T) {
	content := loadFixture(t, "medication_clopidogrel.json")

	got := Extract("fhir_medication", content)

	want := []string{
		"atc:b01ac04",
		"organ:cardiovascular",
		"organ:hematologic",
		"risk:bleeding",
		"rxnorm:309362",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Extract(fhir_medication, clopidogrel) = %v, want %v", got, want)
	}
}

// TestExtract_ConditionSnomed exercises direct snomed: extraction from a
// real Condition resource (FHIR_sample/*.json, chronic sinusitis) with no
// beadType rule and no dictionary hit (SNOMED codes are not in
// dictionary.json).
func TestExtract_ConditionSnomed(t *testing.T) {
	content := loadFixture(t, "condition_sinusitis.json")

	got := Extract("fhir_condition", content)

	want := []string{"snomed:40055000"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Extract(fhir_condition, sinusitis) = %v, want %v", got, want)
	}
}

// TestExtract_ObservationLoincComponents exercises the nested-coding walk: a
// real vital-signs blood-pressure Observation has coding under content.code
// AND under content.component[].code (systolic/diastolic), plus a
// content.category[].coding whose system (observation-category) is not one
// of the three recognized systems and so contributes nothing. All three
// loinc: codes must be found and deduplicated/sorted; the unrecognized
// category coding must be silently skipped (not panic, not appear).
func TestExtract_ObservationLoincComponents(t *testing.T) {
	content := loadFixture(t, "observation_blood_pressure.json")

	got := Extract("fhir_observation", content)

	want := []string{"loinc:55284-4", "loinc:8462-4", "loinc:8480-6"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Extract(fhir_observation, blood pressure) = %v, want %v", got, want)
	}
}

// TestExtract_EncounterTemporalAndMultiPath exercises both the beadType ->
// temporal: rule and coding nested under two different parent fields
// (content.type[].coding and content.reason.coding) in the same resource, a
// real Synthea Encounter (outpatient visit for acute bronchitis).
func TestExtract_EncounterTemporalAndMultiPath(t *testing.T) {
	content := loadFixture(t, "encounter.json")

	got := Extract("fhir_encounter", content)

	want := []string{"snomed:10509002", "snomed:185345009", "temporal:encounter"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Extract(fhir_encounter, encounter) = %v, want %v", got, want)
	}
}

// TestExtract_UnknownBeadTypeNoTemporal confirms the temporal: rule only
// fires for beadTypes present in beadTypeTemporal — an Encounter-shaped
// content under an unmapped beadType yields no temporal: antigen (but the
// coding-derived antigens are unaffected, since direct extraction does not
// depend on beadType at all).
func TestExtract_UnknownBeadTypeNoTemporal(t *testing.T) {
	content := loadFixture(t, "encounter.json")

	got := Extract("fhir_something_unmapped", content)

	want := []string{"snomed:10509002", "snomed:185345009"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Extract(fhir_something_unmapped, encounter) = %v, want %v", got, want)
	}
}

// TestExtract_NilContent confirms Extract never panics on a nil content map
// (e.g. a Bead whose Content is empty) and simply returns no antigens
// (except any beadType-only rule).
func TestExtract_NilContent(t *testing.T) {
	got := Extract("fhir_condition", nil)
	if got != nil {
		t.Fatalf("Extract(fhir_condition, nil) = %v, want nil", got)
	}
}

// TestExtract_EmptyContent confirms an empty (non-nil) content map also
// yields no antigens.
func TestExtract_EmptyContent(t *testing.T) {
	got := Extract("fhir_condition", map[string]any{})
	if got != nil {
		t.Fatalf("Extract(fhir_condition, {}) = %v, want nil", got)
	}
}

// TestExtract_FreeTextContent confirms non-FHIR content (arbitrary free-text
// / EMR-CSV-shaped fields, no "coding" arrays anywhere) does not panic and
// yields no coding-derived antigens. This is the "非 FHIR content" case
// required by this unit: a legacy EMR-style note bead.
func TestExtract_FreeTextContent(t *testing.T) {
	content := map[string]any{
		"drug":      "メロペネム 1g",
		"route":     "点滴静注",
		"frequency": "8時間毎",
		"recorder":  "田中医師",
	}

	got := Extract("prescription", content)

	if got != nil {
		t.Fatalf("Extract(prescription, free-text) = %v, want nil", got)
	}
}

// TestExtract_MalformedCodingShapesDoNotPanic feeds a grab-bag of
// structurally-almost-FHIR content (coding present but not a list, coding
// list entries that are not objects, missing system, missing code, empty
// strings, wrong types) through Extract to confirm collectCodings only ever
// accepts well-formed {system, code} pairs and silently skips (never
// panics on) everything else.
func TestExtract_MalformedCodingShapesDoNotPanic(t *testing.T) {
	content := map[string]any{
		"code": map[string]any{
			"coding": "not-a-list",
		},
		"category": []any{
			map[string]any{
				"coding": []any{
					"not-an-object",
					map[string]any{"system": "http://snomed.info/sct"},                // missing code
					map[string]any{"code": "12345"},                                   // missing system
					map[string]any{"system": "", "code": "12345"},                     // empty system
					map[string]any{"system": "http://snomed.info/sct", "code": ""},    // empty code
					map[string]any{"system": 123, "code": "12345"},                    // wrong type
					map[string]any{"system": "http://snomed.info/sct", "code": "999"}, // valid
				},
			},
		},
		"weird": []any{1, "two", nil, true, 3.14},
	}

	got := Extract("fhir_observation", content)

	want := []string{"snomed:999"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Extract(malformed) = %v, want %v", got, want)
	}
}

// TestExtract_DeterministicOverManyCalls is the required golden determinism
// test: the same (beadType, content) input, run 100 times, must produce the
// byte-identical (including order) output every time — collectCodings walks
// Go maps (randomized iteration order) and dictionary lookups go through a
// Go map too, so this specifically guards against either leaking
// nondeterministic order into the result.
func TestExtract_DeterministicOverManyCalls(t *testing.T) {
	content := loadFixture(t, "encounter.json")
	medContent := loadFixture(t, "medication_clopidogrel.json")

	first := Extract("fhir_encounter", content)
	firstMed := Extract("fhir_medication", medContent)

	for i := 0; i < 100; i++ {
		got := Extract("fhir_encounter", content)
		if !reflect.DeepEqual(got, first) {
			t.Fatalf("iteration %d: Extract(encounter) = %v, want %v (first call)", i, got, first)
		}
		gotMed := Extract("fhir_medication", medContent)
		if !reflect.DeepEqual(gotMed, firstMed) {
			t.Fatalf("iteration %d: Extract(medication) = %v, want %v (first call)", i, gotMed, firstMed)
		}
	}
}

// TestExtract_DoesNotMutateContent confirms Extract is read-only over its
// content argument (a defensive property of "pure function": callers must
// be able to Extract from the same content map they are about to embed in a
// Bead without Extract altering it).
func TestExtract_DoesNotMutateContent(t *testing.T) {
	content := loadFixture(t, "condition_sinusitis.json")
	before, err := json.Marshal(content)
	if err != nil {
		t.Fatalf("marshal before: %v", err)
	}

	_ = Extract("fhir_condition", content)

	after, err := json.Marshal(content)
	if err != nil {
		t.Fatalf("marshal after: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("Extract mutated content:\nbefore: %s\nafter:  %s", before, after)
	}
}
