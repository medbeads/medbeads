package index

import (
	"fmt"
	"sort"
	"strings"

	"github.com/medbeads/medbeads/internal/engine/bead"
)

// Flattener turns a Bead into the two derived text fields index.db stores
// alongside it: search_text (indexed by beads_fts, R3.3) and summary (the
// one-line, machine-generated L1 token-budget text, R3.3 / DESIGN §8). Per
// the task split, FHIR-type-specific flattening ("メロペネム 1g 点滴静注
// 8時間毎"-style human-readable text) is a later unit's responsibility;
// this package only defines the seam and a generic fallback implementation.
type Flattener interface {
	Flatten(b bead.Bead) (searchText, summary string)
}

// DefaultFlattener is a type-agnostic Flattener: it recursively concatenates
// every string value found in b.Content (map/slice values included) into
// search_text, and derives summary as b.Type plus the first such string,
// truncated. It exists so Reindex can self-drive without depending on a
// FHIR-aware flattener that does not exist yet; callers with real
// type-specific flattening (a future ingest/store layer) should supply their
// own Flattener to IndexBead instead.
type DefaultFlattener struct{}

// maxSummaryLen bounds DefaultFlattener's generated summary length so a
// single huge Content string value can't blow up beads.summary (which is
// meant to be an L1, ~40-token budget field per DESIGN §8).
const maxSummaryLen = 200

// Flatten implements Flattener.
func (DefaultFlattener) Flatten(b bead.Bead) (searchText, summary string) {
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
