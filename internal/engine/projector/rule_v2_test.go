package projector

import (
	"testing"

	"github.com/medbeads/medbeads/internal/engine/bead"
)

func TestLinkRuleV2_MinSharedIsApplied(t *testing.T) {
	b := BuildCooccurrenceRuleBead("2026-07-14T00:00:00Z")
	b.Content["trigger"].(map[string]any)["min_shared"] = 2
	id, err := bead.ComputeID(b)
	if err != nil {
		t.Fatalf("ComputeID: %v", err)
	}
	rule, err := decodeLinkRule(id, b.Content)
	if err != nil {
		t.Fatalf("decodeLinkRule: %v", err)
	}

	tags := []patientTag{
		{Tag: "risk:test", BeadID: "a", Timestamp: "2026-01-01"},
		{Tag: "risk:test", BeadID: "b", Timestamp: "2026-01-02"},
		{Tag: "atc:test", BeadID: "a", Timestamp: "2026-01-01"},
		{Tag: "atc:test", BeadID: "b", Timestamp: "2026-01-02"},
	}
	for i := 0; i < 10; i++ {
		tags = append(tags, patientTag{Tag: "loinc:noise", BeadID: string(rune('c' + i)), Timestamp: "2026-01-03"})
	}
	if got := len(projectPatientLinks(rule, "patient", tags)); got != 2 {
		t.Fatalf("two shared tags produced %d links, want 2", got)
	}

	oneShared := make([]patientTag, 0, len(tags))
	for _, tag := range tags {
		if tag.Tag != "atc:test" {
			oneShared = append(oneShared, tag)
		}
	}
	if got := len(projectPatientLinks(rule, "patient", oneShared)); got != 0 {
		t.Fatalf("one shared tag produced %d links with min_shared=2, want 0", got)
	}
}

func TestLinkRuleV2_RejectsUnsafeExecutionLimits(t *testing.T) {
	b := BuildCooccurrenceRuleBead("2026-07-14T00:00:00Z")
	b.Content["execution"].(map[string]any)["frequency_threshold"] = 0
	id, err := bead.ComputeID(b)
	if err != nil {
		t.Fatalf("ComputeID: %v", err)
	}
	if _, err := decodeLinkRule(id, b.Content); err == nil {
		t.Fatal("decodeLinkRule accepted frequency_threshold=0")
	}
}

func TestLinkRuleV1_RemainsReadable(t *testing.T) {
	b := BuildCooccurrenceRuleBead("2026-07-14T00:00:00Z")
	b.Content["schema"] = linkRuleSchemaV1
	delete(b.Content, "execution")
	id, err := bead.ComputeID(b)
	if err != nil {
		t.Fatalf("ComputeID: %v", err)
	}
	rule, err := decodeLinkRule(id, b.Content)
	if err != nil {
		t.Fatalf("decodeLinkRule(v1): %v", err)
	}
	if rule.FrequencyThreshold != frequencyThreshold || rule.MaxLinksPerBead != maxLinksPerBead {
		t.Fatalf("v1 defaults=(%v,%d), want (%v,%d)", rule.FrequencyThreshold, rule.MaxLinksPerBead, frequencyThreshold, maxLinksPerBead)
	}
}

func TestCuratedRuleV2_PreservesExternalEvidenceAndEffectivePeriod(t *testing.T) {
	b := BuildCuratedPairRuleBead(
		"ddi-evidence-test", "drug_drug_interaction", "alert",
		[][2]string{{"atc:a", "atc:b"}}, "2026-07-14T00:00:00Z",
	)
	b.Content["revision_label"] = "2026.1"
	b.Content["evidence_basis"] = "guideline"
	b.Content["evidence_bead_ids"] = []string{"source-b", "source-a", "source-a"}
	b.Content["effective_period"] = map[string]any{
		"from": "2026-07-01T00:00:00Z",
		"to":   "2027-06-30T23:59:59Z",
	}
	id, err := bead.ComputeID(b)
	if err != nil {
		t.Fatalf("ComputeID: %v", err)
	}
	rule, err := decodeCuratedPairRule(id, b.Content)
	if err != nil {
		t.Fatalf("decodeCuratedPairRule: %v", err)
	}
	if rule.RevisionLabel != "2026.1" || rule.EffectiveFrom != "2026-07-01T00:00:00Z" {
		t.Fatalf("decoded governance metadata = %+v", rule)
	}
	if len(rule.EvidenceBeadIDs) != 2 || rule.EvidenceBeadIDs[0] != "source-a" || rule.EvidenceBeadIDs[1] != "source-b" {
		t.Fatalf("evidence IDs = %v, want deduplicated [source-a source-b]", rule.EvidenceBeadIDs)
	}
	links := projectCuratedPairLinks(rule, "patient", []patientTag{
		{Tag: "atc:a", BeadID: "a", Timestamp: "2026-07-14"},
		{Tag: "atc:b", BeadID: "b", Timestamp: "2026-07-14"},
	})
	if len(links) != 1 || len(links[0].EvidenceBeadIDs) != 3 {
		t.Fatalf("projected evidence = %+v, want rule plus two source Beads", links)
	}
	if err := validateRuleEffectivity([]CuratedPairRule{rule}, "2026-07-14T00:00:00Z"); err != nil {
		t.Fatalf("effective rule rejected: %v", err)
	}
	if err := validateRuleEffectivity([]CuratedPairRule{rule}, "2025-07-14T00:00:00Z"); err == nil {
		t.Fatal("future rule was accepted before effective_from")
	}
}
