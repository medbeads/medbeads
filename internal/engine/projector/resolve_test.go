package projector

import (
	"testing"

	"github.com/medbeads/medbeads/internal/engine/bead"
)

// rb is a resolve_test.go-local shorthand for building a resolveBead fixture
// without repeating field names at every call site.
func rb(id, typ string, parents, amends, retracts []string, content map[string]any, recordedAt string, recordedAtValid bool) resolveBead {
	if content == nil {
		content = map[string]any{}
	}
	return resolveBead{
		Bead: bead.Bead{
			ID:       id,
			Type:     typ,
			Parents:  parents,
			Amends:   amends,
			Retracts: retracts,
			Content:  content,
		},
		RecordedAt:      recordedAt,
		RecordedAtValid: recordedAtValid,
	}
}

// --- must-fix: NULL recorded_at + ordering axis -----------------------------

// TestResolvePatientState_NullRecordedAtIsOldest proves beadOrderLess/
// resolvePatientState treat a NULL-recorded_at Bead as strictly OLDER than
// any Bead with a non-NULL recorded_at, regardless of Bead ID lexical order —
// specs/U4_state_derivation.md's "穴2" fix: "(recorded_at IS NULL) ASC,
// recorded_at DESC, id DESC … NULL は最古扱い".
//
// Fixture: "aaa" (a lexically-SMALLEST ID) has a valid, late recorded_at;
// "zzz" (lexically-GREATEST ID) has recorded_at NULL. If NULL were ever
// mistakenly treated as "no information, fall through to ID order" (rather
// than "always oldest"), "zzz" would wrongly win the retraction race below.
// Both retract a common target "fact1"; the winner (retraction_bead_id) must
// be "aaa" (the one with a real recorded_at), not "zzz".
func TestResolvePatientState_NullRecordedAtIsOldest(t *testing.T) {
	fact := rb("fact1", "fhir_condition", nil, nil, nil, map[string]any{"clinicalStatus": "active"}, "2026-01-01T00:00:00Z", true)
	nullRetraction := rb("zzz-null-retraction", "retraction", []string{"fact1"}, nil, []string{"fact1"}, nil, "", false)
	validRetraction := rb("aaa-valid-retraction", "retraction", []string{"fact1"}, nil, []string{"fact1"}, nil, "2026-06-01T00:00:00Z", true)

	states := resolvePatientState([]resolveBead{fact, nullRetraction, validRetraction})

	factState := states["fact1"]
	if factState.Status != "retracted" {
		t.Fatalf("fact1 status = %q, want retracted", factState.Status)
	}
	if factState.RetractionBeadID != "aaa-valid-retraction" {
		t.Errorf("fact1 retraction_bead_id = %q, want %q (the NON-null recorded_at retraction, even though its ID sorts lexically first) — "+
			"NULL recorded_at must be treated as OLDEST, not as a tie-break-by-ID fallback",
			factState.RetractionBeadID, "aaa-valid-retraction")
	}
}

// TestResolvePatientState_OrderingUsesRecordedAtNotTimestamp is the "遅延臨床
// timestamp の amendment" fixture the task specifically calls for. Two
// COMPETING approved amendments name the same target, so which one "wins"
// (becomes superseded_by / current_bead_id) actually depends on which
// ordering axis is used — a single-amender fixture would pass even with a
// wrong axis (nothing to disambiguate against), so this fixture deliberately
// makes the two axes disagree:
//
//   - amendA has the NEWER clinical Timestamp (June) but was recorded_at
//     EARLIER (January) — i.e. a late-entered correction to an old value.
//   - amendB has the OLDER clinical Timestamp (mid-January) but was
//     recorded_at LATER (July) — a genuinely later correction, entered
//     promptly.
//
// Per §2, amendB (later recorded_at) must win, even though amendA has the
// "newer-looking" clinical Timestamp. This test FAILS if resolvePatientState
// (or beadOrderLess) ever sorts by bead.Bead.Timestamp instead of
// RecordedAt.
func TestResolvePatientState_OrderingUsesRecordedAtNotTimestamp(t *testing.T) {
	original := resolveBead{
		Bead: bead.Bead{
			ID: "original-fact", Type: "fhir_observation",
			Timestamp: "2026-01-01T00:00:00Z",
			Content:   map[string]any{"value": "original-reading"},
		},
		RecordedAt: "2026-01-01T00:00:00Z", RecordedAtValid: true,
	}
	amendA := resolveBead{
		Bead: bead.Bead{
			ID: "amend-a-newer-clinical-ts", Type: "fhir_observation",
			Timestamp: "2026-06-01T00:00:00Z", // clinical event: June — the LATER-looking one
			Amends:    []string{"original-fact"},
			Content:   map[string]any{"value": "amend-a-reading"},
		},
		RecordedAt: "2026-02-01T00:00:00Z", RecordedAtValid: true, // but written EARLIER: February
	}
	approveA := resolveBead{
		Bead: bead.Bead{
			ID: "approve-amend-a", Type: "attestation",
			Parents: []string{"amend-a-newer-clinical-ts"},
			Content: map[string]any{"verdict": "approved"},
		},
		RecordedAt: "2026-02-02T00:00:00Z", RecordedAtValid: true,
	}
	amendB := resolveBead{
		Bead: bead.Bead{
			ID: "amend-b-older-clinical-ts", Type: "fhir_observation",
			Timestamp: "2026-01-15T00:00:00Z", // clinical event: mid-January — OLDER-looking than amendA's
			Amends:    []string{"original-fact"},
			Content:   map[string]any{"value": "amend-b-reading"},
		},
		RecordedAt: "2026-07-01T00:00:00Z", RecordedAtValid: true, // but written LATER: July — the true §2-winner
	}
	approveB := resolveBead{
		Bead: bead.Bead{
			ID: "approve-amend-b", Type: "attestation",
			Parents: []string{"amend-b-older-clinical-ts"},
			Content: map[string]any{"verdict": "approved"},
		},
		RecordedAt: "2026-07-02T00:00:00Z", RecordedAtValid: true,
	}

	states := resolvePatientState([]resolveBead{original, amendA, approveA, amendB, approveB})

	origState := states["original-fact"]
	if origState.Status != "amended" {
		t.Fatalf("original-fact status = %q, want amended", origState.Status)
	}
	if origState.CurrentBeadID != "amend-b-older-clinical-ts" {
		t.Errorf("original-fact current_bead_id = %q, want %q (the §2-newest BY RECORDED_AT amendment, "+
			"even though it has the clinically OLDER-looking Timestamp) — "+
			"if this fails, resolution is likely comparing bead.Bead.Timestamp instead of recorded_at",
			origState.CurrentBeadID, "amend-b-older-clinical-ts")
	}
	if origState.SupersededBy != "amend-b-older-clinical-ts" {
		t.Errorf("original-fact superseded_by = %q, want %q", origState.SupersededBy, "amend-b-older-clinical-ts")
	}
}

// --- must-fix: 3-step fixed order -------------------------------------------

// TestResolvePatientState_UnattestedAmenderDoesNotSupersede is must-fix (a):
// an amends Bead with NO approved attestation must be "unattested" AND must
// NOT supersede its target — the target stays "active".
func TestResolvePatientState_UnattestedAmenderDoesNotSupersede(t *testing.T) {
	original := rb("orig", "fhir_condition", nil, nil, nil, map[string]any{"clinicalStatus": "active"}, "2026-01-01T00:00:00Z", true)
	unattestedAmendment := rb("amend-no-attestation", "fhir_condition", nil, []string{"orig"}, nil, map[string]any{"clinicalStatus": "active"}, "2026-02-01T00:00:00Z", true)

	states := resolvePatientState([]resolveBead{original, unattestedAmendment})

	if got := states["orig"].Status; got != "active" {
		t.Errorf("orig status = %q, want active (an unattested amender must not supersede its target)", got)
	}
	if got := states["orig"].CurrentBeadID; got != "orig" {
		t.Errorf("orig current_bead_id = %q, want %q (self, unaffected by the unattested amender)", got, "orig")
	}
	if got := states["amend-no-attestation"].Status; got != "unattested" {
		t.Errorf("amend-no-attestation status = %q, want unattested", got)
	}
}

// TestResolvePatientState_RejectedAttestationAlsoDoesNotSupersede is a
// variant of (a): an EXPLICIT verdict=='rejected' attestation (not just "no
// attestation at all") must also leave the amender unattested and the target
// untouched.
func TestResolvePatientState_RejectedAttestationAlsoDoesNotSupersede(t *testing.T) {
	original := rb("orig2", "fhir_condition", nil, nil, nil, map[string]any{"clinicalStatus": "active"}, "2026-01-01T00:00:00Z", true)
	amendment := rb("amend-rejected", "fhir_condition", nil, []string{"orig2"}, nil, map[string]any{"clinicalStatus": "active"}, "2026-02-01T00:00:00Z", true)
	rejection := rb("rejection-att", "attestation", []string{"amend-rejected"}, nil, nil, map[string]any{"verdict": "rejected"}, "2026-02-02T00:00:00Z", true)

	states := resolvePatientState([]resolveBead{original, amendment, rejection})

	if got := states["orig2"].Status; got != "active" {
		t.Errorf("orig2 status = %q, want active", got)
	}
	if got := states["amend-rejected"].Status; got != "unattested" {
		t.Errorf("amend-rejected status = %q, want unattested", got)
	}
	if got := states["amend-rejected"].AttestationBeadID; got != "rejection-att" {
		t.Errorf("amend-rejected attestation_bead_id = %q, want %q (audit trail of the rejecting attestation)", got, "rejection-att")
	}
}

// TestResolvePatientState_RetractedLeafTerminatesChain is must-fix (b): a
// valid, approved amendment (Y) supersedes its target (X), but Y is later
// itself retracted — X's current_bead_id must become NULL (the chain
// terminates at the retracted leaf), NOT silently fall back to X being
// "active" again.
func TestResolvePatientState_RetractedLeafTerminatesChain(t *testing.T) {
	x := rb("x-original", "fhir_condition", nil, nil, nil, map[string]any{"clinicalStatus": "active"}, "2026-01-01T00:00:00Z", true)
	y := rb("y-amendment", "fhir_condition", nil, []string{"x-original"}, nil, map[string]any{"clinicalStatus": "active"}, "2026-02-01T00:00:00Z", true)
	approveY := rb("approve-y", "attestation", []string{"y-amendment"}, nil, nil, map[string]any{"verdict": "approved"}, "2026-02-02T00:00:00Z", true)
	retractY := rb("retract-y", "retraction", []string{"y-amendment"}, nil, []string{"y-amendment"}, nil, "2026-03-01T00:00:00Z", true)

	states := resolvePatientState([]resolveBead{x, y, approveY, retractY})

	yState := states["y-amendment"]
	if yState.Status != "retracted" {
		t.Fatalf("y-amendment status = %q, want retracted", yState.Status)
	}
	if yState.CurrentBeadID != "" {
		t.Errorf("y-amendment current_bead_id = %q, want \"\" (NULL)", yState.CurrentBeadID)
	}

	xState := states["x-original"]
	if xState.Status != "amended" {
		t.Fatalf("x-original status = %q, want amended (it WAS validly superseded by y-amendment before y was retracted)", xState.Status)
	}
	if xState.SupersededBy != "y-amendment" {
		t.Errorf("x-original superseded_by = %q, want %q", xState.SupersededBy, "y-amendment")
	}
	if xState.CurrentBeadID != "" {
		t.Errorf("x-original current_bead_id = %q, want \"\" (NULL) — a retracted leaf must terminate the chain, "+
			"not revert x-original to being its own current version", xState.CurrentBeadID)
	}
}

// TestResolvePatientState_RetractionBeatsNewerAmends is must-fix (c): X is
// retracted; a NEWER (by recorded_at) approved amendment naming X in its
// Amends[] also exists. Retraction must win regardless of recency — X stays
// retracted, never "amended".
func TestResolvePatientState_RetractionBeatsNewerAmends(t *testing.T) {
	x := rb("x2-original", "fhir_condition", nil, nil, nil, map[string]any{"clinicalStatus": "active"}, "2026-01-01T00:00:00Z", true)
	retractX := rb("retract-x2", "retraction", []string{"x2-original"}, nil, []string{"x2-original"}, nil, "2026-02-01T00:00:00Z", true)
	// This amendment is §2-NEWER (later recorded_at) than the retraction, and
	// its own attestation is approved — yet it must still not resurrect x2.
	newerAmendment := rb("newer-amend-x2", "fhir_condition", nil, []string{"x2-original"}, nil, map[string]any{"clinicalStatus": "active"}, "2026-03-01T00:00:00Z", true)
	approveAmendment := rb("approve-newer-amend", "attestation", []string{"newer-amend-x2"}, nil, nil, map[string]any{"verdict": "approved"}, "2026-03-02T00:00:00Z", true)

	states := resolvePatientState([]resolveBead{x, retractX, newerAmendment, approveAmendment})

	xState := states["x2-original"]
	if xState.Status != "retracted" {
		t.Fatalf("x2-original status = %q, want retracted (retraction must beat a newer amends, per §2 'retracted 最強')", xState.Status)
	}
	if xState.CurrentBeadID != "" {
		t.Errorf("x2-original current_bead_id = %q, want \"\" (NULL)", xState.CurrentBeadID)
	}
	if xState.RetractionBeadID != "retract-x2" {
		t.Errorf("x2-original retraction_bead_id = %q, want %q", xState.RetractionBeadID, "retract-x2")
	}
}

// --- determinism -------------------------------------------------------------

// TestResolvePatientState_DeterministicAcrossInputOrder proves
// resolvePatientState's output does not depend on the order Beads are passed
// in (i.e. does not depend on Go map iteration order internally) — the same
// Bead set, permuted, must yield byte-identical (deep-equal) beadState maps.
func TestResolvePatientState_DeterministicAcrossInputOrder(t *testing.T) {
	x := rb("det-x", "fhir_condition", nil, nil, nil, map[string]any{"clinicalStatus": "active"}, "2026-01-01T00:00:00Z", true)
	y := rb("det-y", "fhir_condition", nil, []string{"det-x"}, nil, map[string]any{"clinicalStatus": "active"}, "2026-02-01T00:00:00Z", true)
	approve := rb("det-approve", "attestation", []string{"det-y"}, nil, nil, map[string]any{"verdict": "approved"}, "2026-02-02T00:00:00Z", true)
	z := rb("det-z", "fhir_observation", nil, nil, nil, map[string]any{}, "2026-01-05T00:00:00Z", true)

	order1 := []resolveBead{x, y, approve, z}
	order2 := []resolveBead{z, approve, y, x}
	order3 := []resolveBead{approve, x, z, y}

	s1 := resolvePatientState(order1)
	s2 := resolvePatientState(order2)
	s3 := resolvePatientState(order3)

	for id := range s1 {
		if s1[id] != s2[id] {
			t.Errorf("bead %s: order1=%+v order2=%+v differ", id, s1[id], s2[id])
		}
		if s1[id] != s3[id] {
			t.Errorf("bead %s: order1=%+v order3=%+v differ", id, s1[id], s3[id])
		}
	}
}

// --- plain fact with no correction ------------------------------------------

func TestResolvePatientState_PlainFactIsActiveSelf(t *testing.T) {
	fact := rb("plain-fact", "fhir_observation", nil, nil, nil, map[string]any{}, "2026-01-01T00:00:00Z", true)
	states := resolvePatientState([]resolveBead{fact})
	st := states["plain-fact"]
	if st.Status != "active" || st.CurrentBeadID != "plain-fact" {
		t.Errorf("plain-fact state = %+v, want {Status:active CurrentBeadID:plain-fact ...}", st)
	}
}
