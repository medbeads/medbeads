package api

import (
	"net/http/httptest"
	"testing"
)

func TestSetCORSHeaders_AllowlistedOrigin(t *testing.T) {
	origCORS := corsAllowedOrigins
	corsAllowedOrigins = []string{"http://localhost:5173", "http://localhost:5174"}
	t.Cleanup(func() { corsAllowedOrigins = origCORS })

	r := httptest.NewRequest("GET", "/beads", nil)
	r.Header.Set("Origin", "http://localhost:5174")
	w := httptest.NewRecorder()

	setCORSHeaders(w, r)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:5174" {
		t.Errorf("Access-Control-Allow-Origin = %q, want the reflected origin", got)
	}
	if got := w.Header().Get("Vary"); got != "Origin" {
		t.Errorf("Vary = %q, want Origin", got)
	}
}

func TestSetCORSHeaders_BlockedOrigin(t *testing.T) {
	origCORS := corsAllowedOrigins
	corsAllowedOrigins = []string{"http://localhost:5173"}
	t.Cleanup(func() { corsAllowedOrigins = origCORS })

	r := httptest.NewRequest("GET", "/beads", nil)
	r.Header.Set("Origin", "http://evil.example.com")
	w := httptest.NewRecorder()

	setCORSHeaders(w, r)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want empty for a non-allowlisted origin", got)
	}
}

func TestSetCORSHeaders_WildcardRestoresAllowAll(t *testing.T) {
	origCORS := corsAllowedOrigins
	corsAllowedOrigins = []string{"*"}
	t.Cleanup(func() { corsAllowedOrigins = origCORS })

	r := httptest.NewRequest("GET", "/beads", nil)
	r.Header.Set("Origin", "http://anything.example.com")
	w := httptest.NewRecorder()

	setCORSHeaders(w, r)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Access-Control-Allow-Origin = %q, want * for a wildcard allowlist", got)
	}
}
