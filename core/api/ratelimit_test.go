package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRateLimiter_Allow(t *testing.T) {
	rl := newRateLimiter(3)

	for i := 1; i <= 3; i++ {
		if !rl.allow("1.2.3.4") {
			t.Errorf("request %d within the limit should be allowed", i)
		}
	}
	if rl.allow("1.2.3.4") {
		t.Error("request 4 over the limit should be rejected")
	}
	// A different IP has its own independent budget.
	if !rl.allow("5.6.7.8") {
		t.Error("a different IP should not be affected by another IP's count")
	}
}

func TestClientIP_HonorsXForwardedFor(t *testing.T) {
	r := httptest.NewRequest("GET", "/beads", nil)
	r.RemoteAddr = "10.0.0.1:5555"
	r.Header.Set("X-Forwarded-For", "203.0.113.7, 10.0.0.1")
	if got := clientIP(r); got != "203.0.113.7" {
		t.Errorf("clientIP = %q, want 203.0.113.7 (first XFF entry)", got)
	}
}

func TestClientIP_FallsBackToRemoteAddr(t *testing.T) {
	r := httptest.NewRequest("GET", "/beads", nil)
	r.RemoteAddr = "198.51.100.4:9999"
	if got := clientIP(r); got != "198.51.100.4" {
		t.Errorf("clientIP = %q, want 198.51.100.4", got)
	}
}

func TestWithRateLimit_Returns429(t *testing.T) {
	origRL, origCORS := globalRateLimiter, corsAllowedOrigins
	globalRateLimiter = newRateLimiter(2)
	corsAllowedOrigins = []string{"*"}
	t.Cleanup(func() { globalRateLimiter, corsAllowedOrigins = origRL, origCORS })

	ok := func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }
	handler := withRateLimit(ok)

	codes := make([]int, 0, 3)
	for i := 0; i < 3; i++ {
		r := httptest.NewRequest("GET", "/beads", nil)
		r.RemoteAddr = "192.0.2.1:1234"
		w := httptest.NewRecorder()
		handler(w, r)
		codes = append(codes, w.Code)
	}
	if codes[0] != http.StatusOK || codes[1] != http.StatusOK {
		t.Errorf("first two requests should be 200, got %v", codes)
	}
	if codes[2] != http.StatusTooManyRequests {
		t.Errorf("third request should be 429, got %d", codes[2])
	}
}
