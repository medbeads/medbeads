package mcpserver

import (
	"context"
	"testing"

	"github.com/medbeads/medbeads/internal/engine/apc"
	"github.com/medbeads/medbeads/internal/engine/bead"
	"github.com/medbeads/medbeads/internal/engine/clearance"
	"github.com/medbeads/medbeads/internal/engine/projector"
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

// TestRetrieve_IncludeSiblingsFalse_DropsSiblingTierEndToEnd is the
// mcpserver-level companion to
// graph.TestBuildContext_WithSiblingsFalse_SkipsBothSiblingTiers: with a
// real APC-generated sibling_link (via apc.Scanner.Scan, mirroring
// TestIntegration_RetrieveOneRoundTrip's setup) on a shared risk:/organ:
// antigen pair, retrieve(include_siblings=false) must not surface the
// sibling observation at all (neither Items nor TruncatedRefs), while the
// default (include_siblings omitted, i.e. true) call does — the
// docs/requirements.md R8.2 dag_nosib vs. dag_full distinction bench/ needs.
func TestRetrieve_IncludeSiblingsFalse_DropsSiblingTierEndToEnd(t *testing.T) {
	e := openT(t)

	patient := seedPatient(t, e, "Sibling Toggle Patient")
	encounter := seedChildBead(t, e, patient, "fhir_encounter", nil, map[string]any{
		"reason": "acute kidney injury follow-up",
	})
	for i := 0; i < 10; i++ {
		seedChildBead(t, e, encounter, "fhir_observation",
			[]string{"loinc:noise-" + string(rune('a'+i))},
			map[string]any{"noise": i})
	}
	medication := seedChildBead(t, e, encounter, "fhir_medicationrequest",
		[]string{"risk:nephrotoxic", "organ:renal"},
		map[string]any{"drug": "meropenem 1g IV every 8 hours"})
	observation := seedChildBead(t, e, encounter, "fhir_observation",
		[]string{"risk:nephrotoxic", "organ:renal"},
		map[string]any{"test": "eGFR renal function panel"})

	scanner := apc.New(e, e.Index(), apc.Default())
	for i := 0; i < 10; i++ {
		res, err := scanner.Scan()
		if err != nil {
			t.Fatalf("Scan: %v", err)
		}
		if res.BeadsScanned == 0 {
			break
		}
	}
	if !siblingLinkExists(t, e) {
		t.Fatalf("APC scan produced no sibling_link Bead for the shared risk:/organ: antigens; test setup assumption violated")
	}

	s := newServerT(t, e, SystemRole)
	observationView := bead.FormatID(observation.ID)

	// Default (include_siblings omitted -> true): the sibling observation is
	// present, reached via the sibling_link's bead_edges row.
	_, defaultOut, err := s.retrieve(context.Background(), nil, retrieveIn{
		Query:       "meropenem",
		PatientID:   patient.ID,
		TokenBudget: 4000,
		ChainDepth:  5,
	})
	if err != nil {
		t.Fatalf("retrieve (default): %v", err)
	}
	if !containsItemID(defaultOut.Items, observationView) {
		t.Fatalf("retrieve default (include_siblings omitted): sibling observation %s missing from Items=%+v", observationView, defaultOut.Items)
	}

	// include_siblings=false: the same sibling observation must be dropped
	// entirely (not demoted to TruncatedRefs either — the sibling tiers are
	// skipped before any candidate is claimed, per graph.WithSiblings).
	includeFalse := false
	_, noSibOut, err := s.retrieve(context.Background(), nil, retrieveIn{
		Query:           "meropenem",
		PatientID:       patient.ID,
		TokenBudget:     4000,
		ChainDepth:      5,
		IncludeSiblings: &includeFalse,
	})
	if err != nil {
		t.Fatalf("retrieve (include_siblings=false): %v", err)
	}
	if containsItemID(noSibOut.Items, observationView) {
		t.Errorf("retrieve(include_siblings=false): sibling observation %s present in Items, want excluded", observationView)
	}
	if containsRefID(noSibOut.TruncatedRefs, observationView) {
		t.Errorf("retrieve(include_siblings=false): sibling observation %s present in TruncatedRefs, want excluded entirely", observationView)
	}

	// The anchor (medicationrequest) and its ancestor (encounter) must be
	// unaffected by include_siblings=false — only the sibling tiers are
	// skipped.
	medicationView := bead.FormatID(medication.ID)
	encounterView := bead.FormatID(encounter.ID)
	if !containsItemID(noSibOut.Items, medicationView) {
		t.Errorf("retrieve(include_siblings=false): anchor medicationrequest %s missing from Items", medicationView)
	}
	if !containsItemID(noSibOut.Items, encounterView) {
		t.Errorf("retrieve(include_siblings=false): ancestor encounter %s missing from Items", encounterView)
	}

	// include_siblings=true explicitly must behave identically to omitting
	// the field (both resolve to true per retrieveIncludeSiblings' default).
	includeTrue := true
	_, explicitTrueOut, err := s.retrieve(context.Background(), nil, retrieveIn{
		Query:           "meropenem",
		PatientID:       patient.ID,
		TokenBudget:     4000,
		ChainDepth:      5,
		IncludeSiblings: &includeTrue,
	})
	if err != nil {
		t.Fatalf("retrieve (include_siblings=true): %v", err)
	}
	if !containsItemID(explicitTrueOut.Items, observationView) {
		t.Errorf("retrieve(include_siblings=true): sibling observation %s missing from Items, want present (same as default)", observationView)
	}
}

func containsItemID(items []provenanceView, id string) bool {
	for _, it := range items {
		if it.ID == id {
			return true
		}
	}
	return false
}

func containsRefID(refs []contextItemView, id string) bool {
	for _, it := range refs {
		if it.ID == id {
			return true
		}
	}
	return false
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

// --- U3c: retrieve surfaces clinical_links --------------------------------

// TestRetrieve_SurfacesClinicalLinks checks U3c's retrieve-wiring judgment
// call: a risk:/atc: cooccurrence pair projected by projector.Reproject
// (U3b) is surfaced in retrieveOut.ClinicalLinks once both endpoints are in
// Items, gated by the same IncludeSiblings flag Items' own sibling tiers
// use (retrieveIn's doc comment: this becomes include_links in U5).
func TestRetrieve_SurfacesClinicalLinks(t *testing.T) {
	e := openT(t)
	patient := seedPatient(t, e, "Clinical Links Patient")
	encounter := seedChildBead(t, e, patient, "fhir_encounter", nil, map[string]any{
		"reason": "acute kidney injury follow-up",
	})
	for i := 0; i < 10; i++ {
		seedChildBead(t, e, encounter, "fhir_observation",
			[]string{"loinc:noise-" + string(rune('a'+i))},
			map[string]any{"noise": i})
	}
	medication := seedChildBead(t, e, encounter, "fhir_medicationrequest",
		[]string{"risk:nephrotoxic", "atc:c09aa03"},
		map[string]any{"drug": "meropenem 1g IV every 8 hours"})
	observation := seedChildBead(t, e, encounter, "fhir_observation",
		[]string{"risk:nephrotoxic"},
		map[string]any{"test": "eGFR renal function panel"})

	rule := ingestT(t, e, projector.BuildCooccurrenceRuleBead("2026-01-01T00:00:00Z"))
	if _, err := projector.Reproject(e.Index(), engineReaderT{e}, []string{rule.ID}, "test-code-v1", "2026-07-11T00:00:00Z"); err != nil {
		t.Fatalf("Reproject: %v", err)
	}

	s := newServerT(t, e, SystemRole)
	_, out, err := s.retrieve(context.Background(), nil, retrieveIn{
		Query:       "meropenem",
		PatientID:   patient.ID,
		TokenBudget: 4000,
		ChainDepth:  5,
	})
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}

	medicationView := bead.FormatID(medication.ID)
	observationView := bead.FormatID(observation.ID)
	if !containsItemID(out.Items, medicationView) {
		t.Fatalf("retrieve Items missing anchor medicationrequest %s: %+v", medicationView, out.Items)
	}

	found := false
	for _, link := range out.ClinicalLinks {
		if link.BeadID == medicationView && link.OtherBeadID == observationView {
			found = true
			if link.Relation != "clinical_correlation" {
				t.Errorf("ClinicalLinks relation = %q, want clinical_correlation", link.Relation)
			}
			if link.Severity != "info" {
				t.Errorf("ClinicalLinks severity = %q, want info", link.Severity)
			}
			if link.MatchedTag != "risk:nephrotoxic" {
				t.Errorf("ClinicalLinks matched_tag = %q, want risk:nephrotoxic", link.MatchedTag)
			}
		}
	}
	if !found {
		t.Fatalf("retrieve ClinicalLinks missing medication<->observation link: %+v", out.ClinicalLinks)
	}

	// include_siblings=false must also suppress ClinicalLinks (same flag as
	// the sibling tiers — see retrieveOut.ClinicalLinks' doc comment).
	includeFalse := false
	_, noLinksOut, err := s.retrieve(context.Background(), nil, retrieveIn{
		Query:           "meropenem",
		PatientID:       patient.ID,
		TokenBudget:     4000,
		ChainDepth:      5,
		IncludeSiblings: &includeFalse,
	})
	if err != nil {
		t.Fatalf("retrieve (include_siblings=false): %v", err)
	}
	if len(noLinksOut.ClinicalLinks) != 0 {
		t.Errorf("retrieve(include_siblings=false).ClinicalLinks = %+v, want empty", noLinksOut.ClinicalLinks)
	}
}

// TestRetrieve_ClinicalLinksDropRestrictedEndpoint checks clearance
// inheritance on retrieve's clinical_links surfacing: a link whose other
// endpoint is access-denied for the viewer role must not appear in
// ClinicalLinks (mirrors TestGetLinks_DropsRestrictedLink's identical
// discipline, applied to retrieve's own surfacing rather than get_links).
func TestRetrieve_ClinicalLinksDropRestrictedEndpoint(t *testing.T) {
	e := openT(t)
	patient := seedPatient(t, e, "Clinical Links Clearance Patient")
	encounter := seedChildBead(t, e, patient, "fhir_encounter", nil, map[string]any{
		"reason": "acute kidney injury follow-up",
	})
	for i := 0; i < 10; i++ {
		seedChildBead(t, e, encounter, "fhir_observation",
			[]string{"loinc:noise-" + string(rune('a'+i))},
			map[string]any{"noise": i})
	}
	medication := seedChildBead(t, e, encounter, "fhir_medicationrequest",
		[]string{"risk:nephrotoxic", "atc:c09aa03"},
		map[string]any{"drug": "meropenem 1g IV every 8 hours"})
	observation := seedChildBead(t, e, encounter, "fhir_observation",
		[]string{"risk:nephrotoxic"},
		map[string]any{"test": "eGFR renal function panel"})

	rule := ingestT(t, e, projector.BuildCooccurrenceRuleBead("2026-01-01T00:00:00Z"))
	if _, err := projector.Reproject(e.Index(), engineReaderT{e}, []string{rule.ID}, "test-code-v1", "2026-07-11T00:00:00Z"); err != nil {
		t.Fatalf("Reproject: %v", err)
	}

	if err := clearance.SaveRule(e.Index(), clearance.Rule{
		ID:          "rule-" + observation.ID,
		BeadID:      observation.ID,
		DeniedRoles: []string{"viewer"},
		CreatedBy:   "test",
		CreatedAt:   "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("SaveRule: %v", err)
	}

	medicationView := bead.FormatID(medication.ID)
	observationView := bead.FormatID(observation.ID)

	viewer := newServerT(t, e, DefaultRole)
	_, viewerOut, err := viewer.retrieve(context.Background(), nil, retrieveIn{
		Query:       "meropenem",
		PatientID:   patient.ID,
		TokenBudget: 4000,
		ChainDepth:  5,
	})
	if err != nil {
		t.Fatalf("viewer retrieve: %v", err)
	}
	for _, link := range viewerOut.ClinicalLinks {
		if link.OtherBeadID == observationView {
			t.Fatalf("viewer retrieve ClinicalLinks leaked restricted endpoint: %+v", link)
		}
	}

	system := newServerT(t, e, SystemRole)
	_, systemOut, err := system.retrieve(context.Background(), nil, retrieveIn{
		Query:       "meropenem",
		PatientID:   patient.ID,
		TokenBudget: 4000,
		ChainDepth:  5,
	})
	if err != nil {
		t.Fatalf("system retrieve: %v", err)
	}
	found := false
	for _, link := range systemOut.ClinicalLinks {
		if link.BeadID == medicationView && link.OtherBeadID == observationView {
			found = true
		}
	}
	if !found {
		t.Fatalf("system retrieve ClinicalLinks missing the link; want it present (system bypasses clearance): %+v", systemOut.ClinicalLinks)
	}
}
