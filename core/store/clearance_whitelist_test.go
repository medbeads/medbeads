package store

import (
	"testing"

	"github.com/sojin25/medbeads/core/types"
)

// ruleWith builds an active clearance rule with explicit denied/allowed roles.
func ruleWith(denied, allowed []string) types.ClearanceRule {
	return types.ClearanceRule{
		ID:           "r",
		BeadID:       "b",
		DeniedRoles:  denied,
		AllowedRoles: allowed,
		CreatedBy:    "test",
		CreatedAt:    "2026-01-01T00:00:00Z",
	}
}

func TestHasAccessWithRules_AllowedRoles(t *testing.T) {
	cases := []struct {
		name        string
		rules       []types.ClearanceRule
		viewerRoles []string
		want        bool
	}{
		{
			"whitelisted role is allowed",
			[]types.ClearanceRule{ruleWith(nil, []string{"dept:genetics"})},
			[]string{"specialist", "dept:genetics"}, true,
		},
		{
			"non-whitelisted role is blocked",
			[]types.ClearanceRule{ruleWith(nil, []string{"dept:genetics"})},
			[]string{"specialist", "dept:cardiology"}, false,
		},
		{
			"viewer with no matching dept is blocked",
			[]types.ClearanceRule{ruleWith(nil, []string{"dept:genetics"})},
			[]string{"primary_care"}, false,
		},
		{
			"emergency bypasses a whitelist rule",
			[]types.ClearanceRule{ruleWith(nil, []string{"dept:genetics"})},
			[]string{"emergency"}, true,
		},
		{
			"denied wins even when also whitelisted",
			[]types.ClearanceRule{ruleWith([]string{"family"}, []string{"family", "dept:genetics"})},
			[]string{"family"}, false,
		},
		{
			"denied and allowed combined: allowed role not denied passes",
			[]types.ClearanceRule{ruleWith([]string{"family"}, []string{"dept:genetics"})},
			[]string{"dept:genetics"}, true,
		},
		{
			"two whitelist rules are AND-combined",
			[]types.ClearanceRule{
				ruleWith(nil, []string{"dept:genetics"}),
				ruleWith(nil, []string{"dept:cardiology"}),
			},
			[]string{"dept:genetics"}, false,
		},
		{
			"empty allowed_roles behaves as no whitelist",
			[]types.ClearanceRule{ruleWith([]string{"insurance"}, []string{})},
			[]string{"primary_care"}, true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := HasAccessWithRules(tc.rules, tc.viewerRoles); got != tc.want {
				t.Errorf("HasAccessWithRules = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestSaveClearanceRule_AllowedRolesRoundTrip verifies allowed_roles survives a
// save/load cycle through SQLite.
func TestSaveClearanceRule_AllowedRolesRoundTrip(t *testing.T) {
	setupTestStore(t)

	patient := seedPatient(t, "Genetics Patient")
	rule := types.ClearanceRule{
		ID:           "wl-rule",
		BeadID:       patient,
		DeniedRoles:  []string{},
		AllowedRoles: []string{"dept:genetics"},
		CreatedBy:    "test",
		CreatedAt:    "2026-01-01T00:00:00Z",
	}
	if err := SaveClearanceRule(rule); err != nil {
		t.Fatalf("SaveClearanceRule: %v", err)
	}

	got, err := GetClearanceRules(patient)
	if err != nil {
		t.Fatalf("GetClearanceRules: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d rules, want 1", len(got))
	}
	if len(got[0].AllowedRoles) != 1 || got[0].AllowedRoles[0] != "dept:genetics" {
		t.Errorf("AllowedRoles round-trip = %v, want [dept:genetics]", got[0].AllowedRoles)
	}

	// Only the genetics department may access; others are denied.
	if ok, _ := HasAccess(patient, []string{"dept:genetics"}); !ok {
		t.Error("dept:genetics should have access")
	}
	if ok, _ := HasAccess(patient, []string{"specialist", "dept:cardiology"}); ok {
		t.Error("dept:cardiology should be denied access")
	}
}
