package graph_test

import (
	"testing"

	"github.com/medbeads/medbeads/internal/engine/graph"
)

// buildTestBundle seeds: root (patient_registration)
//
//	└─ mid (fhir_encounter)          <- ancestor of anchor
//	    └─ anchor (fhir_observation) <- the anchor bead itself
//	    └─ implicitSib (fhir_observation) <- shares parent `mid` with anchor
//	        └─ descendant (fhir_observation) <- child of anchor
//
// plus an explicit sibling edge between anchor and explicitSib (a Bead with
// no other relation to anchor). Returns the loaded Bundle and every Bead's
// ID by role, for BuildContext priority-order assertions.
func buildTestBundle(t *testing.T) (*graph.Bundle, map[string]string) {
	t.Helper()
	e := openT(t)

	root := seedPatient(t, e, "root patient")
	mid := seedChildBead(t, e, root, "fhir_encounter", map[string]any{"n": "mid encounter note text long enough to matter for token sizing maybe"})
	anchor := seedChildBead(t, e, mid, "fhir_observation", map[string]any{"n": "anchor observation content, the thing the agent asked about specifically"})
	implicitSib := seedChildBead(t, e, mid, "fhir_observation", map[string]any{"n": "implicit sibling sharing parent mid with anchor"})
	descendant := seedChildBead(t, e, anchor, "fhir_observation", map[string]any{"n": "descendant of anchor observation"})
	explicitSib := seedChildBead(t, e, root, "fhir_medicationrequest", map[string]any{"n": "explicit sibling linked via AddSiblingEdge, unrelated by parentage"})

	bd, err := graph.LoadBundle(storeFor(e), root.ID)
	if err != nil {
		t.Fatalf("LoadBundle: %v", err)
	}
	bd.AddSiblingEdge(anchor.ID, explicitSib.ID)

	ids := map[string]string{
		"root":        root.ID,
		"mid":         mid.ID,
		"anchor":      anchor.ID,
		"implicitSib": implicitSib.ID,
		"descendant":  descendant.ID,
		"explicitSib": explicitSib.ID,
	}
	return bd, ids
}

func itemFor(cb graph.ContextBundle, id string) (graph.ContextItem, bool) {
	for _, it := range cb.Items {
		if it.ID == id {
			return it, true
		}
	}
	return graph.ContextItem{}, false
}

func truncatedFor(cb graph.ContextBundle, id string) (graph.ContextItem, bool) {
	for _, it := range cb.TruncatedRefs {
		if it.ID == id {
			return it, true
		}
	}
	return graph.ContextItem{}, false
}

// TestBuildContext_GenerousBudget_PriorityOrder checks that with ample
// budget, every tier gets its documented granularity: anchor L0, ancestor
// L1, explicit sibling L1, implicit sibling L2, descendant L2 (per
// specs/DESIGN_v3.md §8's ordering, minus the sibling_link-description tier
// — see context.go's tierGranularity doc comment).
func TestBuildContext_GenerousBudget_PriorityOrder(t *testing.T) {
	bd, ids := buildTestBundle(t)

	cb := graph.BuildContext(bd, []string{ids["anchor"]}, 10_000, 10, 10)

	anchorItem, ok := itemFor(cb, ids["anchor"])
	if !ok {
		t.Fatalf("anchor missing from Items: %+v", cb)
	}
	if anchorItem.Granularity != graph.GranularityL0 {
		t.Errorf("anchor granularity = %s, want L0", anchorItem.Granularity)
	}
	if anchorItem.Provenance != graph.ProvenanceAnchor {
		t.Errorf("anchor provenance = %s, want anchor", anchorItem.Provenance)
	}

	midItem, ok := itemFor(cb, ids["mid"])
	if !ok {
		t.Fatalf("ancestor (mid) missing from Items: %+v", cb)
	}
	if midItem.Granularity != graph.GranularityL1 {
		t.Errorf("ancestor granularity = %s, want L1", midItem.Granularity)
	}
	if midItem.Provenance != graph.ProvenanceAncestor {
		t.Errorf("ancestor provenance = %s, want ancestor", midItem.Provenance)
	}

	explicitItem, ok := itemFor(cb, ids["explicitSib"])
	if !ok {
		t.Fatalf("explicit sibling missing from Items: %+v", cb)
	}
	if explicitItem.Granularity != graph.GranularityL1 {
		t.Errorf("explicit sibling granularity = %s, want L1", explicitItem.Granularity)
	}

	implicitItem, ok := itemFor(cb, ids["implicitSib"])
	if !ok {
		t.Fatalf("implicit sibling missing from Items: %+v", cb)
	}
	if implicitItem.Granularity != graph.GranularityL2 {
		t.Errorf("implicit sibling granularity = %s, want L2", implicitItem.Granularity)
	}

	descItem, ok := itemFor(cb, ids["descendant"])
	if !ok {
		t.Fatalf("descendant missing from Items: %+v", cb)
	}
	if descItem.Granularity != graph.GranularityL2 {
		t.Errorf("descendant granularity = %s, want L2", descItem.Granularity)
	}
	if descItem.Provenance != graph.ProvenanceDescendant {
		t.Errorf("descendant provenance = %s, want descendant", descItem.Provenance)
	}

	if len(cb.TruncatedRefs) != 0 {
		t.Errorf("TruncatedRefs = %+v, want none with a generous budget", cb.TruncatedRefs)
	}
	if cb.UsedTokens <= 0 || cb.UsedTokens > cb.BudgetTokens {
		t.Errorf("UsedTokens = %d, want in (0, %d]", cb.UsedTokens, cb.BudgetTokens)
	}
}

// TestBuildContext_TightBudget_LowerPriorityDemotedToL2 checks that a budget
// too small for every tier's preferred granularity demotes lower-priority
// items to L2 before dropping them, per DESIGN §8's greedy-packing +
// fallback-to-reference behavior.
func TestBuildContext_TightBudget_LowerPriorityDemotedToL2(t *testing.T) {
	bd, ids := buildTestBundle(t)

	// Budget covers the anchor's L0 plus every ancestor's L1 (anchor's
	// ancestors are both root and mid — root sorts first within the
	// ancestor tier since it was ingested earlier) plus a little L2-sized
	// slop, but not enough for the explicit sibling's L1 text too. Anchor's
	// L0 first, so measure it (and every real ancestor) to size the rest of
	// the budget precisely enough to force a demotion.
	generous := graph.BuildContext(bd, []string{ids["anchor"]}, 10_000, 10, 10)
	anchorItem, _ := itemFor(generous, ids["anchor"])
	rootItem, _ := itemFor(generous, ids["root"])
	midItem, _ := itemFor(generous, ids["mid"])

	budget := anchorItem.EstimatedTokens + rootItem.EstimatedTokens + midItem.EstimatedTokens + 16

	cb := graph.BuildContext(bd, []string{ids["anchor"]}, budget, 10, 10)

	gotAnchor, ok := itemFor(cb, ids["anchor"])
	if !ok || gotAnchor.Granularity != graph.GranularityL0 {
		t.Fatalf("anchor should still be L0 under a tight-but-anchor-sized budget, got %+v ok=%v", gotAnchor, ok)
	}
	gotMid, ok := itemFor(cb, ids["mid"])
	if !ok || gotMid.Granularity != graph.GranularityL1 {
		t.Fatalf("ancestor should still be L1 under this budget, got %+v ok=%v", gotMid, ok)
	}

	// The explicit sibling (tier below ancestor) should have been demoted
	// to L2 rather than dropped outright, since an L2 reference is cheap
	// enough to fit in the remaining slop.
	gotExplicit, ok := itemFor(cb, ids["explicitSib"])
	if !ok {
		t.Fatalf("explicit sibling should be present (demoted to L2), got TruncatedRefs=%+v Items=%+v", cb.TruncatedRefs, cb.Items)
	}
	if gotExplicit.Granularity != graph.GranularityL2 {
		t.Errorf("explicit sibling granularity = %s, want L2 (demoted)", gotExplicit.Granularity)
	}
	if gotExplicit.Text != "" {
		t.Errorf("explicit sibling L2 item should carry no Text, got %q", gotExplicit.Text)
	}
}

// TestBuildContext_ExtremelyTightBudget_FallsToTruncatedRefs checks that
// once even an L2 reference cannot fit, an item is reported in
// TruncatedRefs rather than silently vanishing.
func TestBuildContext_ExtremelyTightBudget_FallsToTruncatedRefs(t *testing.T) {
	bd, ids := buildTestBundle(t)

	generous := graph.BuildContext(bd, []string{ids["anchor"]}, 10_000, 10, 10)
	anchorItem, _ := itemFor(generous, ids["anchor"])

	// Budget covers exactly the anchor's L0 cost and nothing more: every
	// other tier's candidates (ancestor, siblings, descendant) cannot even
	// afford an L2 reference (a fixed 15-token floor) and must be
	// truncated.
	budget := anchorItem.EstimatedTokens

	cb := graph.BuildContext(bd, []string{ids["anchor"]}, budget, 10, 10)

	if len(cb.Items) != 1 || cb.Items[0].ID != ids["anchor"] {
		t.Fatalf("Items = %+v, want exactly the anchor", cb.Items)
	}
	for _, want := range []string{"mid", "implicitSib", "descendant", "explicitSib"} {
		ref, ok := truncatedFor(cb, ids[want])
		if !ok {
			t.Errorf("TruncatedRefs missing %s (%s)", want, ids[want])
			continue
		}
		if ref.Granularity != graph.GranularityL2 {
			t.Errorf("TruncatedRefs[%s].Granularity = %s, want L2", want, ref.Granularity)
		}
	}
}

// TestBuildContext_ZeroBudget_StillReturnsAnchorL2Reference is the
// task-mandated "予算 0 でも anchor の L2 参照は返る（空応答にしない）"
// guarantee: even a zero-token budget must not produce a completely empty
// ContextBundle — the anchor itself is always at least an L2 reference,
// surfaced via TruncatedRefs.
func TestBuildContext_ZeroBudget_StillReturnsAnchorL2Reference(t *testing.T) {
	bd, ids := buildTestBundle(t)

	cb := graph.BuildContext(bd, []string{ids["anchor"]}, 0, 10, 10)

	if len(cb.Items) != 0 {
		t.Errorf("Items = %+v, want none at budget=0", cb.Items)
	}
	ref, ok := truncatedFor(cb, ids["anchor"])
	if !ok {
		t.Fatalf("anchor missing from TruncatedRefs at budget=0: %+v", cb)
	}
	if ref.ID != ids["anchor"] || ref.Type == "" || ref.Timestamp == "" {
		t.Errorf("anchor L2 ref incomplete: %+v", ref)
	}
	if cb.UsedTokens != 0 {
		t.Errorf("UsedTokens = %d, want 0 at budget=0", cb.UsedTokens)
	}
}

// TestBuildContext_DeduplicatesAcrossTiers checks that a Bead reachable via
// more than one path (e.g. both as an ancestor of one anchor and a
// descendant of another) is only counted/included once, at its
// highest-priority tier.
func TestBuildContext_DeduplicatesAcrossTiers(t *testing.T) {
	bd, ids := buildTestBundle(t)

	// mid is an ancestor of anchor; use both anchor and mid itself as
	// anchors so mid would otherwise be claimed both as an anchor (tier 0)
	// and as anchor's ancestor (tier 1). It must appear exactly once, at
	// the anchor tier (L0), not duplicated.
	cb := graph.BuildContext(bd, []string{ids["anchor"], ids["mid"]}, 10_000, 10, 10)

	count := 0
	var found graph.ContextItem
	for _, it := range cb.Items {
		if it.ID == ids["mid"] {
			count++
			found = it
		}
	}
	if count != 1 {
		t.Fatalf("mid appears %d times in Items, want exactly 1", count)
	}
	if found.Granularity != graph.GranularityL0 || found.Provenance != graph.ProvenanceAnchor {
		t.Errorf("mid (claimed as both anchor and ancestor) = %+v, want anchor-tier L0", found)
	}
}

// --- EstimateTokens ------------------------------------------------------

func TestEstimateTokens_MonotonicInLength(t *testing.T) {
	short := graph.EstimateTokens("abc")
	long := graph.EstimateTokens("abcdefghijklmnopqrstuvwxyz")
	if long <= short {
		t.Errorf("EstimateTokens(long) = %d, want > EstimateTokens(short) = %d", long, short)
	}
	if got := graph.EstimateTokens(""); got != 0 {
		t.Errorf("EstimateTokens(\"\") = %d, want 0", got)
	}
}
