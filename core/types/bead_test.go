package types

import "testing"

func TestIsValidRole(t *testing.T) {
	cases := []struct {
		role string
		want bool
	}{
		{"primary_care", true},
		{"family", true},
		{"emergency", true},
		{"system", true},
		{"dept:psychiatry", true},
		{"dept:genetics", true},
		{"dept:cardiology", true},
		{"dept:unknown_department", false},
		{"dept:", false},
		{"psychiatry", false}, // bare department name without the dept: prefix
		{"", false},
		{"administrator", false},
	}
	for _, tc := range cases {
		t.Run(tc.role, func(t *testing.T) {
			if got := IsValidRole(tc.role); got != tc.want {
				t.Errorf("IsValidRole(%q) = %v, want %v", tc.role, got, tc.want)
			}
		})
	}
}
