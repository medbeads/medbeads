package projector

import (
	"testing"

	"github.com/medbeads/medbeads/internal/engine/bead"
)

// Warfarin (ATC B01AA03) + an NSAID (ibuprofen, ATC M01AE01) is the canonical
// bleeding-risk interaction. It is also the exact clinical claim the v2
// post-mortem said the system existed to make and could not
// (docs/reviews/2026-07-10_scheme_critique_A_internal.md: the drug-interaction
// links the design was ostensibly for accounted for 0.1% of its output).
const (
	tagWarfarin  = "atc:b01aa03"
	tagIbuprofen = "atc:m01ae01"
)

func curatedDDIRule(t *testing.T) CuratedPairRule {
	t.Helper()
	b := BuildCuratedPairRuleBead(
		"ddi-warfarin-nsaid-v1",
		"drug_drug_interaction",
		"warning",
		[][2]string{{tagWarfarin, tagIbuprofen}},
		"2026-01-01T00:00:00Z",
	)
	id, err := bead.ComputeID(b)
	if err != nil {
		t.Fatalf("ComputeID: %v", err)
	}
	rule, err := decodeCuratedPairRule(id, b.Content)
	if err != nil {
		t.Fatalf("decodeCuratedPairRule: %v", err)
	}
	return rule
}

// TestCuratedPair_WarningNamesItsEvidence pins the property the entire severity
// design rests on: a curated rule may assert a severity above `info`, but only by
// naming the knowledge Bead that justifies it. clinical_links' CHECK constraint
// (migrations/0006) refuses any other shape, so a projector emitting a warning
// with an empty evidence_bead_ids would fail at the INSERT. This asserts the
// projector produces the shape the database will actually accept.
func TestCuratedPair_WarningNamesItsEvidence(t *testing.T) {
	rule := curatedDDIRule(t)

	tags := []patientTag{
		{Tag: tagWarfarin, BeadID: "beadA", Timestamp: "2026-01-02T00:00:00Z"},
		{Tag: tagIbuprofen, BeadID: "beadB", Timestamp: "2026-01-03T00:00:00Z"},
	}

	links := projectCuratedPairLinks(rule, "patient-1", tags)
	if len(links) != 1 {
		t.Fatalf("links = %d, want 1", len(links))
	}
	l := links[0]

	if l.Severity != "warning" {
		t.Errorf("severity = %q, want warning", l.Severity)
	}
	if l.EvidenceBasis != evidenceBasisCuratedKnowledge {
		t.Errorf("evidence_basis = %q, want %q", l.EvidenceBasis, evidenceBasisCuratedKnowledge)
	}
	if l.RuleVersion != rule.RuleVersion {
		t.Errorf("rule_version = %q, want the rule Bead's own ID %q", l.RuleVersion, rule.RuleVersion)
	}
	// The load-bearing assertion: the warning names its own justification.
	if len(l.EvidenceBeadIDs) != 1 || l.EvidenceBeadIDs[0] != rule.RuleVersion {
		t.Errorf("evidence_bead_ids = %v, want [%s]: a warning that cannot name the knowledge Bead justifying it must not exist",
			l.EvidenceBeadIDs, rule.RuleVersion)
	}
	if l.Relation != "drug_drug_interaction" {
		t.Errorf("relation = %q, want drug_drug_interaction", l.Relation)
	}
	if l.BeadA >= l.BeadB {
		t.Errorf("bead_a=%q must sort before bead_b=%q (the table's CHECK)", l.BeadA, l.BeadB)
	}
}

// TestCuratedPair_FiresOnlyWhenBothSidesPresent: a patient on warfarin alone is
// not having a drug interaction. Inventing a warning for them is exactly the
// alert-fatigue failure the severity floor exists to prevent.
func TestCuratedPair_FiresOnlyWhenBothSidesPresent(t *testing.T) {
	rule := curatedDDIRule(t)

	onlyOneSide := []patientTag{
		{Tag: tagWarfarin, BeadID: "beadA", Timestamp: "2026-01-02T00:00:00Z"},
		{Tag: "atc:c09aa03", BeadID: "beadC", Timestamp: "2026-01-03T00:00:00Z"},
	}
	if links := projectCuratedPairLinks(rule, "patient-1", onlyOneSide); len(links) != 0 {
		t.Fatalf("patient carrying only one side of the pair produced %d link(s), want 0: %+v", len(links), links)
	}
}

// TestCuratedPair_NoSelfInteraction: one Bead carrying both tags is not
// interacting with itself.
func TestCuratedPair_NoSelfInteraction(t *testing.T) {
	rule := curatedDDIRule(t)

	tags := []patientTag{
		{Tag: tagWarfarin, BeadID: "beadA", Timestamp: "2026-01-02T00:00:00Z"},
		{Tag: tagIbuprofen, BeadID: "beadA", Timestamp: "2026-01-02T00:00:00Z"},
	}
	if links := projectCuratedPairLinks(rule, "patient-1", tags); len(links) != 0 {
		t.Fatalf("a single Bead carrying both tags produced %d link(s), want 0", len(links))
	}
}

// TestCuratedPair_Deterministic: identical facts + identical rule must produce
// the byte-identical link SET. The cooccurrence projector was once
// nondeterministic in exactly this way (Go map iteration order reaching the
// selection, hidden behind a final sort), so this is asserted, never assumed.
func TestCuratedPair_Deterministic(t *testing.T) {
	rule := curatedDDIRule(t)

	var tags []patientTag
	for _, id := range []string{"b3", "b1", "b2"} {
		tags = append(tags, patientTag{Tag: tagWarfarin, BeadID: id, Timestamp: "2026-01-02T00:00:00Z"})
	}
	for _, id := range []string{"b9", "b7", "b8"} {
		tags = append(tags, patientTag{Tag: tagIbuprofen, BeadID: id, Timestamp: "2026-01-03T00:00:00Z"})
	}

	first := projectCuratedPairLinks(rule, "patient-1", tags)
	if len(first) != 9 {
		t.Fatalf("links = %d, want 9 (3 warfarin x 3 NSAID)", len(first))
	}

	for i := 0; i < 20; i++ {
		again := projectCuratedPairLinks(rule, "patient-1", tags)
		if len(again) != len(first) {
			t.Fatalf("run %d: %d links, want %d", i, len(again), len(first))
		}
		for j := range first {
			if again[j].LinkID != first[j].LinkID {
				t.Fatalf("run %d: link[%d] id = %s, want %s: the selected SET is not stable",
					i, j, again[j].LinkID, first[j].LinkID)
			}
		}
	}
}

// TestCuratedPair_RuleBeadIDIsContentDerived: publishing the same knowledge twice
// mints the same Bead (seeding is idempotent); revising the knowledge mints a
// different one. This is what makes a warning auditable — its rule_version pins
// the exact rule that asserted it, and a revised rule cannot silently redefine
// what an already-written warning meant.
func TestCuratedPair_RuleBeadIDIsContentDerived(t *testing.T) {
	mk := func(severity string, pairs [][2]string) string {
		t.Helper()
		b := BuildCuratedPairRuleBead("ddi-warfarin-nsaid-v1", "drug_drug_interaction", severity, pairs, "2026-01-01T00:00:00Z")
		id, err := bead.ComputeID(b)
		if err != nil {
			t.Fatalf("ComputeID: %v", err)
		}
		return id
	}

	pair := [][2]string{{tagWarfarin, tagIbuprofen}}

	if a, b := mk("warning", pair), mk("warning", pair); a != b {
		t.Fatalf("the same rule content minted two different IDs (%s vs %s): seeding would not be idempotent", a, b)
	}

	// {A,B} and {B,A} are the same knowledge: the pair is a set, not a direction.
	reversed := [][2]string{{tagIbuprofen, tagWarfarin}}
	if mk("warning", pair) != mk("warning", reversed) {
		t.Error("declaring {A,B} and {B,A} minted different rule Beads; the pair must be order-insensitive")
	}

	// Escalating severity is a change to the knowledge and MUST change the ID.
	if mk("warning", pair) == mk("critical", pair) {
		t.Error("escalating severity did not change the rule Bead ID; a revised rule would silently redefine existing warnings")
	}
}
