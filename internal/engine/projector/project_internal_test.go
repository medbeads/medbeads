package projector

import (
	"fmt"
	"testing"
)

// capBindingNamespaces is the fixed set of three trigger namespaces
// densePatientTagsForCapBinding always builds around. It is a set, not a
// priority order — callers pass their own ordered TriggerNamespaces to
// projectPatientLinks separately (see
// TestProjectPatientLinks_ReorderingTriggerNamespaces_ChangesSurvivingSet),
// so this fixture is reusable across different declared priority orders
// without itself encoding one.
var capBindingNamespaces = []string{"atc:", "risk:", "rxnorm:"}

// densePatientTagsForCapBinding builds a patientTag fixture for exactly one
// patient, dense enough that maxLinksPerBead (50) actually binds on a single
// "hub" Bead across at least two different trigger namespaces — the only
// condition under which projectPatientLinks' determinism (and, separately,
// its cap-consumption priority) can be observed. See this file's
// TestProjectPatientLinks_CapBinding_DeterministicAcrossRuns and
// TestProjectPatientLinks_ReorderingTriggerNamespaces_ChangesSurvivingSet
// doc comments for what each test proves with it.
//
// Shape: one hub Bead shares "atc:hub" with 30 distinct atc-partner Beads,
// "risk:hub" with 30 distinct risk-partner Beads, and "rxnorm:hub" with 30
// distinct rxnorm-partner Beads — 90 candidate pairs touching the hub
// across three different trigger namespaces, comfortably above the 50-link
// cap, so no matter which order those three namespaces are visited in, at
// least one namespace's pairs are provably squeezed out.
//
// Every partner Bead additionally carries a unique noise tag, and a further
// set of pure-noise Beads (no trigger tag at all) pads the patient's total
// distinct tag-bearing Bead count so that no single hub trigger tag's
// patient-local frequency (Beads-carrying-that-tag / total-distinct-tag-
// bearing-Beads) reaches frequencyThreshold (0.3) and gets excluded by the
// IDF-style filter: each of hub's three trigger tags sits on 31 Beads (hub +
// 30 partners) out of a padded total of 131, 31/131 ≈ 23.7%, safely under
// 30%.
func densePatientTagsForCapBinding() []patientTag {
	const partnersPerNamespace = 30
	// paddingNoiseBeads: extra pure-noise Beads (own unique loinc: tag only,
	// no trigger-namespace tag) so total distinct tag-bearing Beads is large
	// enough to keep 31/total under frequencyThreshold. 40 is comfortably
	// more than the minimum (>= 14, since 31/(91+14) is just under 0.3);
	// chosen round for readability, not tightness.
	const paddingNoiseBeads = 40

	var tags []patientTag
	ts := "2026-01-01T00:00:00Z"

	hubID := "bead-hub"
	for _, ns := range capBindingNamespaces {
		tags = append(tags, patientTag{Tag: ns + "hub", BeadID: hubID, Timestamp: ts})
	}

	n := 0
	for _, ns := range capBindingNamespaces {
		for i := 0; i < partnersPerNamespace; i++ {
			partnerID := fmt.Sprintf("bead-partner-%03d", n)
			n++
			tags = append(tags, patientTag{Tag: ns + "hub", BeadID: partnerID, Timestamp: ts})
			// Unique noise tag per partner: not a trigger-namespace tag, so
			// it forms no candidate pairs of its own, but it does count
			// toward the total-distinct-tag-bearing-Beads denominator.
			tags = append(tags, patientTag{
				Tag:       fmt.Sprintf("loinc:noise-%03d", n),
				BeadID:    partnerID,
				Timestamp: ts,
			})
		}
	}

	for i := 0; i < paddingNoiseBeads; i++ {
		padID := fmt.Sprintf("bead-pad-%03d", i)
		tags = append(tags, patientTag{
			Tag:       fmt.Sprintf("loinc:pad-%03d", i),
			BeadID:    padID,
			Timestamp: ts,
		})
	}

	return tags
}

// countByNamespace tallies clinicalLink.MatchedTag by namespace prefix.
func countByNamespace(links []clinicalLink) map[string]int {
	counts := make(map[string]int)
	for _, l := range links {
		counts[tagNamespace(l.MatchedTag)]++
	}
	return counts
}

// linkSetKey is the comparison key TestProjectPatientLinks_CapBinding_
// DeterministicAcrossRuns uses to compare two runs' output as a set (per the
// task: "compare on (bead_a, bead_b, matched_tag, link_id) as a set").
type linkSetKey struct {
	beadA, beadB, matchedTag, linkID string
}

func linkSet(links []clinicalLink) map[linkSetKey]bool {
	set := make(map[linkSetKey]bool, len(links))
	for _, l := range links {
		set[linkSetKey{l.BeadA, l.BeadB, l.MatchedTag, l.LinkID}] = true
	}
	return set
}

// hubCandidatePairs counts, for a densePatientTagsForCapBinding-shaped
// fixture, how many of tags' rows are trigger-namespace tags on a Bead
// other than hubID — i.e. how many candidate pairs touch the hub Bead
// before the cap is applied.
func hubCandidatePairs(tags []patientTag, hubID string, namespaces []string) int {
	nsSet := make(map[string]bool, len(namespaces))
	for _, ns := range namespaces {
		nsSet[ns] = true
	}
	n := 0
	for _, pt := range tags {
		if pt.BeadID != hubID && nsSet[tagNamespace(pt.Tag)] {
			n++
		}
	}
	return n
}

// TestProjectPatientLinks_CapBinding_DeterministicAcrossRuns is the
// regression test for the bug where projectPatientLinks iterated
// beadsByTag (a Go map) in raw, randomized order while consuming the
// per-Bead maxLinksPerBead cap: which trigger tag's pairs got to claim a
// capped Bead's remaining slots depended on map iteration order, so two
// projections of byte-identical bead_tags input could (and empirically did,
// on the real corpus) select different link *sets*, even though the final
// sort.Slice made each individual run's own output look internally
// deterministic.
//
// The fixture (densePatientTagsForCapBinding) makes the hub Bead a
// candidate for 90 links (30 per trigger namespace, 3 namespaces) against
// maxLinksPerBead=50, so the cap provably binds and at least one namespace's
// candidate pairs are provably dropped on every run — the exact condition
// under which non-deterministic map order could produce a different
// surviving set from run to run. TestReproject_Deterministic_
// SameInputsSameOutput's existing fixture never reaches this condition (its
// hub Bead never comes close to 50 candidate links), which is why it passed
// throughout.
func TestProjectPatientLinks_CapBinding_DeterministicAcrossRuns(t *testing.T) {
	rule := LinkRule{
		RuleVersion: "test-rule-version",
		RuleID:      CooccurrenceRuleID,
		// The production rule's own declared priority order (rule.go's
		// triggerNamespaces) — this test exercises the real order, not an
		// arbitrary one, so it also stands as a determinism check on the
		// actual shipped priority.
		TriggerNamespaces: []string{"risk:", "atc:", "rxnorm:"},
		Relation:          relationClinicalCorrelation,
		Severity:          severityInfo,
		EvidenceBasis:     evidenceBasisCooccurrence,
	}
	tags := densePatientTagsForCapBinding()

	// Sanity: confirm the fixture actually makes the hub Bead a candidate
	// for more than maxLinksPerBead pairs, i.e. the cap is provably going to
	// bind. If this invariant ever stops holding (fixture edited without
	// care), the test below would degrade back into exactly the kind of
	// uncapped, cap-blind test the task warned against — fail loudly instead.
	const hubID = "bead-hub"
	if n := hubCandidatePairs(tags, hubID, capBindingNamespaces); n <= maxLinksPerBead {
		t.Fatalf("fixture invariant broken: hub Bead has only %d candidate pairs, want > maxLinksPerBead (%d)", n, maxLinksPerBead)
	}

	run1 := projectPatientLinks(rule, "patient-root-1", tags)
	run2 := projectPatientLinks(rule, "patient-root-1", tags)

	// The cap must actually have bound: hub is a candidate for 90 pairs but
	// may appear in at most maxLinksPerBead links.
	hubLinks := 0
	for _, l := range run1 {
		if l.BeadA == hubID || l.BeadB == hubID {
			hubLinks++
		}
	}
	if hubLinks != maxLinksPerBead {
		t.Fatalf("hub Bead appears in %d links, want exactly maxLinksPerBead (%d) — cap did not bind as the fixture intends", hubLinks, maxLinksPerBead)
	}

	set1, set2 := linkSet(run1), linkSet(run2)
	if len(set1) != len(run1) {
		t.Fatalf("run1 has duplicate (bead_a,bead_b,matched_tag,link_id) rows: %d links, %d unique keys", len(run1), len(set1))
	}

	if len(set1) != len(set2) {
		t.Fatalf("non-deterministic link count: run1=%d run2=%d", len(set1), len(set2))
	}
	for k := range set1 {
		if !set2[k] {
			t.Errorf("run1-only link not reproduced in run2: bead_a=%s bead_b=%s matched_tag=%s link_id=%s",
				k.beadA, k.beadB, k.matchedTag, k.linkID)
		}
	}
	for k := range set2 {
		if !set1[k] {
			t.Errorf("run2-only link not present in run1: bead_a=%s bead_b=%s matched_tag=%s link_id=%s",
				k.beadA, k.beadB, k.matchedTag, k.linkID)
		}
	}

	counts1, counts2 := countByNamespace(run1), countByNamespace(run2)
	for _, ns := range capBindingNamespaces {
		if counts1[ns] != counts2[ns] {
			t.Errorf("per-namespace count differs for %q: run1=%d run2=%d", ns, counts1[ns], counts2[ns])
		}
	}
	t.Logf("per-namespace link counts (rule.TriggerNamespaces=%v priority order): %+v", rule.TriggerNamespaces, counts1)
}

// TestProjectPatientLinks_ReorderingTriggerNamespaces_ChangesSurvivingSet
// pins cap-consumption PRIORITY semantics (not just determinism): given the
// exact same patientTags fixture, projecting it under two rules that differ
// ONLY in the declared order of TriggerNamespaces (same namespace set,
// same relation/severity/evidence_basis/everything else) must select a
// DIFFERENT surviving link set on the hub Bead — specifically, whichever
// namespace is listed earlier must claim its full 30-link allotment on the
// hub, while the namespace pushed to last place must be the one the cap
// squeezes.
//
// This is the regression test for the coordinator's follow-up finding: on
// the real corpus, defaulting cap-consumption priority to alphabetical
// order (atc: < risk: < rxnorm:) silently starved risk: — the rule's own
// clinically load-bearing namespace — by 69%, reproducing the exact v2
// failure mode (risk: links drowning in bulk noise) the rule exists to
// avoid. The fix is that priority must be read from rule.TriggerNamespaces
// itself (knowledge, carried in the rule Bead's own content and therefore
// its content-addressed rule_version) rather than computed by any policy
// hard-coded in this package. This test proves that property directly: two
// otherwise-identical LinkRule values, differing only in
// TriggerNamespaces' order, produce different surviving link sets on
// identical input — i.e. reordering the rule Bead alone (no code change)
// changes which links survive.
func TestProjectPatientLinks_ReorderingTriggerNamespaces_ChangesSurvivingSet(t *testing.T) {
	tags := densePatientTagsForCapBinding()
	const hubID = "bead-hub"
	if n := hubCandidatePairs(tags, hubID, capBindingNamespaces); n <= maxLinksPerBead {
		t.Fatalf("fixture invariant broken: hub Bead has only %d candidate pairs, want > maxLinksPerBead (%d)", n, maxLinksPerBead)
	}

	baseRule := LinkRule{
		RuleVersion:   "test-rule-version",
		RuleID:        CooccurrenceRuleID,
		Relation:      relationClinicalCorrelation,
		Severity:      severityInfo,
		EvidenceBasis: evidenceBasisCooccurrence,
	}

	// Two rules, same namespace SET, deliberately different declared
	// ORDER: riskFirst puts risk: ahead of atc: and rxnorm: (the production
	// rule.go ordering after this task's fix); rxnormFirst instead pushes
	// risk: to LAST place, so risk: is the one the cap should squeeze.
	riskFirst := baseRule
	riskFirst.TriggerNamespaces = []string{"risk:", "atc:", "rxnorm:"}
	rxnormFirst := baseRule
	rxnormFirst.TriggerNamespaces = []string{"atc:", "rxnorm:", "risk:"}

	linksRiskFirst := projectPatientLinks(riskFirst, "patient-root-1", tags)
	linksRxnormFirst := projectPatientLinks(rxnormFirst, "patient-root-1", tags)

	countsRiskFirst := countByNamespace(linksRiskFirst)
	countsRxnormFirst := countByNamespace(linksRxnormFirst)
	t.Logf("TriggerNamespaces=%v -> per-namespace hub-adjacent counts: %+v", riskFirst.TriggerNamespaces, countsRiskFirst)
	t.Logf("TriggerNamespaces=%v -> per-namespace hub-adjacent counts: %+v", rxnormFirst.TriggerNamespaces, countsRxnormFirst)

	// Under riskFirst, risk: is listed before rxnorm:, so risk: must claim
	// its full 30-pair allotment on the hub Bead — none of its pairs are
	// squeezed by the cap.
	riskFullUnderRiskFirst := countByNamespaceOnBead(linksRiskFirst, hubID, "risk:")
	if riskFullUnderRiskFirst != 30 {
		t.Errorf("with TriggerNamespaces=%v: hub's risk: links = %d, want 30 (risk: listed first, must not be squeezed)",
			riskFirst.TriggerNamespaces, riskFullUnderRiskFirst)
	}

	// Under rxnormFirst, risk: is listed LAST, so risk: must be the
	// namespace the cap squeezes on the hub Bead: strictly fewer than its
	// full 30-pair allotment.
	riskSqueezedUnderRxnormFirst := countByNamespaceOnBead(linksRxnormFirst, hubID, "risk:")
	if riskSqueezedUnderRxnormFirst >= 30 {
		t.Errorf("with TriggerNamespaces=%v: hub's risk: links = %d, want < 30 (risk: listed last, must be squeezed)",
			rxnormFirst.TriggerNamespaces, riskSqueezedUnderRxnormFirst)
	}

	// The core claim: the two rules' surviving sets differ, purely because
	// TriggerNamespaces' order differs — no other field of LinkRule, no
	// code, and no input tags changed between the two projectPatientLinks
	// calls above.
	if riskFullUnderRiskFirst == riskSqueezedUnderRxnormFirst {
		t.Fatalf("reordering TriggerNamespaces alone did not change the surviving link set: "+
			"risk: hub-links = %d under both orderings %v and %v",
			riskFullUnderRiskFirst, riskFirst.TriggerNamespaces, rxnormFirst.TriggerNamespaces)
	}

	// Determinism must still hold under each individual ordering — re-run
	// riskFirst a second time and confirm its set is reproduced exactly (a
	// narrower repeat of TestProjectPatientLinks_CapBinding_
	// DeterministicAcrossRuns, scoped to this test's own two rule variants,
	// so a reviewer does not have to trust that determinism generalizes
	// across rules without seeing it checked here too).
	linksRiskFirstRerun := projectPatientLinks(riskFirst, "patient-root-1", tags)
	setA, setB := linkSet(linksRiskFirst), linkSet(linksRiskFirstRerun)
	if len(setA) != len(setB) {
		t.Fatalf("riskFirst rule non-deterministic across runs: run1=%d links, run2=%d links", len(setA), len(setB))
	}
	for k := range setA {
		if !setB[k] {
			t.Errorf("riskFirst rule non-deterministic: run1-only link bead_a=%s bead_b=%s matched_tag=%s", k.beadA, k.beadB, k.matchedTag)
		}
	}
}

// countByNamespaceOnBead counts links in which beadID appears (as either
// bead_a or bead_b) whose matched_tag's namespace is ns.
func countByNamespaceOnBead(links []clinicalLink, beadID, ns string) int {
	n := 0
	for _, l := range links {
		if (l.BeadA == beadID || l.BeadB == beadID) && tagNamespace(l.MatchedTag) == ns {
			n++
		}
	}
	return n
}
