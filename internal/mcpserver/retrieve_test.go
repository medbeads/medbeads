package mcpserver

import (
	"context"
	"testing"

	"github.com/medbeads/medbeads/internal/engine/bead"
	"github.com/medbeads/medbeads/internal/engine/clearance"
)

// TestRetrieve_SemanticWithoutEmbedder_IsAToolError checks R4.2's "embedder
// 未設定時は従来エラー" decision: a Server built with no Config.Embedder (the
// default — newServerT never sets one) must reject semantic=true with a
// clear tool-level error, not silently ignore the flag or treat it as
// semantic=false.
func TestRetrieve_SemanticWithoutEmbedder_IsAToolError(t *testing.T) {
	e := openT(t)
	seedPatient(t, e, "Semantic Patient")
	s := newServerT(t, e, SystemRole)

	res, _, err := s.retrieve(context.Background(), nil, retrieveIn{Query: "anything", Semantic: true})
	if err != nil {
		t.Fatalf("retrieve: unexpected Go error: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("retrieve(semantic=true, no embedder configured): want IsError=true result, got %+v", res)
	}
}

// TestRetrieve_BudgetControlAndTruncatedRefs seeds one patient with an
// anchor plus enough ancestor/descendant Beads that a tight token_budget
// cannot fit everything, and checks: the anchor always makes it into Items
// at L0, budget is never exceeded (UsedTokens <= BudgetTokens), and every
// Bead that did not fit still appears in TruncatedRefs (DESIGN §8's "切り捨
// て分も必ず L2 参照で列挙" — graph.BuildContext's own guarantee, exercised
// here through the MCP tool layer end to end).
func TestRetrieve_BudgetControlAndTruncatedRefs(t *testing.T) {
	e := openT(t)
	root := seedPatient(t, e, "Budget Patient")
	mid := seedChildBead(t, e, root, "fhir_encounter", nil, map[string]any{
		"note": "a reasonably long encounter note so this ancestor Bead costs a nontrivial number of estimated tokens",
	})
	anchor := seedChildBead(t, e, mid, "fhir_observation", nil, map[string]any{
		"note": "anchor observation content the query should match against directly here",
	})
	for i := 0; i < 5; i++ {
		seedChildBead(t, e, anchor, "fhir_observation", nil, map[string]any{
			"note": "a descendant Bead with enough text to cost real estimated tokens under a tight budget",
		})
	}

	s := newServerT(t, e, SystemRole)

	_, out, err := s.retrieve(context.Background(), nil, retrieveIn{
		Query:       "anchor observation content",
		TokenBudget: 60, // tight: anchor L0 fits, but ancestor+5 descendants cannot all fit too
		ChainDepth:  5,
	})
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}

	if out.UsedTokens > out.BudgetTokens {
		t.Errorf("UsedTokens = %d, exceeds BudgetTokens = %d", out.UsedTokens, out.BudgetTokens)
	}

	anchorView := bead.FormatID(anchor.ID)
	foundAnchor := false
	for _, item := range out.Items {
		if item.ID == anchorView {
			foundAnchor = true
			if item.Granularity != "L0" {
				t.Errorf("anchor item Granularity = %q, want L0", item.Granularity)
			}
			if item.Provenance != "anchor" {
				t.Errorf("anchor item Provenance = %q, want anchor", item.Provenance)
			}
		}
	}
	if !foundAnchor {
		t.Fatalf("retrieve Items does not include the anchor Bead %s; Items=%+v", anchorView, out.Items)
	}

	if len(out.TruncatedRefs) == 0 {
		t.Fatalf("retrieve with a tight budget produced zero TruncatedRefs; want at least one Bead truncated (mid + 5 descendants under budget=60)")
	}

	// Every Bead this call ever considered (anchor + ancestor + 5
	// descendants = 7) must be accounted for in Items or TruncatedRefs, but
	// never both, and never absent (DESIGN §8's "エージェントが get_bead で追
	// 加取得できる" guarantee — nothing should just vanish).
	seen := make(map[string]int)
	for _, item := range out.Items {
		seen[item.ID]++
	}
	for _, item := range out.TruncatedRefs {
		seen[item.ID]++
	}
	for id, count := range seen {
		if count != 1 {
			t.Errorf("bead %s appears %d times across Items+TruncatedRefs, want exactly 1", id, count)
		}
	}
}

// TestRetrieve_ProvenanceMatchedAntigens checks retrieve's own antigen
// provenance (beyond graph.ContextItem's built-in anchor/ancestor/sibling/
// descendant tags): an anchor selected via the antigens filter reports which
// of the requested antigens it actually matched.
func TestRetrieve_ProvenanceMatchedAntigens(t *testing.T) {
	e := openT(t)
	root := seedPatient(t, e, "Provenance Patient")
	anchor := seedChildBead(t, e, root, "fhir_medicationrequest",
		[]string{"risk:nephrotoxic", "organ:renal"},
		map[string]any{"drug": "meropenem"})

	s := newServerT(t, e, SystemRole)

	_, out, err := s.retrieve(context.Background(), nil, retrieveIn{
		Antigens: []string{"risk:nephrotoxic", "atc:unrelated"},
	})
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}

	anchorView := bead.FormatID(anchor.ID)
	var found bool
	for _, item := range out.Items {
		if item.ID != anchorView {
			continue
		}
		found = true
		if len(item.MatchedAntigens) != 1 || item.MatchedAntigens[0] != "risk:nephrotoxic" {
			t.Errorf("MatchedAntigens = %v, want [risk:nephrotoxic]", item.MatchedAntigens)
		}
	}
	if !found {
		t.Fatalf("retrieve(antigens=[risk:nephrotoxic, atc:unrelated]) did not surface anchor %s; Items=%+v", anchorView, out.Items)
	}
}

// TestRetrieve_ClearanceFilterDropsRestrictedItems checks that a viewer-role
// retrieve call silently drops a Bead it may not access (rather than
// surfacing the masked {"_restricted": true} placeholder retrieve's own
// doc comment explains is not useful to an agent that cannot act on it) —
// while a system-role call for the identical query sees it.
func TestRetrieve_ClearanceFilterDropsRestrictedItems(t *testing.T) {
	e := openT(t)
	root := seedPatient(t, e, "Filtered Patient")
	restricted := seedChildBead(t, e, root, "fhir_observation", nil, map[string]any{
		"note": "restricted lab result content uniquemarkerxyz",
	})

	if err := clearance.SaveRule(e.Index(), clearance.Rule{
		ID:          "rule-retrieve-1",
		BeadID:      restricted.ID,
		DeniedRoles: []string{"viewer"},
		CreatedBy:   "test",
		CreatedAt:   "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("clearance.SaveRule: %v", err)
	}

	viewer := newServerT(t, e, DefaultRole)
	_, viewerOut, err := viewer.retrieve(context.Background(), nil, retrieveIn{Query: "uniquemarkerxyz"})
	if err != nil {
		t.Fatalf("viewer retrieve: %v", err)
	}
	restrictedView := bead.FormatID(restricted.ID)
	for _, item := range viewerOut.Items {
		if item.ID == restrictedView {
			t.Fatalf("viewer retrieve surfaced restricted Bead %s in Items; want it dropped", restrictedView)
		}
	}
	for _, item := range viewerOut.TruncatedRefs {
		if item.ID == restrictedView {
			t.Fatalf("viewer retrieve surfaced restricted Bead %s in TruncatedRefs; want it dropped", restrictedView)
		}
	}
	// data-reviewer regression (Fix 4): the query matches the restricted
	// Bead directly, making it this call's sole anchor — AnchorIDs must be
	// filtered the same as Items/TruncatedRefs, not just report every anchor
	// retrieveAnchors found before clearance ran.
	for _, anchorID := range viewerOut.AnchorIDs {
		if anchorID == restrictedView {
			t.Fatalf("viewer retrieve leaked restricted Bead %s via AnchorIDs; want it dropped", restrictedView)
		}
	}
	if len(viewerOut.AnchorIDs) != 0 {
		t.Errorf("viewer retrieve AnchorIDs = %v, want empty (the only anchor is restricted)", viewerOut.AnchorIDs)
	}

	system := newServerT(t, e, SystemRole)
	_, systemOut, err := system.retrieve(context.Background(), nil, retrieveIn{Query: "uniquemarkerxyz"})
	if err != nil {
		t.Fatalf("system retrieve: %v", err)
	}
	found := false
	for _, item := range systemOut.Items {
		if item.ID == restrictedView {
			found = true
		}
	}
	if !found {
		t.Fatalf("system retrieve did not surface Bead %s; Items=%+v", restrictedView, systemOut.Items)
	}
	foundAnchor := false
	for _, anchorID := range systemOut.AnchorIDs {
		if anchorID == restrictedView {
			foundAnchor = true
		}
	}
	if !foundAnchor {
		t.Fatalf("system retrieve AnchorIDs = %v, want to include %s (system bypasses clearance)", systemOut.AnchorIDs, restrictedView)
	}
}

// TestRetrieve_AnchorIDsDropRestrictedAnchorAmongMultiple is Fix 4's
// dedicated regression: with two anchors matching the same query, one
// restricted and one accessible, AnchorIDs for a viewer session must
// contain only the accessible one — not the pre-filter full anchor set.
func TestRetrieve_AnchorIDsDropRestrictedAnchorAmongMultiple(t *testing.T) {
	e := openT(t)
	root := seedPatient(t, e, "Multi Anchor Patient")
	accessible := seedChildBead(t, e, root, "fhir_observation", nil, map[string]any{
		"note": "multianchormarker accessible observation",
	})
	restricted := seedChildBead(t, e, root, "fhir_observation", nil, map[string]any{
		"note": "multianchormarker restricted observation",
	})
	if err := clearance.SaveRule(e.Index(), clearance.Rule{
		ID:          "rule-multi-anchor",
		BeadID:      restricted.ID,
		DeniedRoles: []string{"viewer"},
		CreatedBy:   "test",
		CreatedAt:   "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("clearance.SaveRule: %v", err)
	}

	viewer := newServerT(t, e, DefaultRole)
	_, out, err := viewer.retrieve(context.Background(), nil, retrieveIn{Query: "multianchormarker"})
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}

	accessibleView := bead.FormatID(accessible.ID)
	restrictedView := bead.FormatID(restricted.ID)

	foundAccessible := false
	for _, anchorID := range out.AnchorIDs {
		if anchorID == restrictedView {
			t.Fatalf("viewer retrieve AnchorIDs leaked restricted anchor %s: %v", restrictedView, out.AnchorIDs)
		}
		if anchorID == accessibleView {
			foundAccessible = true
		}
	}
	if !foundAccessible {
		t.Fatalf("viewer retrieve AnchorIDs = %v, want to include the accessible anchor %s", out.AnchorIDs, accessibleView)
	}
}
