package clearance_test

import (
	"testing"

	"github.com/medbeads/medbeads/internal/engine/bead"
	"github.com/medbeads/medbeads/internal/engine/clearance"
)

// activeRule builds a permanent (non-expiring) clearance.Rule denying the
// given roles, mirroring v2.2.0's core/store.activeRule test helper.
func activeRule(denied ...string) clearance.Rule {
	return clearance.Rule{
		ID:          "r",
		BeadID:      "b",
		DeniedRoles: denied,
		CreatedBy:   "test",
		CreatedAt:   "2026-01-01T00:00:00Z",
	}
}

// TestHasAccessWithRules ports v2.2.0's core/store.TestHasAccessWithRules
// verbatim (case-for-case).
func TestHasAccessWithRules(t *testing.T) {
	cases := []struct {
		name        string
		rules       []clearance.Rule
		viewerRoles []string
		want        bool
	}{
		{"no rules grants access", nil, []string{"insurance"}, true},
		{"denied role is blocked", []clearance.Rule{activeRule("insurance")}, []string{"insurance"}, false},
		{"non-denied role is allowed", []clearance.Rule{activeRule("insurance")}, []string{"primary_care"}, true},
		{"emergency bypasses a denying rule", []clearance.Rule{activeRule("emergency", "insurance")}, []string{"emergency"}, true},
		{"system bypasses a denying rule", []clearance.Rule{activeRule("insurance")}, []string{"system"}, true},
		{"zero roles denied when an active rule exists", []clearance.Rule{activeRule("insurance")}, []string{}, false},
		{"zero roles allowed when no rule exists", nil, []string{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := clearance.HasAccessWithRules(tc.rules, tc.viewerRoles); got != tc.want {
				t.Errorf("HasAccessWithRules(%v, %v) = %v, want %v", tc.rules, tc.viewerRoles, got, tc.want)
			}
		})
	}
}

// TestHasAccess_RoundTrip ports v2.2.0's core/store.TestHasAccess_RoundTrip,
// adapted to v3's engine.Open/Ingest + clearance.SaveRule/HasAccess.
func TestHasAccess_RoundTrip(t *testing.T) {
	e := openT(t)

	patient := seedPatient(t, e, "Restricted Patient")
	seedClearanceRule(t, e, patient.ID, []string{"insurance"}, nil)

	denied, err := clearance.HasAccess(e.Index(), patient, []string{"insurance"})
	if err != nil {
		t.Fatalf("HasAccess: %v", err)
	}
	if denied {
		t.Error("insurance role should be denied access to the restricted bead")
	}

	allowed, err := clearance.HasAccess(e.Index(), patient, []string{"primary_care"})
	if err != nil {
		t.Fatalf("HasAccess: %v", err)
	}
	if !allowed {
		t.Error("primary_care role should have access to the restricted bead")
	}
}

// TestFilterByAccess_MasksRestrictedContent ports v2.2.0's core/store.
// TestFilterByAccess_MasksRestrictedContent.
func TestFilterByAccess_MasksRestrictedContent(t *testing.T) {
	e := openT(t)

	patient := seedPatient(t, e, "Patient")
	psych := seedChildBead(t, e, patient, "fhir_condition", map[string]any{
		"diagnosis": "psychiatric evaluation",
	})
	seedClearanceRule(t, e, psych.ID, []string{"insurance"}, nil)

	patientBead, err := e.GetBead(patient.ID)
	if err != nil {
		t.Fatalf("get patient: %v", err)
	}
	psychBead, err := e.GetBead(psych.ID)
	if err != nil {
		t.Fatalf("get psych: %v", err)
	}

	filtered, err := clearance.FilterByAccess(e.Index(), []bead.Bead{patientBead, psychBead}, []string{"insurance"})
	if err != nil {
		t.Fatalf("FilterByAccess: %v", err)
	}
	if len(filtered) != 2 {
		t.Fatalf("FilterByAccess returned %d beads, want 2", len(filtered))
	}

	// The unrestricted patient bead passes through unchanged.
	if filtered[0].Content["name"] != "Patient" {
		t.Errorf("patient bead content was altered: %v", filtered[0].Content)
	}

	// The restricted bead is masked but keeps its graph metadata.
	masked := filtered[1]
	if masked.Content["_restricted"] != true {
		t.Errorf("restricted bead content = %v, want {_restricted: true}", masked.Content)
	}
	if _, leaked := masked.Content["diagnosis"]; leaked {
		t.Error("restricted bead leaked its diagnosis content")
	}
	if masked.ID != psych.ID {
		t.Errorf("masked bead lost its ID: got %q want %q", masked.ID, psych.ID)
	}
	if masked.Type != "fhir_condition" {
		t.Errorf("masked bead lost its Type: got %q", masked.Type)
	}
	if len(masked.Parents) != 1 || masked.Parents[0] != patient.ID {
		t.Errorf("masked bead lost its Parents: got %v", masked.Parents)
	}
}

// TestFilterByAccess_AuthorizedSeesContent ports v2.2.0's core/store.
// TestFilterByAccess_AuthorizedSeesContent.
func TestFilterByAccess_AuthorizedSeesContent(t *testing.T) {
	e := openT(t)

	patient := seedPatient(t, e, "Patient")
	psych := seedChildBead(t, e, patient, "fhir_condition", map[string]any{
		"diagnosis": "psychiatric evaluation",
	})
	seedClearanceRule(t, e, psych.ID, []string{"insurance"}, nil)

	psychBead, err := e.GetBead(psych.ID)
	if err != nil {
		t.Fatalf("get psych: %v", err)
	}
	filtered, err := clearance.FilterByAccess(e.Index(), []bead.Bead{psychBead}, []string{"primary_care"})
	if err != nil {
		t.Fatalf("FilterByAccess: %v", err)
	}
	if filtered[0].Content["diagnosis"] != "psychiatric evaluation" {
		t.Errorf("primary_care should see full content, got %v", filtered[0].Content)
	}
}
