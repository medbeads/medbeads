package api

import (
	"net/http/httptest"
	"reflect"
	"sort"
	"testing"
)

func TestParseViewerRoles(t *testing.T) {
	origToken := serviceToken
	serviceToken = "secret"
	t.Cleanup(func() { serviceToken = origToken })

	cases := []struct {
		name      string
		header    string
		token     string
		setHeader bool
		want      []string
	}{
		{name: "missing header yields empty set", setHeader: false, want: []string{}},
		{name: "empty header yields empty set", header: "", setHeader: true, want: []string{}},
		{name: "ordinary roles pass through", header: "primary_care,nurse", setHeader: true, want: []string{"nurse", "primary_care"}},
		{name: "whitespace is trimmed", header: " primary_care , nurse ", setHeader: true, want: []string{"nurse", "primary_care"}},
		{name: "system is stripped without a token", header: "system,primary_care", setHeader: true, want: []string{"primary_care"}},
		{name: "system is stripped with a wrong token", header: "system", token: "wrong", setHeader: true, want: []string{}},
		{name: "system is kept with a valid token", header: "system", token: "secret", setHeader: true, want: []string{"system"}},
		{name: "emergency is always kept", header: "emergency", setHeader: true, want: []string{"emergency"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/beads", nil)
			if tc.setHeader {
				r.Header.Set("X-Viewer-Roles", tc.header)
			}
			if tc.token != "" {
				r.Header.Set("X-Service-Token", tc.token)
			}
			got := parseViewerRoles(r)
			sort.Strings(got)
			sort.Strings(tc.want)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseViewerRoles() = %v, want %v", got, tc.want)
			}
		})
	}
}
