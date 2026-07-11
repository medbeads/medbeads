package mcpserver

import (
	"context"
	"testing"

	"github.com/medbeads/medbeads/internal/engine/bead"
)

// TestIntegration_RetrieveOneRoundTrip is this unit's headline M1
// integration test, updated for U5a (specs/U5_api_retrieve.md removed
// package apc and graph's sibling tiers entirely — clinical_links, U3, is
// now the sole link mechanism, surfaced as retrieve's ClinicalLinks sidecar
// rather than a context-bundle tier):
//
//  1. Ingest a small Synthea-shaped patient bundle: patient_registration ->
//     fhir_encounter -> fhir_medicationrequest -> fhir_observation (the
//     observation now a genuine *descendant* of the medicationrequest
//     anchor, not a same-parent sibling — the shape BuildContext's
//     anchor/ancestor/descendant tiers can still reach post-U5a).
//  2. Call the retrieve MCP tool handler directly, querying for the
//     medicationrequest's own content — one round trip, per DESIGN §8.
//
// Assertions: the medicationrequest anchor is present; the encounter
// ancestor is present; the observation descendant is present; everything
// fits within a budget generous enough to hold every Bead this small patient
// has (so nothing needs to spill to TruncatedRefs, isolating this test from
// budget-sizing details already covered by
// TestRetrieve_BudgetControlAndTruncatedRefs); and provenance on every
// non-anchor item correctly identifies how it was reached.
func TestIntegration_RetrieveOneRoundTrip(t *testing.T) {
	e := openT(t)

	patient := seedPatient(t, e, "Synthea Test Patient")
	encounter := seedChildBead(t, e, patient, "fhir_encounter", nil, map[string]any{
		"reason": "acute kidney injury follow-up",
	})
	medication := seedChildBead(t, e, encounter, "fhir_medicationrequest",
		[]string{"risk:nephrotoxic", "organ:renal"},
		map[string]any{"drug": "meropenem 1g IV every 8 hours"})
	observation := seedChildBead(t, e, medication, "fhir_observation",
		[]string{"risk:nephrotoxic", "organ:renal"},
		map[string]any{"test": "eGFR renal function panel"})

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
		t.Fatalf("retrieve did not include the descendant observation %s; Items=%+v", observationView, out.Items)
	}
	if observationItem.Provenance != "descendant" {
		t.Errorf("observation Provenance = %q, want descendant", observationItem.Provenance)
	}

	if len(out.TruncatedRefs) != 0 {
		t.Errorf("TruncatedRefs = %+v, want empty (4000-token budget should hold this whole small patient)", out.TruncatedRefs)
	}
	if out.UsedTokens > out.BudgetTokens {
		t.Errorf("UsedTokens = %d exceeds BudgetTokens = %d", out.UsedTokens, out.BudgetTokens)
	}
}
