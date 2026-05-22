package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sojin25/medbeads/core/store"
	"github.com/sojin25/medbeads/core/types"
)

// maxContextDepth caps the depth parameter accepted by /beads/context.
const maxContextDepth = 50

// serviceToken is the shared secret that internal services must present (via
// the X-Service-Token header) to be allowed to assert the privileged `system`
// role. It is read once at startup from MEDBEADS_SERVICE_TOKEN. When empty,
// the `system` role can never be asserted via a request.
var serviceToken string

// corsAllowedOrigins is the allowlist of browser origins permitted by CORS.
// Read once at startup from MEDBEADS_CORS_ORIGINS (comma-separated). The
// literal "*" entry restores the previous allow-all behavior.
var corsAllowedOrigins []string

// globalRateLimiter throttles requests per client IP.
var globalRateLimiter *rateLimiter

// StartServer starts the MedBeads Core HTTP server.
func StartServer(port string) error {
	serviceToken = os.Getenv("MEDBEADS_SERVICE_TOKEN")
	if serviceToken == "" {
		fmt.Println("⚠️  MEDBEADS_SERVICE_TOKEN is not set: the 'system' role cannot be asserted by any request")
	}

	corsAllowedOrigins = parseCSVEnv("MEDBEADS_CORS_ORIGINS",
		"http://localhost:5173,http://localhost:5174,http://localhost:3000")

	rateLimit := 600
	if v := os.Getenv("MEDBEADS_RATE_LIMIT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			rateLimit = n
		}
	}
	globalRateLimiter = newRateLimiter(rateLimit)

	handle := func(pattern string, h http.HandlerFunc) {
		http.HandleFunc(pattern, withRateLimit(h))
	}
	handle("/beads", handleBeads)
	handle("/beads/context", handleContext)
	handle("/patients", handlePatients)
	handle("/search", handleSearch)
	handle("/resource-counts", handleResourceCounts)
	handle("/clearance", handleClearance)
	handle("/clearance/check", handleClearanceCheck)
	handle("/roles", handleRoles)
	fmt.Printf("🚀 MedBeads Core Server running on port %s (rate limit: %d req/min/IP)\n", port, rateLimit)
	return http.ListenAndServe(port, nil)
}

// parseCSVEnv reads a comma-separated env var, trimming entries, with a fallback.
func parseCSVEnv(name, fallback string) []string {
	raw := os.Getenv(name)
	if raw == "" {
		raw = fallback
	}
	var out []string
	for _, item := range strings.Split(raw, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// rateLimiter is a simple fixed-window per-IP request counter.
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

// clientIP extracts the caller's IP, honoring X-Forwarded-For when present.
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

// withRateLimit wraps a handler with per-IP rate limiting.
func withRateLimit(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if globalRateLimiter != nil && !globalRateLimiter.allow(clientIP(r)) {
			setCORSHeaders(w, r)
			http.Error(w, "Rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next(w, r)
	}
}

// parseViewerRoles extracts viewer roles from the X-Viewer-Roles header.
// A missing/empty header yields an empty role set (no identified role), which
// is denied access to any bead carrying an active clearance rule.
// The privileged `system` role is only honored when the request carries a
// valid X-Service-Token; otherwise it is stripped. `emergency` is kept but
// every emergency access is audited (see auditEmergencyAccess).
func parseViewerRoles(r *http.Request) []string {
	header := r.Header.Get("X-Viewer-Roles")
	if header == "" {
		return []string{}
	}

	isService := serviceToken != "" && r.Header.Get("X-Service-Token") == serviceToken

	roles := []string{}
	for _, role := range strings.Split(header, ",") {
		role = strings.TrimSpace(role)
		if role == "" {
			continue
		}
		// The `system` role bypasses all clearance rules and may only be
		// asserted by an authenticated internal service.
		if role == types.RoleSystem && !isService {
			continue
		}
		roles = append(roles, role)
	}

	return roles
}

// setCORSHeaders sets CORS headers, reflecting the request Origin only when it
// is on the configured allowlist (MEDBEADS_CORS_ORIGINS).
func setCORSHeaders(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	for _, allowed := range corsAllowedOrigins {
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

// auditEmergencyAccess writes a clearance_audit entry for every accessed bead
// that carries an active clearance rule, when the viewer is using the
// `emergency` role. This makes emergency overrides fully accountable.
func auditEmergencyAccess(r *http.Request, beads []types.Bead, viewerRoles []string) {
	hasEmergency := false
	for _, role := range viewerRoles {
		if role == types.RoleEmergency {
			hasEmergency = true
			break
		}
	}
	if !hasEmergency || len(beads) == 0 {
		return
	}

	beadIDs := make([]string, 0, len(beads))
	for _, b := range beads {
		if b.ID != "" {
			beadIDs = append(beadIDs, b.ID)
		}
	}

	rulesMap, err := store.GetAllClearanceRulesForBeads(beadIDs)
	if err != nil {
		fmt.Printf("⚠️  auditEmergencyAccess: failed to load clearance rules: %v\n", err)
		return
	}

	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		userID = "unknown"
	}
	reason := r.Header.Get("X-Access-Reason")
	now := time.Now()

	for _, id := range beadIDs {
		active := false
		for _, rule := range rulesMap[id] {
			if store.IsRuleActive(rule, now) {
				active = true
				break
			}
		}
		if !active {
			continue
		}
		details := "Emergency access override"
		if reason != "" {
			details += " - reason: " + reason
		}
		store.LogClearanceAction(id, "emergency_access", userID, viewerRoles, details)
	}
}

// handleRoles returns available roles
func handleRoles(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w, r)
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(types.AllRoles)
}

// handleClearance handles CRUD operations for clearance rules
func handleClearance(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w, r)

	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	switch r.Method {
	case "GET":
		getClearanceHandler(w, r)
	case "POST":
		createClearanceHandler(w, r)
	case "DELETE":
		deleteClearanceHandler(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func getClearanceHandler(w http.ResponseWriter, r *http.Request) {
	beadID := r.URL.Query().Get("bead_id")
	if beadID == "" {
		http.Error(w, "Missing 'bead_id' parameter", http.StatusBadRequest)
		return
	}

	rules, err := store.GetClearanceRules(beadID)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to get clearance rules: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(rules)
}

type CreateClearanceRequest struct {
	BeadID       string   `json:"bead_id"`
	DeniedRoles  []string `json:"denied_roles"`
	AllowedRoles []string `json:"allowed_roles,omitempty"`
	Reason       string   `json:"reason,omitempty"`
	ExpiresAt    *string  `json:"expires_at,omitempty"`
}

func createClearanceHandler(w http.ResponseWriter, r *http.Request) {
	var req CreateClearanceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// A rule must restrict something: either a blacklist or a whitelist.
	if req.BeadID == "" || (len(req.DeniedRoles) == 0 && len(req.AllowedRoles) == 0) {
		http.Error(w, "bead_id and at least one of denied_roles / allowed_roles are required", http.StatusBadRequest)
		return
	}

	// An identified user is required to create a clearance rule (accountability).
	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		http.Error(w, "X-User-ID header is required to create a clearance rule", http.StatusUnauthorized)
		return
	}

	// The target bead must exist.
	if _, err := store.GetFromCAS(req.BeadID); err != nil {
		http.Error(w, "bead_id does not exist", http.StatusNotFound)
		return
	}

	// Validate denied roles: each must be a known role (functional or dept:*).
	// Denying `system`/`emergency` is meaningless since they bypass clearance.
	for _, role := range req.DeniedRoles {
		if !types.IsValidRole(role) {
			http.Error(w, fmt.Sprintf("invalid role: %q", role), http.StatusBadRequest)
			return
		}
		if role == types.RoleSystem || role == types.RoleEmergency {
			http.Error(w, fmt.Sprintf("role %q cannot be denied (it bypasses clearance)", role), http.StatusBadRequest)
			return
		}
	}

	// Validate allowed roles (whitelist): each must be a known role.
	for _, role := range req.AllowedRoles {
		if !types.IsValidRole(role) {
			http.Error(w, fmt.Sprintf("invalid role: %q", role), http.StatusBadRequest)
			return
		}
	}

	// Generate unique ID using sha256
	idData := fmt.Sprintf("%s-%s-%d", req.BeadID, userID, time.Now().UnixNano())
	hash := sha256.Sum256([]byte(idData))
	ruleID := hex.EncodeToString(hash[:16]) // Use first 16 bytes (32 hex chars)

	rule := types.ClearanceRule{
		ID:           ruleID,
		BeadID:       req.BeadID,
		DeniedRoles:  req.DeniedRoles,
		AllowedRoles: req.AllowedRoles,
		CreatedBy:    userID,
		CreatedAt:    time.Now().Format(time.RFC3339),
		Reason:       req.Reason,
		ExpiresAt:    req.ExpiresAt,
	}

	if err := store.SaveClearanceRule(rule); err != nil {
		http.Error(w, fmt.Sprintf("Failed to save clearance rule: %v", err), http.StatusInternalServerError)
		return
	}

	// Log the action
	viewerRoles := parseViewerRoles(r)
	store.LogClearanceAction(req.BeadID, "created", userID, viewerRoles, fmt.Sprintf("Denied roles: %v, Allowed roles: %v", req.DeniedRoles, req.AllowedRoles))

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(rule)
}

func deleteClearanceHandler(w http.ResponseWriter, r *http.Request) {
	ruleID := r.URL.Query().Get("id")
	if ruleID == "" {
		http.Error(w, "Missing 'id' parameter", http.StatusBadRequest)
		return
	}

	if err := store.DeleteClearanceRule(ruleID); err != nil {
		http.Error(w, fmt.Sprintf("Failed to delete clearance rule: %v", err), http.StatusInternalServerError)
		return
	}

	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		userID = "unknown"
	}

	// Log the action
	viewerRoles := parseViewerRoles(r)
	store.LogClearanceAction(ruleID, "deleted", userID, viewerRoles, "Rule deleted")

	w.WriteHeader(http.StatusNoContent)
}

// handleClearanceCheck checks if a viewer has access to a bead
func handleClearanceCheck(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w, r)

	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	beadID := r.URL.Query().Get("bead_id")
	if beadID == "" {
		http.Error(w, "Missing 'bead_id' parameter", http.StatusBadRequest)
		return
	}

	viewerRoles := parseViewerRoles(r)
	hasAccess, err := store.HasAccess(beadID, viewerRoles)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to check access: %v", err), http.StatusInternalServerError)
		return
	}

	// Emergency access is audited centrally via auditEmergencyAccess on the
	// data-returning endpoints (/beads, /beads/context, /search, /patients).

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"has_access": hasAccess})
}

func handleSearch(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w, r)

	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	query := r.URL.Query().Get("q")

	// Get resource types filter
	resourceTypesStr := r.URL.Query().Get("resourceTypes")
	var resourceTypes []string
	if resourceTypesStr != "" {
		// Parse comma-separated resource types
		for _, rt := range strings.Split(resourceTypesStr, ",") {
			rt = strings.TrimSpace(rt)
			if rt != "" {
				resourceTypes = append(resourceTypes, rt)
			}
		}
	}

	var patients []types.Bead
	var err error

	// Use new function with resource type filtering
	if len(resourceTypes) > 0 || query == "" {
		patients, err = store.SearchPatientsByContentWithResourceTypes(query, resourceTypes)
	} else {
		// Fallback to old function for backward compatibility
		patients, err = store.SearchPatientsByContent(query)
	}

	if err != nil {
		http.Error(w, fmt.Sprintf("Search failed: %v", err), http.StatusInternalServerError)
		return
	}

	// Filter by viewer roles
	viewerRoles := parseViewerRoles(r)
	patients, err = store.FilterByAccess(patients, viewerRoles)
	if err != nil {
		http.Error(w, fmt.Sprintf("Access filter failed: %v", err), http.StatusInternalServerError)
		return
	}
	auditEmergencyAccess(r, patients, viewerRoles)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(patients)
}

func handleContext(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w, r)

	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	id := r.URL.Query().Get("id")
	depthStr := r.URL.Query().Get("depth")

	if id == "" {
		http.Error(w, "Missing 'id' parameter", http.StatusBadRequest)
		return
	}

	depth := 5 // Default depth
	if depthStr != "" {
		parsed, err := strconv.Atoi(depthStr)
		if err != nil {
			http.Error(w, "Invalid 'depth' parameter: must be an integer", http.StatusBadRequest)
			return
		}
		depth = parsed
	}
	if depth < 1 || depth > maxContextDepth {
		http.Error(w, fmt.Sprintf("'depth' must be between 1 and %d", maxContextDepth), http.StatusBadRequest)
		return
	}

	var beads []types.Bead
	var err error

	lookupType := r.URL.Query().Get("lookup")
	if lookupType == "reverse" {
		beads, err = store.GetBeadsByParent(id, depth)
	} else {
		beads, err = store.GetContext(id, depth)
	}

	if err != nil {
		http.Error(w, "Failed to retrieve context", http.StatusInternalServerError)
		return
	}

	// Filter by viewer roles
	viewerRoles := parseViewerRoles(r)
	beads, err = store.FilterByAccess(beads, viewerRoles)
	if err != nil {
		http.Error(w, fmt.Sprintf("Access filter failed: %v", err), http.StatusInternalServerError)
		return
	}
	auditEmergencyAccess(r, beads, viewerRoles)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(beads)
}

func handleBeads(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w, r)

	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	if r.Method == "POST" {
		saveHandler(w, r)
		return
	}

	if r.Method == "GET" {
		getBeadHandler(w, r)
		return
	}

	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
}

func saveHandler(w http.ResponseWriter, r *http.Request) {
	var bead types.Bead

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read body", http.StatusBadRequest)
		return
	}

	if err := json.Unmarshal(body, &bead); err != nil {
		http.Error(w, "Invalid JSON format", http.StatusBadRequest)
		return
	}

	if bead.Timestamp == "" {
		bead.Timestamp = time.Now().Format(time.RFC3339)
	}

	hashID, err := store.SaveToCAS(bead)
	if err != nil {
		if errors.Is(err, store.ErrCycleDetected) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		http.Error(w, "Failed to save data", http.StatusInternalServerError)
		return
	}

	response := map[string]string{
		"status": "success",
		"id":     hashID,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)

	fmt.Printf("📥 Received & Saved: %s (Type: %s)\n", hashID, bead.Type)
}

func getBeadHandler(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "Missing 'id' parameter", http.StatusBadRequest)
		return
	}

	// Check access before returning the bead
	viewerRoles := parseViewerRoles(r)
	hasAccess, err := store.HasAccess(id, viewerRoles)
	if err != nil {
		http.Error(w, fmt.Sprintf("Access check failed: %v", err), http.StatusInternalServerError)
		return
	}

	if !hasAccess {
		http.Error(w, "Access denied", http.StatusForbidden)
		return
	}

	auditEmergencyAccess(r, []types.Bead{{ID: id}}, viewerRoles)

	data, err := store.GetFromCAS(id)
	if err != nil {
		http.Error(w, "Bead not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

func handlePatients(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w, r)

	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	patients, err := store.GetPatients()
	if err != nil {
		http.Error(w, "Failed to retrieve patients", http.StatusInternalServerError)
		return
	}

	// Filter by viewer roles
	viewerRoles := parseViewerRoles(r)
	patients, err = store.FilterByAccess(patients, viewerRoles)
	if err != nil {
		http.Error(w, fmt.Sprintf("Access filter failed: %v", err), http.StatusInternalServerError)
		return
	}
	auditEmergencyAccess(r, patients, viewerRoles)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(patients)
}

func handleResourceCounts(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w, r)

	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	counts, err := store.GetResourceTypeCounts()
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to get counts: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(counts)
}
