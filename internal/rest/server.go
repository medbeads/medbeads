package rest

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/medbeads/medbeads/internal/engine"
	"github.com/medbeads/medbeads/internal/engine/clearance"
	"github.com/medbeads/medbeads/internal/engine/pod"
)

// maxContextDepth caps the depth parameter accepted by /beads/context,
// ported verbatim from v2.2.0's core/api.maxContextDepth.
const maxContextDepth = 50

// defaultRateLimit is v2.2.0's core/api.StartServer default (600 req/min/IP)
// when MEDBEADS_RATE_LIMIT is unset. This package takes rateLimit as an
// explicit Config field instead of an env var read (see Config), but the
// default value itself is part of the frozen contract's operational
// behavior.
const defaultRateLimit = 600

// defaultCORSOrigins is v2.2.0's core/api.StartServer fallback allowlist
// when MEDBEADS_CORS_ORIGINS is unset — the UI dev server's own ports.
var defaultCORSOrigins = []string{
	"http://localhost:5173",
	"http://localhost:5174",
	"http://localhost:3000",
}

// Config bundles Server's construction arguments. Unlike v2.2.0's core/api
// (which read MEDBEADS_SERVICE_TOKEN / MEDBEADS_CORS_ORIGINS / MEDBEADS_RATE_LIMIT
// from the environment inside StartServer itself), this package takes them
// as explicit fields so cmd/medbeadsd's serve subcommand controls env-var
// parsing (and tests can construct a Server without touching process-global
// environment) — the parsing behavior each env var drove in v2 is preserved
// one level up, in cmd/medbeadsd, not lost.
type Config struct {
	// Engine is the already-Open'd engine this Server projects. New does not
	// take ownership of it: the caller (cmd/medbeadsd) opens and closes it.
	Engine *engine.Engine

	// ServiceToken is the shared secret that must accompany X-Service-Token
	// for a request to assert the privileged `system` role via
	// X-Viewer-Roles (v2.2.0's MEDBEADS_SERVICE_TOKEN). Empty means `system`
	// can never be asserted by any request, matching v2's documented
	// behavior for an unset token.
	ServiceToken string

	// CORSAllowedOrigins is the browser-origin allowlist (v2.2.0's
	// MEDBEADS_CORS_ORIGINS, comma-split by the caller before reaching
	// here). "*" restores allow-all. A nil/empty slice uses
	// defaultCORSOrigins (v2's own fallback).
	CORSAllowedOrigins []string

	// RateLimit is the per-IP requests/minute cap (v2.2.0's
	// MEDBEADS_RATE_LIMIT). Zero uses defaultRateLimit.
	RateLimit int
}

// Server holds the engine handle plus the small amount of request-scoped
// state (CORS allowlist, rate limiter, service token) v2.2.0's core/api kept
// as package-level globals. Bundling them into a struct (rather than
// reproducing the globals) makes this package safe to construct more than
// once in a test process without one test's Config leaking into another's,
// while every exported HTTP behavior below still matches v2.2.0's global-based
// implementation exactly.
type Server struct {
	eng          *engine.Engine
	store        *pod.Store
	serviceToken string
	corsOrigins  []string
	limiter      *rateLimiter
}

// New builds a Server over cfg.Engine, ready to have its Mux mounted into a
// caller-supplied http.ServeMux (cmd/medbeadsd's serve subcommand mounts it
// alongside /mcp on the same mux — see docs/requirements.md R6.1).
func New(cfg Config) (*Server, error) {
	if cfg.Engine == nil {
		return nil, errRequired("rest: new", "Config.Engine")
	}

	origins := cfg.CORSAllowedOrigins
	if len(origins) == 0 {
		origins = defaultCORSOrigins
	}
	rateLimit := cfg.RateLimit
	if rateLimit <= 0 {
		rateLimit = defaultRateLimit
	}

	return &Server{
		eng:          cfg.Engine,
		store:        pod.NewStore(cfg.Engine.DataDir()),
		serviceToken: cfg.ServiceToken,
		corsOrigins:  origins,
		limiter:      newRateLimiter(rateLimit),
	}, nil
}

// errRequired is a tiny fmt.Errorf-alike kept local to avoid importing fmt
// twice in a one-line constructor check; see New's Config.Engine guard.
func errRequired(op, field string) error {
	return &requiredFieldError{op: op, field: field}
}

type requiredFieldError struct{ op, field string }

func (e *requiredFieldError) Error() string {
	return e.op + ": " + e.field + " must not be nil"
}

// Mux returns an *http.ServeMux with every route this package serves
// (v2.2.0's core/api.StartServer registration list, same paths) registered,
// each wrapped in withRateLimit — ready for cmd/medbeadsd's serve subcommand
// to mount at the mux root alongside the MCP Streamable HTTP handler at
// /mcp.
func (s *Server) Mux() *http.ServeMux {
	mux := http.NewServeMux()
	handle := func(pattern string, h http.HandlerFunc) {
		mux.HandleFunc(pattern, s.withRateLimit(h))
	}
	handle("/beads", s.handleBeads)
	handle("/beads/context", s.handleContext)
	handle("/patients", s.handlePatients)
	handle("/search", s.handleSearch)
	handle("/resource-counts", s.handleResourceCounts)
	handle("/clearance", s.handleClearance)
	handle("/clearance/check", s.handleClearanceCheck)
	handle("/roles", s.handleRoles)
	return mux
}

// --- CORS / rate limit / viewer roles (ported from v2.2.0's core/api) ------

// rateLimiter is a simple fixed-window per-IP request counter, ported
// verbatim (in behavior) from v2.2.0's core/api.rateLimiter.
type rateLimiter struct {
	mu     sync.Mutex
	counts map[string]int
	window time.Time
	limit  int
}

func newRateLimiter(limit int) *rateLimiter {
	return &rateLimiter{counts: make(map[string]int), window: time.Now(), limit: limit}
}

// allow records a request from ip and reports whether it is within the limit.
func (rl *rateLimiter) allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	if time.Since(rl.window) > time.Minute {
		rl.counts = make(map[string]int)
		rl.window = time.Now()
	}
	rl.counts[ip]++
	return rl.counts[ip] <= rl.limit
}

// clientIP extracts the caller's IP, honoring X-Forwarded-For when present
// (ported verbatim from v2.2.0's core/api.clientIP).
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(strings.Split(xff, ",")[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// withRateLimit wraps a handler with per-IP rate limiting (ported verbatim
// from v2.2.0's core/api.withRateLimit).
func (s *Server) withRateLimit(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.limiter != nil && !s.limiter.allow(clientIP(r)) {
			s.setCORSHeaders(w, r)
			http.Error(w, "Rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next(w, r)
	}
}

// parseViewerRoles extracts viewer roles from the X-Viewer-Roles header,
// ported verbatim (in behavior) from v2.2.0's core/api.parseViewerRoles: a
// missing/empty header yields an empty role set (denied access to any Bead
// carrying an active clearance rule); the privileged `system` role is only
// honored with a valid X-Service-Token; `emergency` passes through
// unconditionally (every emergency access is audited — see
// auditEmergencyAccess).
func (s *Server) parseViewerRoles(r *http.Request) []string {
	header := r.Header.Get("X-Viewer-Roles")
	if header == "" {
		return []string{}
	}

	isService := s.serviceToken != "" && r.Header.Get("X-Service-Token") == s.serviceToken

	roles := []string{}
	for _, role := range strings.Split(header, ",") {
		role = strings.TrimSpace(role)
		if role == "" {
			continue
		}
		if role == clearance.RoleSystem && !isService {
			continue
		}
		roles = append(roles, role)
	}
	return roles
}

// setCORSHeaders sets CORS headers, reflecting the request Origin only when
// it is on the configured allowlist — ported verbatim from v2.2.0's
// core/api.setCORSHeaders.
func (s *Server) setCORSHeaders(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	for _, allowed := range s.corsOrigins {
		if allowed == "*" {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			break
		}
		if origin != "" && origin == allowed {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Add("Vary", "Origin")
			break
		}
	}
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Viewer-Roles, X-User-ID, X-Service-Token, X-Access-Reason")
}
