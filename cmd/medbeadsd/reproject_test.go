package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/medbeads/medbeads/internal/engine"
	"github.com/medbeads/medbeads/internal/engine/bead"
	"github.com/medbeads/medbeads/internal/engine/projector"
)

func TestPublishCuratedRules_RejectsUnverifiedInlineSignature(t *testing.T) {
	dataDir := t.TempDir()
	eng, err := engine.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close() //nolint:errcheck
	path := filepath.Join(dataDir, "rule.json")
	raw := `{"rules":[{"rule_id":"rule-1","relation":"clinical_correlation","severity":"warning","tag_pairs":[["atc:a","atc:b"]],"timestamp":"2026-01-01T00:00:00Z","author":"ehr:user:1","signature":"not-verified"}]}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = publishCuratedRules(eng, path)
	if err == nil || !strings.Contains(err.Error(), "inline signature is not a trusted signature") {
		t.Fatalf("publishCuratedRules error = %v", err)
	}
}

// staleCooccurrenceRuleBead builds a link_rule Bead sharing
// projector.CooccurrenceRuleID (the same stable rule_id
// BuildCooccurrenceRuleBead itself uses) but different content
// (trigger.tag_namespaces in the OLD alphabetical order this task's earlier
// revision used, before TriggerNamespaces was reordered to
// risk:-first) — a different content-addressed Bead ID, standing in for
// "whatever rule Bead a prior version of this codebase already seeded into
// a real store". This mirrors projector's own reproject_test.go
// (altTriggerCooccurrenceRuleBead) and reindex_reproject_test.go's
// established "two variants of one rule_id" fixture shape.
func staleCooccurrenceRuleBead(timestamp string) bead.Bead {
	content := map[string]any{
		"schema":      "medbeads.link_rule.v1",
		"rule_id":     projector.CooccurrenceRuleID,
		"rule_family": "cooccurrence",
		"trigger": map[string]any{
			"tag_namespaces": []string{"atc:", "risk:", "rxnorm:"},
			"min_shared":     1,
			"excludes": map[string]any{
				"same_code_namespaces": []string{"loinc:"},
			},
		},
		"relation":       "clinical_correlation",
		"severity":       "info",
		"evidence_basis": "cooccurrence",
		"score_model": map[string]any{
			"weights": map[string]any{"shared_tag": 1},
		},
	}
	return bead.Bead{
		Type:      "link_rule",
		Timestamp: timestamp,
		Author:    "projector_seed",
		Content:   content,
	}
}

// TestRun_Reproject_SelectsCurrentBuildRuleOverStaleAlreadySeededRule is the
// regression test for the bug data-reviewer caught: `medbeadsd reproject`'s
// ensureCooccurrenceRule used to call projector.LoadActiveCooccurrenceRule
// with no knowledgeBeadIDs filter FIRST, which matches any already-indexed
// link_rule Bead sharing rule_id="cooccurrence-risk-atc-v1" (rule_id is a
// stable key ACROSS revisions by design) and returns immediately — so an
// older rule Bead already present in the store (e.g. seeded by a prior
// build of this same binary) was selected, and the CURRENT build's own
// projector.BuildCooccurrenceRuleBead content was never even computed or
// ingested, let alone selected. All of projector's own unit/integration
// tests passed throughout, because none of them exercise "a store that
// already has a same-rule_id rule Bead from a different content revision"
// — this is a cmd/medbeadsd-level wiring bug, not a projector-package bug,
// which is exactly why it slipped through 100% green projector tests while
// being a 100% no-op on the real corpus.
//
// Fixture: seed staleCooccurrenceRuleBead (a different link_rule Bead
// sharing the same rule_id, standing in for "already seeded by a prior
// build") directly into the store BEFORE ever calling `reproject` — the
// exact precondition that triggered the bug. Then run `medbeadsd reproject`
// via run() (the real CLI entry point, not projector.Reproject called
// directly, since the bug lived in cmd/medbeadsd's own wiring) and assert:
//
//  1. The CURRENT build's rule Bead (projector.BuildCooccurrenceRuleBead)
//     is now present in the store's shared Beads (it was ingested).
//  2. Every clinical_links row's rule_version is the CURRENT build's rule
//     Bead ID, not the stale one.
//  3. projection_manifest's active row's knowledge_bead_ids names the
//     CURRENT build's rule Bead ID.
//  4. The STALE rule Bead is still present, byte-unchanged, in the store's
//     shared Beads afterward — knowledge Beads are immutable; a superseded
//     rule Bead must never be deleted or overwritten, only no longer the
//     one a run names (this is the "旧ルール Bead が fact 層に残ること自体は
//     設計どおり正しい" invariant the coordinator's task explicitly called
//     out as something this test must also protect).
func TestRun_Reproject_SelectsCurrentBuildRuleOverStaleAlreadySeededRule(t *testing.T) {
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer devNull.Close()

	dir := t.TempDir()

	// Seed the stale rule Bead AND a patient with a genuine risk:/atc:
	// cooccurrence pair directly via engine.Ingest, standing in for "a
	// store a prior build of this binary already ran `reproject` against".
	eng, err := engine.Open(dir)
	if err != nil {
		t.Fatalf("engine.Open: %v", err)
	}
	stale, err := eng.Ingest(staleCooccurrenceRuleBead("2025-01-01T00:00:00Z"))
	if err != nil {
		t.Fatalf("ingest stale rule bead: %v", err)
	}

	root, err := eng.Ingest(bead.Bead{
		Type: "patient_registration", Timestamp: "2026-01-01T00:00:00Z",
		Author: "did:medbeads:doctor:12345", Content: map[string]any{"name": "Stale Rule Regression Patient"},
	})
	if err != nil {
		t.Fatalf("ingest patient: %v", err)
	}

	// Noise Beads: keeps risk:nephrotoxic's patient-local frequency
	// comfortably under projector's frequencyThreshold (0.3) — without
	// this padding, a bare two-Bead cooccurrence pair IS the entire
	// patient's tag-bearing Bead set (100% frequency), so the IDF-style
	// filter would exclude it and no clinical_links row would ever be
	// written regardless of which rule Bead is selected, making this test
	// unable to observe rule_version at all. Mirrors internal/engine/
	// projector's own reproject_test.go padWithNoiseBeads helper.
	for i := 0; i < 10; i++ {
		noise, err := eng.Ingest(bead.Bead{
			Type: "fhir_observation", Timestamp: "2026-01-15T00:00:00Z",
			Author: "did:medbeads:doctor:12345", Parents: []string{root.ID},
			Content: map[string]any{"noise": i},
		})
		if err != nil {
			t.Fatalf("ingest noise bead %d: %v", i, err)
		}
		if _, err := eng.Index().SQLDB().Exec(
			`INSERT OR IGNORE INTO bead_tags (tag, bead_id, patient_root) VALUES (?, ?, ?)`,
			"loinc:noise", noise.ID, root.ID,
		); err != nil {
			t.Fatalf("seed noise tag %d: %v", i, err)
		}
	}

	rx, err := eng.Ingest(bead.Bead{
		Type: "fhir_medicationrequest", Timestamp: "2026-02-01T09:00:00Z",
		Author: "did:medbeads:doctor:12345", Parents: []string{root.ID},
		Content: map[string]any{"drug": "meropenem"},
	})
	if err != nil {
		t.Fatalf("ingest rx: %v", err)
	}
	lab, err := eng.Ingest(bead.Bead{
		Type: "fhir_observation", Timestamp: "2026-02-01T10:00:00Z",
		Author: "did:medbeads:doctor:12345", Parents: []string{root.ID},
		Content: map[string]any{"test": "eGFR"},
	})
	if err != nil {
		t.Fatalf("ingest lab: %v", err)
	}
	if _, err := eng.Index().SQLDB().Exec(
		`INSERT OR IGNORE INTO bead_tags (tag, bead_id, patient_root) VALUES (?, ?, ?)`,
		"risk:nephrotoxic", rx.ID, root.ID,
	); err != nil {
		t.Fatalf("seed tag rx risk: %v", err)
	}
	if _, err := eng.Index().SQLDB().Exec(
		`INSERT OR IGNORE INTO bead_tags (tag, bead_id, patient_root) VALUES (?, ?, ?)`,
		"atc:c09aa03", rx.ID, root.ID,
	); err != nil {
		t.Fatalf("seed tag rx atc: %v", err)
	}
	if _, err := eng.Index().SQLDB().Exec(
		`INSERT OR IGNORE INTO bead_tags (tag, bead_id, patient_root) VALUES (?, ?, ?)`,
		"risk:nephrotoxic", lab.ID, root.ID,
	); err != nil {
		t.Fatalf("seed tag lab risk: %v", err)
	}
	if err := eng.Close(); err != nil {
		t.Fatalf("close engine before reproject: %v", err)
	}

	// The CURRENT build's own rule Bead ID (what a fixed ensureCooccurrenceRule
	// must select) — computed independently of the CLI path, purely from
	// BuildCooccurrenceRuleBead, so this assertion cannot be trivially
	// satisfied by the bug (which would instead leave rule_version == stale.ID).
	currentRuleBead := projector.BuildCooccurrenceRuleBead("2026-01-01T00:00:00Z")
	currentRuleID, err := bead.ComputeID(currentRuleBead)
	if err != nil {
		t.Fatalf("compute current rule bead ID: %v", err)
	}
	if currentRuleID == stale.ID {
		t.Fatalf("test setup invariant broken: staleCooccurrenceRuleBead minted the same ID as the current build's rule bead (%s) — fixture's content is not actually different", currentRuleID)
	}

	if got := run([]string{"reproject", "-data", dir}, devNull, devNull); got != 0 {
		t.Fatalf("run(reproject -data %s) = %d, want 0", dir, got)
	}

	eng2, err := engine.Open(dir)
	if err != nil {
		t.Fatalf("engine.Open after reproject: %v", err)
	}
	defer eng2.Close()

	// (1) The current build's rule Bead was actually ingested.
	if _, err := eng2.GetBead(currentRuleID); err != nil {
		t.Errorf("current build's rule bead %s not found after reproject: %v", currentRuleID, err)
	}

	// (4) The stale rule Bead is still present, unchanged — immutability.
	staleAfter, err := eng2.GetBead(stale.ID)
	if err != nil {
		t.Fatalf("stale rule bead %s missing after reproject (must remain — knowledge beads are immutable): %v", stale.ID, err)
	}
	staleAfterJSON, _ := json.Marshal(staleAfter.Content)
	staleBeforeJSON, _ := json.Marshal(stale.Content)
	if string(staleAfterJSON) != string(staleBeforeJSON) {
		t.Errorf("stale rule bead %s content changed after reproject:\n before=%s\n after=%s", stale.ID, staleBeforeJSON, staleAfterJSON)
	}

	// (2) Every clinical_links row for this patient carries the CURRENT
	// build's rule_version, not the stale one.
	rows, err := eng2.Index().SQLDB().Query(
		`SELECT DISTINCT rule_version FROM clinical_links WHERE patient_root = ?`, root.ID)
	if err != nil {
		t.Fatalf("query clinical_links rule_version: %v", err)
	}
	var ruleVersions []string
	for rows.Next() {
		var rv string
		if err := rows.Scan(&rv); err != nil {
			t.Fatalf("scan rule_version: %v", err)
		}
		ruleVersions = append(ruleVersions, rv)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	if len(ruleVersions) == 0 {
		t.Fatalf("no clinical_links rows written for patient %s — expected the risk:/atc: cooccurrence pair to produce at least one", root.ID)
	}
	for _, rv := range ruleVersions {
		if rv != currentRuleID {
			t.Errorf("clinical_links.rule_version = %q, want %q (current build's rule bead) — stale rule bead %q must not be selected", rv, currentRuleID, stale.ID)
		}
	}

	// (3) projection_manifest's active row names the current build's rule
	// bead ID in knowledge_bead_ids.
	var manifestKnowledgeBeadIDsJSON string
	if err := eng2.Index().SQLDB().QueryRow(
		`SELECT knowledge_bead_ids FROM projection_manifest WHERE projection_name = ? AND status = 'active'`,
		projector.ProjectionName,
	).Scan(&manifestKnowledgeBeadIDsJSON); err != nil {
		t.Fatalf("query active projection_manifest row: %v", err)
	}
	var knowledgeBeadIDs []string
	if err := json.Unmarshal([]byte(manifestKnowledgeBeadIDsJSON), &knowledgeBeadIDs); err != nil {
		t.Fatalf("decode knowledge_bead_ids %q: %v", manifestKnowledgeBeadIDsJSON, err)
	}
	found := false
	for _, id := range knowledgeBeadIDs {
		if id == currentRuleID {
			found = true
		}
		if id == stale.ID {
			t.Errorf("projection_manifest.knowledge_bead_ids = %v names the STALE rule bead %q, want only the current build's %q", knowledgeBeadIDs, stale.ID, currentRuleID)
		}
	}
	if !found {
		t.Errorf("projection_manifest.knowledge_bead_ids = %v, want it to contain the current build's rule bead %q", knowledgeBeadIDs, currentRuleID)
	}
}
