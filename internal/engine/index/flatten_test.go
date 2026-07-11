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
