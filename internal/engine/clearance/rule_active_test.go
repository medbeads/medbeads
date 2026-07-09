package clearance_test

import (
	"testing"
	"time"

	"github.com/medbeads/medbeads/internal/engine/clearance"
)

func strptr(s string) *string { return &s }

// TestIsRuleActive ports v2.2.0's core/store.TestIsRuleActive verbatim
// (case-for-case).
func TestIsRuleActive(t *testing.T) {
	now := time.Date(2026, 5, 22, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name      string
		expiresAt *string
		want      bool
	}{
		{"nil expiry is permanent and active", nil, true},
		{"empty expiry is permanent and active", strptr(""), true},
		{"future expiry is active", strptr("2026-12-31T00:00:00Z"), true},
		{"past expiry is inactive", strptr("2026-01-01T00:00:00Z"), false},
		{"unparseable expiry is treated as active (fail-closed)", strptr("not-a-date"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rule := clearance.Rule{ExpiresAt: tc.expiresAt}
			if got := clearance.IsRuleActive(rule, now); got != tc.want {
				t.Errorf("IsRuleActive(expires=%v) = %v, want %v", tc.expiresAt, got, tc.want)
			}
		})
	}
}
