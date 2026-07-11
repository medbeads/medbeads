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
	if b.Type == clinicalNoteType {
		if searchText, summary, ok := flattenClinicalNote(b); ok {
			return searchText, summary
		}
		// content.raw_text absent or not a string: fall through to the
		// generic behavior below rather than producing an empty index entry.
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
