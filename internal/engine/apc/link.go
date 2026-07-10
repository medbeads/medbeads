package apc

import (
	"fmt"
	"sort"
	"time"

	"github.com/medbeads/medbeads/internal/engine/bead"
)

// severity mirrors specs/MEDBEADS_SIBLING_SPEC.md §4.4's four-level scale.
// This scanner always emits "warning" (see buildSiblingLinkBead's doc
// comment for why) — the constants exist so the content.severity string
// value is spelled consistently and future severity logic has named targets.
const (
	severityInfo     = "info"
	severityWarning  = "warning"
	severityAlert    = "alert"
	severityCritical = "critical"
)

// relationClinicalCorrelation is the relation type this scanner assigns to
// every sibling_link it generates: specs/MEDBEADS_SIBLING_SPEC.md §4.3 lists
// ten relation values (drug_interaction, contraindication,
// clinical_correlation, ...), each requiring domain knowledge this
// antigen-overlap scanner does not have (e.g. distinguishing "drug
// interaction" from "contraindication" needs a drug-pair-specific rule, not
// just a shared risk: antigen). "clinical_correlation" is the spec's
// general-purpose bucket ("臨床的関連", used in its own worked example: eGFR
// low value <-> nephrotoxic prescription) and is the only relation this
// generic score-based matcher can honestly claim; a future drug-interaction-
// aware scorer can assign the more specific relation values.
const relationClinicalCorrelation = "clinical_correlation"

// buildSiblingLinkBead constructs the (unsaved, ID-less) sibling_link Bead
// for a matched pair, per specs/MEDBEADS_SIBLING_SPEC.md §4.1/§4.2 and
// DESIGN §7. Parents are [a.ID, b.ID] (sorted — see normalizedPair), so
// bead.Normalize's own parents-sort makes the two possible call orders
// (a,b) and (b,a) yield the identical Parents slice and therefore the
// identical hash — ComputeID does not need normalizedPair's ordering
// convention for correctness, but relying on it here anyway keeps this
// function's own reasoning about "which Bead is which" simple.
//
// # Timestamp: deterministic, not wall-clock
//
// This scanner sets Timestamp to the later (max) of a.Timestamp/b.Timestamp
// rather than time.Now(). Two considerations argue for this over a
// wall-clock timestamp, per the task's explicit hint ("研究再現性の観点では
// 入力Beadから導出可能な決定論的timestampが望ましい"):
//
//  1. Reproducibility: Bead.ID is content-derived and Timestamp is a
//     hash-target field (specs/DESIGN_v3.md §4). A wall-clock timestamp
//     would make the *same* sibling_link (same pair, same matched antigens,
//     same score) hash differently every time Scan happens to run — Scan is
//     meant to be idempotent (SIBLING_SPEC §6.4 "重複防止", DESIGN §7 point
//     1's UNIQUE constraint), and a non-reproducible ID actively works
//     against that: a re-scan after, say, restoring from backup and
//     replaying the same ingest history would mint a *different* ID for
//     "the same" link, defeating sibling_pairs' UNIQUE(bead_a, bead_b,
//     matched_antigen) de-duplication (which is scoped to the pair+antigen,
//     not the link ID, but a duplicate-with-different-ID link is exactly the
//     kind of non-determinism the whole content-hash design exists to rule
//     out).
//  2. Clinical honesty: max(a.Timestamp, b.Timestamp) is the earliest
//     instant at which both underlying facts existed, i.e. the earliest
//     instant a correlation between them was even possible to observe — a
//     defensible clinical "as-of" time. When Scan actually ran (wall clock)
//     is recorded separately, in bead_apc_scan.scanned_at (an index.db
//     column, not a hash-target Bead field), which is the right place for a
//     genuinely time-of-processing fact to live.
func buildSiblingLinkBead(a, b bead.Bead, matched []string, score float64, generation int) bead.Bead {
	pairA, pairB := normalizedPair(a, b)

	ts := pairA.Timestamp
	if pairB.Timestamp > ts {
		ts = pairB.Timestamp
	}

	description := fmt.Sprintf(
		"APC scanner matched %s and %s on %d shared antigen(s) (score %.2f): %s",
		pairA.Type, pairB.Type, len(matched), score, joinAntigens(matched),
	)

	return bead.Bead{
		Type:      "sibling_link",
		Timestamp: ts,
		Author:    "apc_daemon",
		Parents:   []string{pairA.ID, pairB.ID},
		// No Antigens field to set (v3.1 removed it from Bead entirely — see
		// bead.Bead's doc comment). content.matched_antigens below is now the
		// sole source for this Bead's own projected tags too:
		// index.IndexBead's extractTags special-cases type="sibling_link" to
		// read this same field (in addition to indexSiblingLink's identical
		// read for sibling_pairs rows), specifically to preserve
		// generation-2 ("二次応答") matching — a later Bead sharing one of
		// these antigens can still match this sibling_link Bead itself as a
		// Scanner candidate, exactly as it could when Antigens was a direct
		// Bead field (see extractTags' doc comment for the full reasoning).
		Content: map[string]any{
			"matched_antigens": append([]string(nil), matched...),
			"score":            score,
			"relation":         relationClinicalCorrelation,
			"severity":         severityWarning,
			"description":      description,
			"detected_by":      "apc_daemon",
			"scan_generation":  generation,
		},
	}
}

// normalizedPair returns (a, b) reordered so the first result's ID is
// lexicographically <= the second's — the "bead_a < bead_b" storage
// convention the task and sibling_pairs' schema comment both call for, so an
// undirected pair always normalizes to one canonical (bead_a, bead_b) no
// matter which order the scanner happened to visit the two Beads in.
func normalizedPair(a, b bead.Bead) (bead.Bead, bead.Bead) {
	if a.ID <= b.ID {
		return a, b
	}
	return b, a
}

// joinAntigens renders matched (already deduplicated by the caller) as a
// human-readable comma list for the description field, sorting first so the
// text is deterministic (map/slice iteration order elsewhere in the call
// chain must never leak into a hash-target Content string).
func joinAntigens(matched []string) string {
	sorted := append([]string(nil), matched...)
	sort.Strings(sorted)
	out := ""
	for i, ag := range sorted {
		if i > 0 {
			out += ", "
		}
		out += ag
	}
	return out
}

// nowRFC3339 is scanTimestamp's wall-clock source, a var (not a direct
// time.Now() call at each use site) so tests can override it for
// deterministic bead_apc_scan.scanned_at assertions without depending on
// real elapsed time.
var nowRFC3339 = func() string { return time.Now().UTC().Format(time.RFC3339) }
