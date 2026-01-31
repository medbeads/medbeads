package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sojin25/medbeads/core/store"
	"github.com/sojin25/medbeads/core/types"
)

// StartServer starts the MedBeads Core HTTP server.
func StartServer(port string) error {
	http.HandleFunc("/beads", handleBeads)
	http.HandleFunc("/beads/context", handleContext)
	http.HandleFunc("/patients", handlePatients)
	http.HandleFunc("/search", handleSearch)
	http.HandleFunc("/resource-counts", handleResourceCounts)
	http.HandleFunc("/clearance", handleClearance)
	http.HandleFunc("/clearance/check", handleClearanceCheck)
	http.HandleFunc("/roles", handleRoles)
	fmt.Printf("🚀 MedBeads Core Server running on port %s\n", port)
	return http.ListenAndServe(port, nil)
}

// parseViewerRoles extracts viewer roles from X-Viewer-Roles header
// If not present, returns system role (full access for backward compatibility)
func parseViewerRoles(r *http.Request) []string {
	header := r.Header.Get("X-Viewer-Roles")
	if header == "" {
		return []string{types.RoleSystem}
	}

	var roles []string
	for _, role := range strings.Split(header, ",") {
		role = strings.TrimSpace(role)
		if role != "" {
			roles = append(roles, role)
		}
	}

	if len(roles) == 0 {
		return []string{types.RoleSystem}
	}
	return roles
}

// setCORSHeaders sets standard CORS headers
func setCORSHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Viewer-Roles, X-User-ID")
}

// handleRoles returns available roles
func handleRoles(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(types.AllRoles)
}

// handleClearance handles CRUD operations for clearance rules
func handleClearance(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w)

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
	BeadID      string   `json:"bead_id"`
	DeniedRoles []string `json:"denied_roles"`
	Reason      string   `json:"reason,omitempty"`
	ExpiresAt   *string  `json:"expires_at,omitempty"`
}

func createClearanceHandler(w http.ResponseWriter, r *http.Request) {
	var req CreateClearanceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if req.BeadID == "" || len(req.DeniedRoles) == 0 {
		http.Error(w, "bead_id and denied_roles are required", http.StatusBadRequest)
		return
	}

	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		userID = "unknown"
	}

	// Generate unique ID using sha256
	idData := fmt.Sprintf("%s-%s-%d", req.BeadID, userID, time.Now().UnixNano())
	hash := sha256.Sum256([]byte(idData))
	ruleID := hex.EncodeToString(hash[:16]) // Use first 16 bytes (32 hex chars)

	rule := types.ClearanceRule{
		ID:          ruleID,
		BeadID:      req.BeadID,
		DeniedRoles: req.DeniedRoles,
		CreatedBy:   userID,
		CreatedAt:   time.Now().Format(time.RFC3339),
		Reason:      req.Reason,
		ExpiresAt:   req.ExpiresAt,
	}

	if err := store.SaveClearanceRule(rule); err != nil {
		http.Error(w, fmt.Sprintf("Failed to save clearance rule: %v", err), http.StatusInternalServerError)
		return
	}

	// Log the action
	viewerRoles := parseViewerRoles(r)
	store.LogClearanceAction(req.BeadID, "created", userID, viewerRoles, fmt.Sprintf("Denied roles: %v", req.DeniedRoles))

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
	setCORSHeaders(w)

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

	// Log emergency access
	if !hasAccess {
		for _, role := range viewerRoles {
			if role == types.RoleEmergency {
				userID := r.Header.Get("X-User-ID")
				if userID == "" {
					userID = "unknown"
				}
				store.LogClearanceAction(beadID, "emergency_access", userID, viewerRoles, "Emergency access override")
				break
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"has_access": hasAccess})
}

func handleSearch(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w)

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

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(patients)
}

func handleContext(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w)

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
		fmt.Sscanf(depthStr, "%d", &depth)
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

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(beads)
}

func handleBeads(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w)

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

	data, err := store.GetFromCAS(id)
	if err != nil {
		http.Error(w, "Bead not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

func handlePatients(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w)

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

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(patients)
}

func handleResourceCounts(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w)

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
