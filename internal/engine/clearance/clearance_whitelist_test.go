package clearance_test

import (
	"testing"

	"github.com/medbeads/medbeads/internal/engine/clearance"
)

// ruleWith builds an active clearance.Rule with explicit denied/allowed
// roles, mirroring v2.2.0's core/store.ruleWith test helper.
func ruleWith(denied, allowed []string) clearance.Rule {
	return clearance.Rule{
		ID:           "r",
		BeadID:       "b",
		DeniedRoles:  denied,
		AllowedRoles: allowed,
		CreatedBy:    "test",
		CreatedAt:    "2026-01-01T00:00:00Z",
	}
}

// TestHasAccessWithRules_AllowedRoles ports v2.2.0's core/store.
// TestHasAccessWithRules_AllowedRoles verbatim (case-for-case).
func TestHasAccessWithRules_AllowedRoles(t *testing.T) {
	cases := []struct {
		name        string
		rules       []clearance.Rule
		viewerRoles []string
		want        bool
	}{
		{
			"whitelisted role is allowed",
			[]clearance.Rule{ruleWith(nil, []string{"dept:genetics"})},
			[]string{"specialist", "dept:genetics"}, true,
		},
		{
			"non-whitelisted role is blocked",
			[]clearance.Rule{ruleWith(nil, []string{"dept:genetics"})},
			[]string{"specialist", "dept:cardiology"}, false,
		},
		{
			"viewer with no matching dept is blocked",
			[]clearance.Rule{ruleWith(nil, []string{"dept:genetics"})},
			[]string{"primary_care"}, false,
		},
		{
			"emergency bypasses a whitelist rule",
			[]clearance.Rule{ruleWith(nil, []string{"dept:genetics"})},
			[]string{"emergency"}, true,
		},
		{
			"denied wins even when also whitelisted",
			[]clearance.Rule{ruleWith([]string{"family"}, []string{"family", "dept:genetics"})},
			[]string{"family"}, false,
		},
		{
			"denied and allowed combined: allowed role not denied passes",
			[]clearance.Rule{ruleWith([]string{"family"}, []string{"dept:genetics"})},
			[]string{"dept:genetics"}, true,
		},
		{
			"two whitelist rules are AND-combined",
			[]clearance.Rule{
				ruleWith(nil, []string{"dept:genetics"}),
				ruleWith(nil, []string{"dept:cardiology"}),
			},
			[]string{"dept:genetics"}, false,
		},
		{
			"empty allowed_roles behaves as no whitelist",
			[]clearance.Rule{ruleWith([]string{"insurance"}, []string{})},
			[]string{"primary_care"}, true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := clearance.HasAccessWithRules(tc.rules, tc.viewerRoles); got != tc.want {
				t.Errorf("HasAccessWithRules = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestSaveRule_AllowedRolesRoundTrip ports v2.2.0's core/store.
// TestSaveClearanceRule_AllowedRolesRoundTrip, adapted to v3's
// engine.Open/Ingest + clearance.SaveRule/GetRules/HasAccess.
func TestSaveRule_AllowedRolesRoundTrip(t *testing.T) {
	e := openT(t)

	patient := seedPatient(t, e, "Genetics Patient")
	rule := clearance.Rule{
		ID:           "wl-rule",
		BeadID:       patient.ID,
		DeniedRoles:  []string{},
		AllowedRoles: []string{"dept:genetics"},
		CreatedBy:    "test",
		CreatedAt:    "2026-01-01T00:00:00Z",
	}
	if err := clearance.SaveRule(e.Index(), rule); err != nil {
		t.Fatalf("SaveRule: %v", err)
	}

	got, err := clearance.GetRules(e.Index(), patient.ID)
	if err != nil {
		t.Fatalf("GetRules: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d rules, want 1", len(got))
	}
	if len(got[0].AllowedRoles) != 1 || got[0].AllowedRoles[0] != "dept:genetics" {
		t.Errorf("AllowedRoles round-trip = %v, want [dept:genetics]", got[0].AllowedRoles)
	}

	// Only the genetics department may access; others are denied.
	if ok, _ := clearance.HasAccess(e.Index(), patient, []string{"dept:genetics"}); !ok {
		t.Error("dept:genetics should have access")
	}
	if ok, _ := clearance.HasAccess(e.Index(), patient, []string{"specialist", "dept:cardiology"}); ok {
		t.Error("dept:cardiology should be denied access")
	}
}
