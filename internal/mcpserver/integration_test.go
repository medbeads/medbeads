package mcpserver

import (
	"context"
	"testing"

	"github.com/medbeads/medbeads/internal/engine"
	"github.com/medbeads/medbeads/internal/engine/apc"
	"github.com/medbeads/medbeads/internal/engine/bead"
)

// TestIntegration_RetrieveOneRoundTrip is this unit's headline M1
// integration test (task: "engine に Synthea 風の小さな患者(registration +
// medication + observation、共通 antigen で APC が sibling_link を作る状態)
// を ingest → retrieve 1往復で anchor + sibling + 祖先が予算内に入る"):
//
//  1. Ingest a small Synthea-shaped patient bundle: patient_registration ->
//     fhir_encounter -> {fhir_medicationrequest, fhir_observation}, the
//     medicationrequest and observation sharing risk:/organ: antigens (the
//     same shared-antigen shape apc_test.go's own headline test uses, scaled
//     up with noise Beads to clear the IDF frequency filter).
//  2. Run apc.Scanner.Scan to convergence so the shared-antigen pair
//     produces a sibling_link Bead + bidirectional 'sibling' bead_edges rows
//     (docs/requirements.md R5), exactly as a real ingest+scan cycle would.
//  3. Call the retrieve MCP tool handler directly, querying for the
//     medicationrequest's own content — one round trip, per DESIGN §8.
//
// Assertions: the medicationrequest anchor is present; its sibling (the
// observation, reached via the sibling_link's bead_edges rows, injected via
// loadExplicitSiblingEdges) is present; the encounter ancestor is present;
// everything fits within a budget generous enough to hold every Bead this
// small patient has (so nothing needs to spill to TruncatedRefs, isolating
// this test from budget-sizing details already covered by
// TestRetrieve_BudgetControlAndTruncatedRefs); and provenance on every
// non-anchor item correctly identifies how it was reached.
func TestIntegration_RetrieveOneRoundTrip(t *testing.T) {
	e := openT(t)

	patient := seedPatient(t, e, "Synthea Test Patient")
	encounter := seedChildBead(t, e, patient, "fhir_encounter", nil, map[string]any{
		"reason": "acute kidney injury follow-up",
	})

	// Noise Beads keep risk:nephrotoxic/organ:renal comfortably under the
	// APC scanner's default 30% patient-local IDF frequency threshold (see
	// apc/apc_test.go's padWithNoiseBeads, which this mirrors).
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
	var scanRes apc.Result
	for i := 0; i < 10; i++ {
		res, err := scanner.Scan()
		if err != nil {
			t.Fatalf("Scan: %v", err)
		}
		scanRes = res
		if res.BeadsScanned == 0 {
			break
		}
	}
	if scanRes.SiblingLinksCreated == 0 && !siblingLinkExists(t, e) {
		t.Fatalf("APC scan produced no sibling_link Bead for the shared risk:/organ: antigens; test setup assumption violated")
	}

	s := newServerT(t, e, SystemRole)

	_, out, err := s.retrieve(context.Background(), nil, retrieveIn{
		Query:       "meropenem",
		PatientID:   patient.ID,
		TokenBudget: 4000, // generous: this whole small patient should fit with no truncation
		ChainDepth:  5,
	})
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}

	medicationView := bead.FormatID(medication.ID)
	encounterView := bead.FormatID(encounter.ID)
	observationView := bead.FormatID(observation.ID)

	byID := make(map[string]contextItemView, len(out.Items))
	for _, item := range out.Items {
		byID[item.ID] = item.contextItemView
	}

	anchorItem, ok := byID[medicationView]
	if !ok {
		t.Fatalf("retrieve did not include the anchor medicationrequest %s; Items=%+v", medicationView, out.Items)
	}
	if anchorItem.Provenance != "anchor" {
		t.Errorf("anchor Provenance = %q, want anchor", anchorItem.Provenance)
	}
	if anchorItem.Granularity != "L0" {
		t.Errorf("anchor Granularity = %q, want L0 (should fit at full content under a 4000-token budget)", anchorItem.Granularity)
	}

	encounterItem, ok := byID[encounterView]
	if !ok {
		t.Fatalf("retrieve did not include the ancestor encounter %s; Items=%+v", encounterView, out.Items)
	}
	if encounterItem.Provenance != "ancestor" {
		t.Errorf("encounter Provenance = %q, want ancestor", encounterItem.Provenance)
	}

	observationItem, ok := byID[observationView]
	if !ok {
		t.Fatalf("retrieve did not include the sibling observation %s (via the APC sibling_link's bead_edges rows); Items=%+v", observationView, out.Items)
	}
	if observationItem.Provenance != "sibling" {
		t.Errorf("observation Provenance = %q, want sibling (reached via the sibling_link edge)", observationItem.Provenance)
	}

	if len(out.TruncatedRefs) != 0 {
		t.Errorf("TruncatedRefs = %+v, want empty (4000-token budget should hold this whole small patient)", out.TruncatedRefs)
	}
	if out.UsedTokens > out.BudgetTokens {
		t.Errorf("UsedTokens = %d exceeds BudgetTokens = %d", out.UsedTokens, out.BudgetTokens)
	}
}

// siblingLinkExists is a fallback check for
// TestIntegration_RetrieveOneRoundTrip: the convergence loop above already
// accumulates SiblingLinksCreated across every Scan() call in the common
// case, but this queries the beads table directly (any row of
// type='sibling_link') as a defensive double-check independent of that
// accumulation logic.
func siblingLinkExists(t *testing.T, e *engine.Engine) bool {
	t.Helper()
	var n int
	if err := e.Index().SQLDB().QueryRow(
		`SELECT COUNT(*) FROM beads WHERE type = 'sibling_link'`,
	).Scan(&n); err != nil {
		t.Fatalf("siblingLinkExists: query: %v", err)
	}
	return n > 0
}
