package mcpserver

import (
	"context"
	"testing"

	"github.com/medbeads/medbeads/internal/engine/bead"
	"github.com/medbeads/medbeads/internal/engine/clearance"
	"github.com/medbeads/medbeads/internal/engine/index"
	"github.com/medbeads/medbeads/internal/engine/projector"
)

// This file pins U5b's status-normalization behavior (specs/
// U5_api_retrieve.md's U5b section): retrieve's default exclusion of
// retracted/unattested Beads, its amended -> current_bead_id substitution,
// and the crux 2 empty-bead_status fallback ruling. Every fixture here runs
// projector.StatusReproject (record_state_v31) explicitly, then calls
// retrieve, mirroring record_state_test.go's own bead.Bead construction
// conventions (Amends/Retracts/attestation Parents) rather than
// mcpserver_test.go's plain unsavedBead helper, since these tests need
// correction-chain shapes unsavedBead does not build.

// TestRetrieve_RetractedAnchorExcludedFromAllPaths is DONE MEANS (a):
// a retracted anchor must not appear in AnchorIDs, Items, or TruncatedRefs.
func TestRetrieve_RetractedAnchorExcludedFromAllPaths(t *testing.T) {
	e := openT(t)
	root := seedPatient(t, e, "Retracted Anchor Patient")
	cond := ingestT(t, e, bead.Bead{
		Type: "fhir_condition", Timestamp: nextTimestamp(), Author: "did:medbeads:doctor:1",
		Parents: []string{root.ID},
		Content: map[string]any{"clinicalStatus": "active", "note": "retractedanchor marker text"},
	})
	ingestT(t, e, bead.Bead{
		Type: "retraction", Timestamp: nextTimestamp(), Author: "did:medbeads:doctor:1",
		Parents: []string{cond.ID, root.ID}, Retracts: []string{cond.ID},
		Content: map[string]any{"reason_code": "entered-in-error", "authorized_by": "did:medbeads:doctor:1"},
	})

	if _, err := projector.StatusReproject(e.Index(), e, "test-code-v1", "2026-07-11T00:00:00Z"); err != nil {
		t.Fatalf("StatusReproject: %v", err)
	}

	s := newServerT(t, e, SystemRole)
	_, out, err := s.retrieve(context.Background(), nil, retrieveIn{
		Query:       "retractedanchor",
		TokenBudget: 4000,
		ChainDepth:  5,
	})
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}

	condView := bead.FormatID(cond.ID)
	for _, id := range out.AnchorIDs {
		if id == condView {
			t.Errorf("AnchorIDs leaked retracted anchor %s: %v", condView, out.AnchorIDs)
		}
	}
	if len(out.AnchorIDs) != 0 {
		t.Errorf("AnchorIDs = %v, want empty (the only match is retracted)", out.AnchorIDs)
	}
	for _, item := range out.Items {
		if item.ID == condView {
			t.Errorf("Items leaked retracted Bead %s: %+v", condView, out.Items)
		}
	}
	for _, ref := range out.TruncatedRefs {
		if ref.ID == condView {
			t.Errorf("TruncatedRefs leaked retracted Bead %s: %+v", condView, out.TruncatedRefs)
		}
	}
}

// TestRetrieve_AmendedAnchorSubstitutedWithCurrent is DONE MEANS (b), first
// half: a validly-amended anchor is replaced with its current_bead_id
// (the amender) rather than surfacing the stale original.
func TestRetrieve_AmendedAnchorSubstitutedWithCurrent(t *testing.T) {
	e := openT(t)
	root := seedPatient(t, e, "Amended Anchor Patient")
	original := ingestT(t, e, bead.Bead{
		Type: "fhir_condition", Timestamp: nextTimestamp(), Author: "did:medbeads:doctor:1",
		Parents: []string{root.ID},
		Content: map[string]any{"clinicalStatus": "active", "note": "amendmarker original text"},
	})
	amendment := ingestT(t, e, bead.Bead{
		Type: "fhir_condition", Timestamp: nextTimestamp(), Author: "did:medbeads:doctor:1",
		Parents: []string{root.ID}, Amends: []string{original.ID},
		Content: map[string]any{"clinicalStatus": "active", "note": "amendmarker corrected text"},
	})
	ingestT(t, e, bead.Bead{
		Type: "attestation", Timestamp: nextTimestamp(), Author: "did:medbeads:doctor:2",
		Parents: []string{amendment.ID, root.ID},
		Content: map[string]any{"verdict": "approved"},
	})

	if _, err := projector.StatusReproject(e.Index(), e, "test-code-v1", "2026-07-11T00:00:00Z"); err != nil {
		t.Fatalf("StatusReproject: %v", err)
	}

	s := newServerT(t, e, SystemRole)
	_, out, err := s.retrieve(context.Background(), nil, retrieveIn{
		Query:       "amendmarker",
		TokenBudget: 4000,
		ChainDepth:  5,
	})
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}

	originalView := bead.FormatID(original.ID)
	amendmentView := bead.FormatID(amendment.ID)

	for _, id := range out.AnchorIDs {
		if id == originalView {
			t.Errorf("AnchorIDs surfaced the stale original %s, want it substituted with %s: %v", originalView, amendmentView, out.AnchorIDs)
		}
	}
	foundAmendment := false
	for _, id := range out.AnchorIDs {
		if id == amendmentView {
			foundAmendment = true
		}
	}
	if !foundAmendment {
		t.Errorf("AnchorIDs = %v, want to include current_bead_id %s", out.AnchorIDs, amendmentView)
	}
	for _, item := range out.Items {
		if item.ID == originalView {
			t.Errorf("Items surfaced the stale original %s", originalView)
		}
	}
}

// TestRetrieve_AmendedWithNullCurrent_Dropped is DONE MEANS (b), second half
// (must-fix): an amended Bead whose correction chain terminates at a
// retracted leaf (current_bead_id NULL) must be DROPPED, not substituted to
// an empty/invalid ID.
func TestRetrieve_AmendedWithNullCurrent_Dropped(t *testing.T) {
	e := openT(t)
	root := seedPatient(t, e, "Amended Null Current Patient")
	original := ingestT(t, e, bead.Bead{
		Type: "fhir_condition", Timestamp: nextTimestamp(), Author: "did:medbeads:doctor:1",
		Parents: []string{root.ID},
		Content: map[string]any{"clinicalStatus": "active", "note": "nullcurrentmarker original"},
	})
	amendment := ingestT(t, e, bead.Bead{
		Type: "fhir_condition", Timestamp: nextTimestamp(), Author: "did:medbeads:doctor:1",
		Parents: []string{root.ID}, Amends: []string{original.ID},
		Content: map[string]any{"clinicalStatus": "active", "note": "nullcurrentmarker corrected"},
	})
	ingestT(t, e, bead.Bead{
		Type: "attestation", Timestamp: nextTimestamp(), Author: "did:medbeads:doctor:2",
		Parents: []string{amendment.ID, root.ID},
		Content: map[string]any{"verdict": "approved"},
	})
	// Retract the amendment itself: the chain's leaf is now retracted, so
	// original's resolved current_bead_id must be NULL (resolve.go's
	// chainLeaf), not the retracted amendment's ID.
	ingestT(t, e, bead.Bead{
		Type: "retraction", Timestamp: nextTimestamp(), Author: "did:medbeads:doctor:1",
		Parents: []string{amendment.ID, root.ID}, Retracts: []string{amendment.ID},
		Content: map[string]any{"reason_code": "entered-in-error", "authorized_by": "did:medbeads:doctor:1"},
	})

	if _, err := projector.StatusReproject(e.Index(), e, "test-code-v1", "2026-07-11T00:00:00Z"); err != nil {
		t.Fatalf("StatusReproject: %v", err)
	}

	s := newServerT(t, e, SystemRole)
	_, out, err := s.retrieve(context.Background(), nil, retrieveIn{
		Query:       "nullcurrentmarker",
		TokenBudget: 4000,
		ChainDepth:  5,
	})
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}

	originalView := bead.FormatID(original.ID)
	amendmentView := bead.FormatID(amendment.ID)
	for _, id := range out.AnchorIDs {
		if id == originalView || id == amendmentView {
			t.Errorf("AnchorIDs = %v, want neither the amended-to-nothing original %s nor the retracted amendment %s", out.AnchorIDs, originalView, amendmentView)
		}
	}
	if len(out.AnchorIDs) != 0 {
		t.Errorf("AnchorIDs = %v, want empty (both Beads resolve to drop)", out.AnchorIDs)
	}
}

// TestResolveStatus_Decisions pins resolveStatus at the decision boundary
// itself (not via retrieve's end output), because the end-output assertion in
// TestRetrieve_AmendedWithNullCurrent_Dropped cannot distinguish "drop" from
// "substitute to an empty ID that downstream GetBead("") silently discards"
// (data-reviewer留保, 2026-07-11 + failure-catalog #14). This asserts the
// resolveStatusDecision each §2 status yields directly, so a regression that
// turns the amended-NULL drop into an empty-ID substitution FAILS here even
// though the retrieve end output would still look empty.
func TestResolveStatus_Decisions(t *testing.T) {
	const self = "sha256:selfbead"
	const current = "sha256:currentbead"

	cases := []struct {
		name              string
		st                index.BeadStatusRow
		hasStatus         bool
		includeUnattested bool
		want              resolveStatusDecision
	}{
		{"absent means active", index.BeadStatusRow{}, false, false,
			resolveStatusDecision{resolvedID: self}},
		{"active keeps self", index.BeadStatusRow{Status: "active"}, true, false,
			resolveStatusDecision{resolvedID: self}},
		{"retracted drops", index.BeadStatusRow{Status: "retracted"}, true, false,
			resolveStatusDecision{drop: true}},
		{"unattested drops by default", index.BeadStatusRow{Status: "unattested"}, true, false,
			resolveStatusDecision{drop: true}},
		{"unattested surfaced with flag", index.BeadStatusRow{Status: "unattested"}, true, true,
			resolveStatusDecision{resolvedID: self, notForClinicalAction: true}},
		{"amended substitutes to current", index.BeadStatusRow{Status: "amended", CurrentBeadID: current}, true, false,
			resolveStatusDecision{resolvedID: current}},
		// The留保 case: amended whose current_bead_id is NULL (retracted-chain
		// leaf) must DROP, not substitute to "". Asserted at the decision, so a
		// mutation to {resolvedID: ""} (empty substitution) is caught here.
		{"amended with NULL current drops", index.BeadStatusRow{Status: "amended", CurrentBeadID: ""}, true, false,
			resolveStatusDecision{drop: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveStatus(self, tc.st, tc.hasStatus, tc.includeUnattested)
			if got != tc.want {
				t.Errorf("resolveStatus = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestRetrieve_UnattestedExcludedByDefault_IncludedWithFlag is DONE MEANS
// (c): an unattested Bead is excluded by default, and surfaced with
// not_for_clinical_action=true under include_unattested=true — while still
// being clearance-filtered (checked via a second, restricted-role call).
func TestRetrieve_UnattestedExcludedByDefault_IncludedWithFlag(t *testing.T) {
	e := openT(t)
	root := seedPatient(t, e, "Unattested Patient")
	// A Type=="assessment" Bead requires attestation (resolve.go's
	// requiresAttestation) and none is provided here, so it resolves
	// unattested.
	assessment := ingestT(t, e, bead.Bead{
		Type: "assessment", Timestamp: nextTimestamp(), Author: "did:medbeads:doctor:1",
		Parents: []string{root.ID},
		Content: map[string]any{"note": "unattestedmarker draft assessment"},
	})

	if _, err := projector.StatusReproject(e.Index(), e, "test-code-v1", "2026-07-11T00:00:00Z"); err != nil {
		t.Fatalf("StatusReproject: %v", err)
	}

	s := newServerT(t, e, SystemRole)

	_, defaultOut, err := s.retrieve(context.Background(), nil, retrieveIn{
		Query:       "unattestedmarker",
		TokenBudget: 4000,
		ChainDepth:  5,
	})
	if err != nil {
		t.Fatalf("retrieve (default): %v", err)
	}
	assessmentView := bead.FormatID(assessment.ID)
	for _, id := range defaultOut.AnchorIDs {
		if id == assessmentView {
			t.Errorf("default retrieve AnchorIDs leaked unattested Bead %s: %v", assessmentView, defaultOut.AnchorIDs)
		}
	}
	if len(defaultOut.AnchorIDs) != 0 {
		t.Errorf("default retrieve AnchorIDs = %v, want empty (unattested excluded by default)", defaultOut.AnchorIDs)
	}

	includeTrue := true
	_, includedOut, err := s.retrieve(context.Background(), nil, retrieveIn{
		Query:             "unattestedmarker",
		TokenBudget:       4000,
		ChainDepth:        5,
		IncludeUnattested: &includeTrue,
	})
	if err != nil {
		t.Fatalf("retrieve (include_unattested=true): %v", err)
	}
	foundAnchor := false
	for _, id := range includedOut.AnchorIDs {
		if id == assessmentView {
			foundAnchor = true
		}
	}
	if !foundAnchor {
		t.Fatalf("retrieve(include_unattested=true) AnchorIDs = %v, want to include %s", includedOut.AnchorIDs, assessmentView)
	}
	foundItem := false
	for _, item := range includedOut.Items {
		if item.ID == assessmentView {
			foundItem = true
			if !item.NotForClinicalAction {
				t.Errorf("item %s NotForClinicalAction = false, want true (unattested)", assessmentView)
			}
		}
	}
	if !foundItem {
		t.Fatalf("retrieve(include_unattested=true) Items missing %s: %+v", assessmentView, includedOut.Items)
	}
}

// TestRetrieve_UnattestedStillClearanceFiltered checks that
// include_unattested=true does NOT bypass clearance: a restricted unattested
// Bead is still dropped for a viewer role even though the caller explicitly
// asked to see unattested Beads.
func TestRetrieve_UnattestedStillClearanceFiltered(t *testing.T) {
	e := openT(t)
	root := seedPatient(t, e, "Unattested Clearance Patient")
	assessment := ingestT(t, e, bead.Bead{
		Type: "assessment", Timestamp: nextTimestamp(), Author: "did:medbeads:doctor:1",
		Parents: []string{root.ID},
		Content: map[string]any{"note": "restrictedunattestedmarker draft assessment"},
	})
	if err := clearance.SaveRule(e.Index(), clearance.Rule{
		ID:          "rule-" + assessment.ID,
		BeadID:      assessment.ID,
		DeniedRoles: []string{"viewer"},
		CreatedBy:   "test",
		CreatedAt:   "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("SaveRule: %v", err)
	}

	if _, err := projector.StatusReproject(e.Index(), e, "test-code-v1", "2026-07-11T00:00:00Z"); err != nil {
		t.Fatalf("StatusReproject: %v", err)
	}

	viewer := newServerT(t, e, DefaultRole)
	includeTrue := true
	_, out, err := viewer.retrieve(context.Background(), nil, retrieveIn{
		Query:             "restrictedunattestedmarker",
		TokenBudget:       4000,
		ChainDepth:        5,
		IncludeUnattested: &includeTrue,
	})
	if err != nil {
		t.Fatalf("viewer retrieve: %v", err)
	}
	assessmentView := bead.FormatID(assessment.ID)
	for _, id := range out.AnchorIDs {
		if id == assessmentView {
			t.Errorf("viewer retrieve(include_unattested=true) leaked restricted Bead %s via AnchorIDs", assessmentView)
		}
	}
	for _, item := range out.Items {
		if item.ID == assessmentView {
			t.Errorf("viewer retrieve(include_unattested=true) leaked restricted Bead %s via Items", assessmentView)
		}
	}
}

// TestRetrieve_EmptyBeadStatus_ReturnsNormalRetrieve is DONE MEANS (d) / crux
// 2: a store where StatusReproject has never run (bead_status is completely
// empty) must still return a normal, all-active retrieve — not an
// empty/broken one — per specs/U5_api_retrieve.md's user-arbitrated
// "absent = active" fallback.
func TestRetrieve_EmptyBeadStatus_ReturnsNormalRetrieve(t *testing.T) {
	e := openT(t)
	root := seedPatient(t, e, "Never Reprojected Patient")
	obs := seedChildBead(t, e, root, "fhir_observation", nil, map[string]any{
		"note": "neverreprojectedmarker observation content",
	})

	var n int
	if err := e.Index().SQLDB().QueryRow(`SELECT COUNT(*) FROM bead_status`).Scan(&n); err != nil {
		t.Fatalf("count bead_status: %v", err)
	}
	if n != 0 {
		t.Fatalf("precondition failed: bead_status has %d rows, want 0 (StatusReproject must not have run)", n)
	}

	s := newServerT(t, e, SystemRole)
	_, out, err := s.retrieve(context.Background(), nil, retrieveIn{
		Query:       "neverreprojectedmarker",
		TokenBudget: 4000,
		ChainDepth:  5,
	})
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}

	obsView := bead.FormatID(obs.ID)
	found := false
	for _, item := range out.Items {
		if item.ID == obsView {
			found = true
		}
	}
	if !found {
		t.Fatalf("retrieve on a store with empty bead_status returned no matching Items (want the anchor surfaced as active-by-default): %+v", out.Items)
	}
	foundAnchor := false
	for _, id := range out.AnchorIDs {
		if id == obsView {
			foundAnchor = true
		}
	}
	if !foundAnchor {
		t.Fatalf("retrieve on a store with empty bead_status returned no matching AnchorIDs: %v", out.AnchorIDs)
	}
}
