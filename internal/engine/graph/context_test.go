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

// TestBuildContext_WithSiblingsFalse_SkipsBothSiblingTiers checks
// graph.WithSiblings(false): with a generous budget, both the explicit
// (sibling_link) and implicit (same-parent) sibling tiers are omitted
// entirely — neither in Items nor TruncatedRefs — while every other tier
// (anchor, ancestor, descendant) is unaffected, matching bench/'s dag_nosib
// arm's need to isolate DAG traversal without sibling_link contribution
// (docs/requirements.md R8.2).
func TestBuildContext_WithSiblingsFalse_SkipsBothSiblingTiers(t *testing.T) {
	bd, ids := buildTestBundle(t)

	cbFull := graph.BuildContext(bd, []string{ids["anchor"]}, 10_000, 10, 10)
	if _, ok := itemFor(cbFull, ids["explicitSib"]); !ok {
		t.Fatalf("sanity: default (siblings included) call should surface explicitSib")
	}
	if _, ok := itemFor(cbFull, ids["implicitSib"]); !ok {
		t.Fatalf("sanity: default (siblings included) call should surface implicitSib")
	}

	cbNoSib := graph.BuildContext(bd, []string{ids["anchor"]}, 10_000, 10, 10, graph.WithSiblings(false))

	for _, want := range []string{"explicitSib", "implicitSib"} {
		if _, ok := itemFor(cbNoSib, ids[want]); ok {
			t.Errorf("WithSiblings(false): %s present in Items, want excluded entirely", want)
		}
		if _, ok := truncatedFor(cbNoSib, ids[want]); ok {
			t.Errorf("WithSiblings(false): %s present in TruncatedRefs, want excluded entirely (not even as an L2 ref)", want)
		}
	}

	// Non-sibling tiers must be unaffected: anchor, ancestor, descendant
	// still present exactly as in the siblings-included call.
	if _, ok := itemFor(cbNoSib, ids["anchor"]); !ok {
		t.Errorf("WithSiblings(false): anchor missing from Items")
	}
	if _, ok := itemFor(cbNoSib, ids["mid"]); !ok {
		t.Errorf("WithSiblings(false): ancestor (mid) missing from Items")
	}
	if _, ok := itemFor(cbNoSib, ids["descendant"]); !ok {
		t.Errorf("WithSiblings(false): descendant missing from Items")
	}

	// WithSiblings(false) must also mean fewer (or equal) UsedTokens than the
	// siblings-included call, since two whole tiers' worth of candidates are
	// no longer competing for budget.
	if cbNoSib.UsedTokens >= cbFull.UsedTokens {
		t.Errorf("WithSiblings(false) UsedTokens = %d, want < siblings-included UsedTokens = %d", cbNoSib.UsedTokens, cbFull.UsedTokens)
	}
}

// TestBuildContext_NoOptions_DefaultsToSiblingsIncluded checks that omitting
// opts entirely (every pre-existing call site's shape) is identical to
// explicitly passing WithSiblings(true) — BuildContext's signature change
// must not alter behavior for callers that pass no options.
func TestBuildContext_NoOptions_DefaultsToSiblingsIncluded(t *testing.T) {
	bd, ids := buildTestBundle(t)

	cbNoOpts := graph.BuildContext(bd, []string{ids["anchor"]}, 10_000, 10, 10)
	cbExplicitTrue := graph.BuildContext(bd, []string{ids["anchor"]}, 10_000, 10, 10, graph.WithSiblings(true))

	if len(cbNoOpts.Items) != len(cbExplicitTrue.Items) {
		t.Fatalf("Items length differs: no-opts=%d explicit-true=%d", len(cbNoOpts.Items), len(cbExplicitTrue.Items))
	}
	if cbNoOpts.UsedTokens != cbExplicitTrue.UsedTokens {
		t.Errorf("UsedTokens differs: no-opts=%d explicit-true=%d", cbNoOpts.UsedTokens, cbExplicitTrue.UsedTokens)
	}
}

// TestBuildContext_MultiAnchor_CrossAnchorTierPromotion_NoDuplicate is the
// Go-level regression for a real duplicate-ContextItem bug found via
// bench/'s dag_full/dag_nosib comparison integration test (multi-anchor
// semantic=true retrieve calls, up to 50 anchors): a Bead reachable at a
// low-priority tier (e.g. descendant, tier 4) via one anchor, and later
// re-reached at a *higher*-priority tier (e.g. explicit sibling, tier 2) via
// a later anchor in the same anchors slice, must appear exactly once in
// Items — at its final, highest-priority tier — never twice.
//
// Reproduction shape: anchor1 -> descendant walk reaches shared (tier 4,
// first, in the old single-pass "claim" design); anchor2 (processed after
// anchor1) explicit-sibling-links to shared (tier 2) — a strictly *better*
// tier than the one shared was already claimed at. The old bug: seen[shared]
// only blocked a re-claim into an equal-or-*worse* tier, so anchor2's better
// claim overwrote seen[shared] and appended a *second* candidate to
// tiers[2], leaving the original tiers[4] entry in place too — shared ended
// up in Items twice (once at L2/descendant, once at L1/sibling).
func TestBuildContext_MultiAnchor_CrossAnchorTierPromotion_NoDuplicate(t *testing.T) {
	e := openT(t)

	root := seedPatient(t, e, "cross-tier promotion patient")
	anchor1 := seedChildBead(t, e, root, "fhir_encounter", map[string]any{"n": "anchor1, shared is its descendant"})
	shared := seedChildBead(t, e, anchor1, "fhir_observation", map[string]any{"n": "shared bead: anchor1's descendant AND anchor2's explicit sibling"})
	anchor2 := seedChildBead(t, e, root, "fhir_medicationrequest", map[string]any{"n": "anchor2, explicit-sibling-linked to shared"})

	bd, err := graph.LoadBundle(storeFor(e), root.ID)
	if err != nil {
		t.Fatalf("LoadBundle: %v", err)
	}
	bd.AddSiblingEdge(anchor2.ID, shared.ID)

	// anchor1 first, anchor2 second: anchor1's descendant walk (tier 4)
	// claims `shared` before anchor2's explicit-sibling walk (tier 2) does —
	// exactly the ordering that triggered the old bug (a later, better
	// claim after an earlier, worse one).
	cb := graph.BuildContext(bd, []string{anchor1.ID, anchor2.ID}, 10_000, 10, 10)

	seenCount := map[string]int{}
	for _, it := range cb.Items {
		seenCount[it.ID]++
	}
	for _, it := range cb.TruncatedRefs {
		seenCount[it.ID]++
	}
	for id, count := range seenCount {
		if count != 1 {
			t.Errorf("Bead %s appears %d times across Items+TruncatedRefs, want exactly 1", id, count)
		}
	}

	sharedItem, ok := itemFor(cb, shared.ID)
	if !ok {
		t.Fatalf("shared Bead missing from Items entirely: %+v", cb)
	}
	if sharedItem.Provenance != graph.ProvenanceSibling {
		t.Errorf("shared Bead Provenance = %s, want sibling (its final, highest-priority tier — not descendant, its first-claimed tier)", sharedItem.Provenance)
	}
	if sharedItem.Granularity != graph.GranularityL1 {
		t.Errorf("shared Bead Granularity = %s, want L1 (the explicit-sibling tier's granularity)", sharedItem.Granularity)
	}

	// UsedTokens must equal the sum of Items' own EstimatedTokens exactly —
	// this is only guaranteed once `shared` is packed exactly once (a stale
	// duplicate entry would have paid its EstimatedTokens cost a second
	// time against remaining/UsedTokens without a second Items entry to
	// show for it, silently wasting budget the caller could never account
	// for from the response alone — lead's item 5, "UsedTokens が重複除去後
	// の実態と一致すること").
	var sumItemTokens int
	for _, it := range cb.Items {
		sumItemTokens += it.EstimatedTokens
	}
	if cb.UsedTokens != sumItemTokens {
		t.Errorf("UsedTokens = %d, want == sum(Items[].EstimatedTokens) = %d (a stale duplicate claim would inflate UsedTokens beyond what Items actually accounts for)", cb.UsedTokens, sumItemTokens)
	}
}

// TestBuildContext_ItemsAndTruncatedRefs_NeverContainDuplicateBeadIDs is a
// general invariant check (not tied to the specific multi-anchor
// reproduction above): across every existing BuildContext scenario this
// test file already exercises (buildTestBundle's shapes, several budgets),
// no Bead ID ever appears more than once across Items+TruncatedRefs
// combined.
func TestBuildContext_ItemsAndTruncatedRefs_NeverContainDuplicateBeadIDs(t *testing.T) {
	bd, ids := buildTestBundle(t)

	for _, budget := range []int{0, 50, 200, 10_000} {
		cb := graph.BuildContext(bd, []string{ids["anchor"], ids["mid"]}, budget, 10, 10)
		seenCount := map[string]int{}
		for _, it := range cb.Items {
			seenCount[it.ID]++
		}
		for _, it := range cb.TruncatedRefs {
			seenCount[it.ID]++
		}
		for id, count := range seenCount {
			if count != 1 {
				t.Errorf("budget=%d: Bead %s appears %d times across Items+TruncatedRefs, want exactly 1", budget, id, count)
			}
		}
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
