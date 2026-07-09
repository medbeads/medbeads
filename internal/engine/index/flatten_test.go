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
