package graph_test

import (
	"testing"

	"github.com/medbeads/medbeads/internal/engine/graph"
)

// buildTestBundle seeds: root (patient_registration)
//
//	└─ mid (fhir_encounter)          <- ancestor of anchor
//	    └─ anchor (fhir_observation) <- the anchor bead itself
//	        └─ descendant (fhir_observation) <- child of anchor
//
// Returns the loaded Bundle and every Bead's ID by role, for BuildContext
// priority-order assertions. U5a (specs/U5_api_retrieve.md) removed graph's
// sibling tiers along with package apc entirely, so this bundle shape
// (unlike its pre-U5a ancestor) carries no implicit/explicit sibling Beads
// at all — BuildContext now only ever produces anchor/ancestor/descendant
// tiers.
func buildTestBundle(t *testing.T) (*graph.Bundle, map[string]string) {
	t.Helper()
	e := openT(t)

	root := seedPatient(t, e, "root patient")
	mid := seedChildBead(t, e, root, "fhir_encounter", map[string]any{"n": "mid encounter note text long enough to matter for token sizing maybe"})
	anchor := seedChildBead(t, e, mid, "fhir_observation", map[string]any{"n": "anchor observation content, the thing the agent asked about specifically"})
	descendant := seedChildBead(t, e, anchor, "fhir_observation", map[string]any{"n": "descendant of anchor observation"})

	bd, err := graph.LoadBundle(storeFor(e), root.ID)
	if err != nil {
		t.Fatalf("LoadBundle: %v", err)
	}

	ids := map[string]string{
		"root":       root.ID,
		"mid":        mid.ID,
		"anchor":     anchor.ID,
		"descendant": descendant.ID,
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
// L1, descendant L2 (per specs/DESIGN_v3.md §8's ordering, minus the
// sibling_link-description and explicit/implicit sibling tiers — see
// context.go's tierGranularity doc comment; U5a removed those along with
// package apc).
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
	// slop, but not enough for the descendant's L2 reference too — use an
	// exact-anchor-plus-one-ancestor budget and check that ancestor (not
	// tested here) demotion; instead exercise demotion via a second
	// ancestor-tier Bead added deliberately tight against budget.
	generous := graph.BuildContext(bd, []string{ids["anchor"]}, 10_000, 10, 10)
	anchorItem, _ := itemFor(generous, ids["anchor"])
	rootItem, _ := itemFor(generous, ids["root"])

	// Budget covers anchor's L0 and root's L1 only, with a sliver left over
	// (not enough for mid's own L1 too) — mid, whose ancestor tier is tied
	// with root's, should get demoted to L2 given the same tier priority
	// but insufficient remaining budget, exactly as a lower tier would.
	budget := anchorItem.EstimatedTokens + rootItem.EstimatedTokens + 16

	cb := graph.BuildContext(bd, []string{ids["anchor"]}, budget, 10, 10)

	gotAnchor, ok := itemFor(cb, ids["anchor"])
	if !ok || gotAnchor.Granularity != graph.GranularityL0 {
		t.Fatalf("anchor should still be L0 under a tight-but-anchor-sized budget, got %+v ok=%v", gotAnchor, ok)
	}

	// root sorts before mid within the ancestor tier (seeded earlier, so an
	// earlier timestamp), so it should still fit at L1; mid — same tier,
	// later in sort order — should be demoted to L2 rather than dropped
	// outright, since an L2 reference is cheap enough to fit in the
	// remaining slop.
	gotRoot, ok := itemFor(cb, ids["root"])
	if !ok || gotRoot.Granularity != graph.GranularityL1 {
		t.Fatalf("root ancestor should still be L1 under this budget, got %+v ok=%v", gotRoot, ok)
	}
	gotMid, ok := itemFor(cb, ids["mid"])
	if !ok {
		t.Fatalf("mid ancestor should be present (demoted to L2), got TruncatedRefs=%+v Items=%+v", cb.TruncatedRefs, cb.Items)
	}
	if gotMid.Granularity != graph.GranularityL2 {
		t.Errorf("mid ancestor granularity = %s, want L2 (demoted)", gotMid.Granularity)
	}
	if gotMid.Text != "" {
		t.Errorf("mid ancestor L2 item should carry no Text, got %q", gotMid.Text)
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
	// other tier's candidates (ancestors, descendant) cannot even afford an
	// L2 reference (a fixed 15-token floor) and must be truncated.
	budget := anchorItem.EstimatedTokens

	cb := graph.BuildContext(bd, []string{ids["anchor"]}, budget, 10, 10)

	if len(cb.Items) != 1 || cb.Items[0].ID != ids["anchor"] {
		t.Fatalf("Items = %+v, want exactly the anchor", cb.Items)
	}
	for _, want := range []string{"root", "mid", "descendant"} {
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

// TestBuildContext_MultiAnchor_CrossAnchorTierPromotion_NoDuplicate is the
// Go-level regression for a real duplicate-ContextItem bug found via
// bench/'s dag_full/dag_nosib comparison integration test (multi-anchor
// semantic=true retrieve calls, up to 50 anchors): a Bead reachable at a
// low-priority tier (e.g. descendant, tier 2) via one anchor, and later
// re-reached at a *higher*-priority tier (e.g. ancestor, tier 1) via a later
// anchor in the same anchors slice, must appear exactly once in Items — at
// its final, highest-priority tier — never twice.
//
// Reproduction shape: anchor1 -> descendant walk reaches shared (tier 2,
// first, in the old single-pass "claim" design); anchor2 (processed after
// anchor1, with shared as its own ancestor) reaches shared via the ancestor
// tier (tier 1) — a strictly *better* tier than the one shared was already
// claimed at. The old bug (pre-U5a, when there were 5 tiers including two
// sibling tiers): seen[shared] only blocked a re-claim into an
// equal-or-*worse* tier, so a later, better claim overwrote seen[shared] and
// appended a *second* candidate to the better tier, leaving the original
// stale entry in place too — shared ended up in Items twice.
func TestBuildContext_MultiAnchor_CrossAnchorTierPromotion_NoDuplicate(t *testing.T) {
	e := openT(t)

	root := seedPatient(t, e, "cross-tier promotion patient")
	anchor1 := seedChildBead(t, e, root, "fhir_encounter", map[string]any{"n": "anchor1, shared is its descendant"})
	shared := seedChildBead(t, e, anchor1, "fhir_observation", map[string]any{"n": "shared bead: anchor1's descendant AND anchor2's ancestor"})
	anchor2 := seedChildBead(t, e, shared, "fhir_medicationrequest", map[string]any{"n": "anchor2, shared is its ancestor"})

	bd, err := graph.LoadBundle(storeFor(e), root.ID)
	if err != nil {
		t.Fatalf("LoadBundle: %v", err)
	}

	// anchor1 first, anchor2 second: anchor1's descendant walk (tier 2)
	// claims `shared` before anchor2's ancestor walk (tier 1) does —
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
	if sharedItem.Provenance != graph.ProvenanceAncestor {
		t.Errorf("shared Bead Provenance = %s, want ancestor (its final, highest-priority tier — not descendant, its first-claimed tier)", sharedItem.Provenance)
	}
	if sharedItem.Granularity != graph.GranularityL1 {
		t.Errorf("shared Bead Granularity = %s, want L1 (the ancestor tier's granularity)", sharedItem.Granularity)
	}

	// UsedTokens must equal the sum of Items' own EstimatedTokens exactly —
	// this is only guaranteed once `shared` is packed exactly once (a stale
	// duplicate entry would have paid its EstimatedTokens cost a second
	// time against remaining/UsedTokens without a second Items entry to
	// show for it, silently wasting budget the caller could never account
	// for from the response alone).
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
