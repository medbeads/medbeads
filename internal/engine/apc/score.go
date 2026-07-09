package apc

import (
	"sort"
	"strings"
	"time"
)

// matchScore is the outcome of scoring one candidate pair against a set of
// shared antigens, per specs/MEDBEADS_SIBLING_SPEC.md §6.4 ("スコアリング") —
// verbatim-identical to the canonical spec's
// specs/MEDBEADS_SPECIFICATION_v2.1.md §9.3 ("マッチングルール" > "スコアリング"),
// same table, same threshold. SIBLING_SPEC is this repo's earlier draft of
// the same rules; §9.3 is the spec version actually shipped as
// specs/MEDBEADS_SPECIFICATION_v2.1.md (referenced elsewhere in this
// codebase, e.g. bead.IDPrefix's doc comment) — both are cited below so
// either can be found from this file.
type matchScore struct {
	// Total is the final score after every additive rule and the generation
	// decay (Config.GenerationDecay) have been applied.
	Total float64
	// MatchedAntigens is the shared-antigen set the score was computed from,
	// deduplicated and sorted (deterministic — see scorePair).
	MatchedAntigens []string
}

// scorePair computes the SIBLING_SPEC §6.4 / SPECIFICATION_v2.1 §9.3 match
// score for a pair of Beads sharing matchedAntigens (already filtered to
// exclude any antigen over the IDF frequency threshold — runaway-prevention
// d; see Scanner.candidatesFor). generation is the higher of the two Beads'
// own scan_generation (0 for an ordinary first-generation Bead; >0 only when
// at least one side is itself a sibling_link Bead born from a prior scan —
// runaway-prevention c), used to apply Config.GenerationDecay.
//
// Scoring rules (SIBLING_SPEC §6.4 / SPECIFICATION_v2.1 §9.3's table,
// identical in both), applied in order:
//   - +1 for the first shared antigen, +2 for each additional shared antigen
//     ("共通antigenが1つ: +1" / "共通antigenが2つ以上: +2 per additional")
//   - +3 per shared antigen in the risk: namespace ("臨床的重要度が高い")
//   - +2 per shared antigen in the organ: namespace
//   - +1 per shared antigen in the temporal: namespace
//   - +2 if the two Beads' timestamps are within 24h of each other; +1 if
//     within 7 days (mutually exclusive — the tighter window wins, since the
//     spec's two rows are read as an escalating proximity bonus, not
//     additive)
//   - +3 if one Bead's type is "fhir_medicationrequest" and the other's is a
//     lab/observation type (fhir_observation or fhir_diagnosticreport) —
//     SIBLING_SPEC §6.4's "一方がprescription、他方がlab_results" rule,
//     translated to this project's v3 FHIR-ingest type names (SPEC §2.3's
//     "prescription"/"lab_results" are the pre-v3 EMR-CSV type vocabulary;
//     R4.4/antigen.Extract's ingest path only ever produces "fhir_*" types)
//
// The additive subtotal is then multiplied by GenerationDecay^generation
// (SIBLING_SPEC §6.5 secondary_response_decay / DESIGN §7 point 3), so a
// second-generation match needs proportionally more matched-antigen weight
// to clear MinScoreThreshold than a first-generation one does.
func scorePair(aType, bType, aTimestamp, bTimestamp string, matchedAntigens []string, generation int, cfg Config) matchScore {
	sorted := append([]string(nil), matchedAntigens...)
	sort.Strings(sorted)

	var subtotal float64
	if len(sorted) > 0 {
		subtotal += 1                          // first shared antigen
		subtotal += 2 * float64(len(sorted)-1) // each additional
	}
	for _, ag := range sorted {
		switch {
		case strings.HasPrefix(ag, "risk:"):
			subtotal += 3
		case strings.HasPrefix(ag, "organ:"):
			subtotal += 2
		case strings.HasPrefix(ag, "temporal:"):
			subtotal += 1
		}
	}

	subtotal += temporalProximityBonus(aTimestamp, bTimestamp)
	subtotal += prescriptionLabBonus(aType, bType)

	decay := 1.0
	for i := 0; i < generation; i++ {
		decay *= cfg.GenerationDecay
	}

	return matchScore{Total: subtotal * decay, MatchedAntigens: sorted}
}

// temporalProximityBonus implements SIBLING_SPEC §6.4's time-proximity rows:
// +2 within 24h, +1 within 7 days, 0 otherwise (or if either timestamp fails
// to parse — a malformed timestamp should never crash scoring, only forfeit
// this bonus).
func temporalProximityBonus(aTimestamp, bTimestamp string) float64 {
	a, errA := time.Parse(time.RFC3339, aTimestamp)
	b, errB := time.Parse(time.RFC3339, bTimestamp)
	if errA != nil || errB != nil {
		return 0
	}
	delta := a.Sub(b)
	if delta < 0 {
		delta = -delta
	}
	switch {
	case delta <= 24*time.Hour:
		return 2
	case delta <= 7*24*time.Hour:
		return 1
	default:
		return 0
	}
}

// labResultTypes are this v3 codebase's FHIR-ingest type names that stand in
// for SIBLING_SPEC §6.4's pre-v3 "lab_results" EMR-CSV type (see scorePair's
// doc comment).
var labResultTypes = map[string]bool{
	"fhir_observation":      true,
	"fhir_diagnosticreport": true,
}

// prescriptionLabBonus implements SIBLING_SPEC §6.4's "一方がprescription、
// 他方がlab_results: +3" rule.
func prescriptionLabBonus(aType, bType string) float64 {
	isRxLab := func(x, y string) bool {
		return x == "fhir_medicationrequest" && labResultTypes[y]
	}
	if isRxLab(aType, bType) || isRxLab(bType, aType) {
		return 3
	}
	return 0
}
