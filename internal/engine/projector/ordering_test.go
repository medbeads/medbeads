package projector

import (
	"testing"

	"github.com/medbeads/medbeads/internal/engine/bead"
)

// The two timestamps below are verbatim from two Beads in the SAME Pod of the
// production store, at offsets 246113 and 246922 — the first was durably
// appended BEFORE the second.
//
//	offset 246113 (appended first) : 2026-07-11T13:51:46.89Z
//	offset 246922 (appended second): 2026-07-11T13:51:46.897924Z
//
// Chronologically   46.89     <  46.897924   → the second is later. Correct.
// Lexicographically "...89Z"  >  "...897924Z" → the FIRST compares as later,
// because RFC3339Nano (pod/record.go) omits trailing zeros in the fractional
// second, and 'Z' (0x5A) sorts above '7' (0x37).
//
// This shape is not exotic: 208 such inverted pairs exist in the production
// store today.
//
// A fixture built on well-behaved timestamps ("2026-01-01T00:00:00Z" vs
// "2026-01-02T00:00:00Z") sorts identically as a string and as a time, so it
// passes under a broken comparator AND a correct one — which is exactly why the
// existing tests never caught this. These constants are chosen so the two orders
// DISAGREE, which is what gives the test discriminating power.
const (
	writtenFirstAt  = "2026-07-11T13:51:46.89Z"
	writtenSecondAt = "2026-07-11T13:51:46.897924Z"
)

// TestBeadOrdering_LaterAppendWins pins the property the correction chain rests
// on: among competing amendments, the one APPENDED LATER is the current one.
//
// The Bead IDs are adversarial too — the first-appended Bead is given the
// lexicographically GREATER ID, so a comparator falling back to `id DESC` also
// picks the wrong Bead. Both the timestamp path and the ID tiebreak favour the
// wrong answer; only ordering on append position gets it right.
func TestBeadOrdering_LaterAppendWins(t *testing.T) {
	first := resolveBead{ // appended FIRST (lower offset), but greater ID and greater string
		Bead:            bead.Bead{ID: "zzzzzz", Type: "fhir_observation"},
		Offset:          246113,
		RecordedAt:      writtenFirstAt,
		RecordedAtValid: true,
	}
	second := resolveBead{ // appended SECOND — the true current version
		Bead:            bead.Bead{ID: "aaaaaa", Type: "fhir_observation"},
		Offset:          246922,
		RecordedAt:      writtenSecondAt,
		RecordedAtValid: true,
	}

	// beadOrderLess sorts newest-first: the later-appended Bead must sort first.
	if !beadOrderLess(second, first) {
		t.Errorf("beadOrderLess(appendedSecond, appendedFirst) = false, want true.\n"+
			"  offset %d must sort as newer than offset %d.\n"+
			"  recorded_at as a STRING inverts this pair (%q > %q lexicographically, though it is\n"+
			"  chronologically EARLIER), and the id tiebreak (%q > %q) also favours the wrong Bead.\n"+
			"  Only append position gives the right answer.",
			second.Offset, first.Offset,
			first.RecordedAt, second.RecordedAt,
			first.Bead.ID, second.Bead.ID)
	}
	if beadOrderLess(first, second) {
		t.Error("beadOrderLess(appendedFirst, appendedSecond) = true, want false: the order must be antisymmetric")
	}
}

// TestResolvePatientState_LaterAmendmentWins drives the same defect through the
// resolver that actually decides a patient's current record.
//
// Two approved amendments target the same observation. The one appended later
// must become the target's current version. Ordering on the recorded_at string
// makes the EARLIER amendment win — so the chart shows a superseded correction
// as current, deterministically and silently.
func TestResolvePatientState_LaterAmendmentWins(t *testing.T) {
	const target = "target-observation"

	amendFirst := bead.Bead{ID: "zzz-amend-first", Type: "fhir_observation", Amends: []string{target}}
	amendSecond := bead.Bead{ID: "aaa-amend-second", Type: "fhir_observation", Amends: []string{target}}

	beads := []resolveBead{
		{Bead: bead.Bead{ID: target, Type: "fhir_observation"}, Offset: 100},
		{
			Bead:            amendFirst,
			Offset:          246113,
			RecordedAt:      writtenFirstAt,
			RecordedAtValid: true,
		},
		{
			Bead:            amendSecond,
			Offset:          246922,
			RecordedAt:      writtenSecondAt,
			RecordedAtValid: true,
		},
		// Both amendments are approved — the attestation gate is not what is
		// under test here; the ORDER of two valid amendments is.
		{
			Bead: bead.Bead{
				ID:      "attest-first",
				Type:    "attestation",
				Parents: []string{amendFirst.ID},
				Content: map[string]any{"verdict": "approved"},
			},
			Offset: 300000,
		},
		{
			Bead: bead.Bead{
				ID:      "attest-second",
				Type:    "attestation",
				Parents: []string{amendSecond.ID},
				Content: map[string]any{"verdict": "approved"},
			},
			Offset: 300001,
		},
	}

	got := resolvePatientState(beads)[target]

	if got.Status != "amended" {
		t.Fatalf("target status = %q, want amended", got.Status)
	}
	if got.CurrentBeadID != amendSecond.ID {
		t.Errorf("target current_bead_id = %q, want %q (the amendment appended LATER, offset 246922).\n"+
			"  Picking the EARLIER-appended amendment means a superseded correction is shown as the\n"+
			"  patient's current record.",
			got.CurrentBeadID, amendSecond.ID)
	}
}
