package store

import (
	"testing"

	"github.com/sojin25/medbeads/core/types"
)

func activeRule(denied ...string) types.ClearanceRule {
	return types.ClearanceRule{
		ID:          "r",
		BeadID:      "b",
		DeniedRoles: denied,
		CreatedBy:   "test",
		CreatedAt:   "2026-01-01T00:00:00Z",
	}
}

func TestHasAccessWithRules(t *testing.T) {
	cases := []struct {
		name        string
		rules       []types.ClearanceRule
		viewerRoles []string
		want        bool
	}{
		{"no rules grants access", nil, []string{"insurance"}, true},
		{"denied role is blocked", []types.ClearanceRule{activeRule("insurance")}, []string{"insurance"}, false},
		{"non-denied role is allowed", []types.ClearanceRule{activeRule("insurance")}, []string{"primary_care"}, true},
		{"emergency bypasses a denying rule", []types.ClearanceRule{activeRule("emergency", "insurance")}, []string{"emergency"}, true},
		{"system bypasses a denying rule", []types.ClearanceRule{activeRule("insurance")}, []string{"system"}, true},
		{"zero roles denied when an active rule exists", []types.ClearanceRule{activeRule("insurance")}, []string{}, false},
		{"zero roles allowed when no rule exists", nil, []string{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := HasAccessWithRules(tc.rules, tc.viewerRoles); got != tc.want {
				t.Errorf("HasAccessWithRules(%v, %v) = %v, want %v", tc.rules, tc.viewerRoles, got, tc.want)
			}
		})
	}
}

func TestHasAccess_RoundTrip(t *testing.T) {
	setupTestStore(t)

	patient := seedPatient(t, "Restricted Patient")
	seedClearanceRule(t, patient, []string{"insurance"}, nil)

	denied, err := HasAccess(patient, []string{"insurance"})
	if err != nil {
		t.Fatalf("HasAccess: %v", err)
	}
	if denied {
		t.Error("insurance role should be denied access to the restricted bead")
	}

	allowed, err := HasAccess(patient, []string{"primary_care"})
	if err != nil {
		t.Fatalf("HasAccess: %v", err)
	}
	if !allowed {
		t.Error("primary_care role should have access to the restricted bead")
	}
}

func TestFilterByAccess_MasksRestrictedContent(t *testing.T) {
	setupTestStore(t)

	patient := seedPatient(t, "Patient")
	psych := seedChildBead(t, patient, "fhir_condition", map[string]interface{}{
		"diagnosis": "psychiatric evaluation",
	})
	seedClearanceRule(t, psych, []string{"insurance"}, nil)

	patientBead, err := LoadFromCAS(patient)
	if err != nil {
		t.Fatalf("load patient: %v", err)
	}
	psychBead, err := LoadFromCAS(psych)
	if err != nil {
		t.Fatalf("load psych: %v", err)
	}

	filtered, err := FilterByAccess([]types.Bead{patientBead, psychBead}, []string{"insurance"})
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
	if masked.ID != psych {
		t.Errorf("masked bead lost its ID: got %q want %q", masked.ID, psych)
	}
	if masked.Type != "fhir_condition" {
		t.Errorf("masked bead lost its Type: got %q", masked.Type)
	}
	if len(masked.Parents) != 1 || masked.Parents[0] != patient {
		t.Errorf("masked bead lost its Parents: got %v", masked.Parents)
	}
}

func TestFilterByAccess_AuthorizedSeesContent(t *testing.T) {
	setupTestStore(t)

	patient := seedPatient(t, "Patient")
	psych := seedChildBead(t, patient, "fhir_condition", map[string]interface{}{
		"diagnosis": "psychiatric evaluation",
	})
	seedClearanceRule(t, psych, []string{"insurance"}, nil)

	psychBead, _ := LoadFromCAS(psych)
	filtered, err := FilterByAccess([]types.Bead{psychBead}, []string{"primary_care"})
	if err != nil {
		t.Fatalf("FilterByAccess: %v", err)
	}
	if filtered[0].Content["diagnosis"] != "psychiatric evaluation" {
		t.Errorf("primary_care should see full content, got %v", filtered[0].Content)
	}
}
