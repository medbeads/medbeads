package rest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/medbeads/medbeads/internal/engine/bead"
	"github.com/medbeads/medbeads/internal/engine/clearance"
	"github.com/medbeads/medbeads/internal/engine/graph"
	"github.com/medbeads/medbeads/internal/engine/index"
)

// handleRoles returns available roles — ported from v2.2.0's
// core/api.handleRoles (GET types.AllRoles as a JSON array of strings).
func (s *Server) handleRoles(w http.ResponseWriter, r *http.Request) {
	s.setCORSHeaders(w, r)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	writeJSON(w, http.StatusOK, clearance.AllRoles)
}

// handlePatients returns every patient_registration Bead, masked (not
// dropped) per viewer role — ported from v2.2.0's core/api.handlePatients.
func (s *Server) handlePatients(w http.ResponseWriter, r *http.Request) {
	s.setCORSHeaders(w, r)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	refs, err := s.eng.Index().ListPatients()
	if err != nil {
		http.Error(w, "Failed to retrieve patients", http.StatusInternalServerError)
		return
	}
	beads, err := s.loadBeads(refs)
	if err != nil {
		http.Error(w, "Failed to retrieve patients", http.StatusInternalServerError)
		return
	}

	viewerRoles := s.parseViewerRoles(r)
	filtered, err := clearance.FilterByAccess(s.eng.Index(), beads, viewerRoles)
	if err != nil {
		http.Error(w, fmt.Sprintf("Access filter failed: %v", err), http.StatusInternalServerError)
		return
	}
	s.auditEmergencyAccess(r, filtered, viewerRoles)

	writeJSON(w, http.StatusOK, newBeadViews(filtered))
}

// handleSearch is v2.2.0's core/api.handleSearch /
// core/store.SearchPatientsByContentWithResourceTypes ported: unlike every
// other endpoint in this package, /search's result is NOT the matched Beads
// themselves — it resolves every match up to its owning patient's
// patient_registration Bead and returns the deduplicated set of *patient*
// Beads (mapBeadToPatient in ui/src/lib/api.ts assumes exactly this shape:
// content.name/birthDate/gender, i.e. a patient_registration Bead's own
// Content). A query hit on, say, an fhir_observation Bead surfaces its
// patient in the results list, not the observation itself.
//
// v2 branched on whether queryText was empty: empty query + resourceTypes
// set returned every patient having at least one Bead of a matching type
// (searchByResourceTypes, no FTS involved); a non-empty query ran FTS5 MATCH
// (optionally AND'ed with the type filter) and resolved each hit's patient.
// This matters because ui/src/components/PatientSidebar.tsx's debounced
// search effect calls searchPatients("", selectedResourceTypes) whenever a
// resource-type filter chip is toggled with no search text typed — an
// empty-query, type-filter-only request is a real, reachable UI path, not a
// theoretical one.
//
// v3's beads.patient_root is pre-resolved at Ingest (specs/DESIGN_v3.md §3),
// so "resolve a hit to its patient" here is a lookup, not v2's per-hit
// findPatientRoot parent-chain walk.
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	s.setCORSHeaders(w, r)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	query := r.URL.Query().Get("q")
	var resourceTypes []string
	if rt := r.URL.Query().Get("resourceTypes"); rt != "" {
		for _, t := range strings.Split(rt, ",") {
			if t = strings.TrimSpace(t); t != "" {
				resourceTypes = append(resourceTypes, t)
			}
		}
	}
	beadTypes := resourceTypeBeadTypeSet(resourceTypes)

	var patientRoots []string
	var err error
	if query == "" && beadTypes != nil {
		patientRoots, err = s.patientRootsByType(beadTypes)
	} else {
		patientRoots, err = s.patientRootsByQuery(query, beadTypes)
	}
	if err != nil {
		http.Error(w, fmt.Sprintf("Search failed: %v", err), http.StatusInternalServerError)
		return
	}

	beads, err := s.loadBeadsByID(patientRoots)
	if err != nil {
		http.Error(w, fmt.Sprintf("Search failed: %v", err), http.StatusInternalServerError)
		return
	}

	viewerRoles := s.parseViewerRoles(r)
	filtered, err := clearance.FilterByAccess(s.eng.Index(), beads, viewerRoles)
	if err != nil {
		http.Error(w, fmt.Sprintf("Access filter failed: %v", err), http.StatusInternalServerError)
		return
	}
	s.auditEmergencyAccess(r, filtered, viewerRoles)

	writeJSON(w, http.StatusOK, newBeadViews(filtered))
}

// patientRootsByType resolves every distinct patient_root that has at least
// one indexed Bead whose type is in beadTypes — v2.2.0's searchByResourceTypes,
// minus its per-row findPatientRoot walk (v3's patient_root column is
// already resolved; a patient_registration Bead is its own root, matching
// v2's `if bType == "patient_registration" { matchedPatientID = id }`
// special case since such a row's own patient_root is its own id).
func (s *Server) patientRootsByType(beadTypes map[string]bool) ([]string, error) {
	types := make([]string, 0, len(beadTypes))
	for t := range beadTypes {
		types = append(types, t)
	}
	placeholders := make([]string, len(types))
	args := make([]any, len(types))
	for i, t := range types {
		placeholders[i] = "?"
		args[i] = t
	}
	query := fmt.Sprintf(
		`SELECT DISTINCT COALESCE(patient_root, id) FROM beads WHERE type IN (%s)`,
		strings.Join(placeholders, ","))

	rows, err := s.eng.Index().SQLDB().Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("rest: patient roots by type: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var root string
		if err := rows.Scan(&root); err != nil {
			return nil, fmt.Errorf("rest: patient roots by type: scan: %w", err)
		}
		if root != "" {
			out = append(out, root)
		}
	}
	return out, rows.Err()
}

// patientRootsByQuery runs the FTS5 anchor search and resolves each hit
// (optionally restricted to beadTypes) to its owning patient_root,
// deduplicated — v2.2.0's searchWithTypeFilter minus the AND-of-comma-terms
// and snippet bookkeeping (see handleSearch's doc comment: this package
// omits v2's _snippet content, a presentation nicety ui/src/lib/api.ts
// already renders conditionally, `patient.snippet &&`, degrading gracefully
// to none — not a status-code/shape contract element).
func (s *Server) patientRootsByQuery(query string, beadTypes map[string]bool) ([]string, error) {
	if query == "" {
		return nil, nil
	}
	hits, err := s.eng.Index().Search(query, 0)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]bool)
	var out []string
	for _, hit := range hits {
		if beadTypes != nil && !beadTypes[hit.Type] {
			continue
		}
		root := hit.PatientRoot
		if hit.Type == "patient_registration" {
			root = hit.BeadID
		}
		if root == "" || seen[root] {
			continue
		}
		seen[root] = true
		out = append(out, root)
	}
	return out, nil
}

// loadBeadsByID resolves ids' full content via engine.GetBead, skipping any
// id that fails to resolve (v2.2.0's LoadFromCAS-in-a-loop `if err == nil`
// pattern), preserving nil-ness like loadBeads (see its doc comment).
func (s *Server) loadBeadsByID(ids []string) ([]bead.Bead, error) {
	var out []bead.Bead
	for _, id := range ids {
		b, err := s.eng.GetBead(id)
		if err != nil {
			continue
		}
		out = append(out, b)
	}
	return out, nil
}

// resourceTypeBeadTypeSet mirrors v2.2.0's core/store's UI-resource-type ->
// underlying-Bead-type mapping (both the legacy short type and its
// "fhir_"-prefixed counterpart). A nil return means "no filter" (every type
// passes), matching v2's behavior when resourceTypes is empty.
func resourceTypeBeadTypeSet(resourceTypes []string) map[string]bool {
	if len(resourceTypes) == 0 {
		return nil
	}
	out := make(map[string]bool)
	for _, rt := range resourceTypes {
		for _, t := range resourceTypeBeadTypes[rt] {
			out[t] = true
		}
	}
	return out
}

// resourceTypeBeadTypes is v2.2.0's core/store.SearchPatientsByContentWithResourceTypes
// / GetResourceTypeCounts switch statement, ported as a table.
var resourceTypeBeadTypes = map[string][]string{
	"encounter":         {"encounter", "fhir_encounter"},
	"medication":        {"medication", "fhir_medicationrequest"},
	"observation":       {"observation", "fhir_observation"},
	"condition":         {"condition", "fhir_condition"},
	"diagnostic_report": {"diagnostic_report", "fhir_diagnosticreport", "fhir_documentreference"},
	"procedure":         {"fhir_procedure"},
	"immunization":      {"fhir_immunization"},
	"imaging_study":     {"fhir_imagingstudy"},
}

// resourceTypeOrder is a fixed iteration order over resourceTypeBeadTypes,
// matching v2.2.0's core/store.GetResourceTypeCounts's literal slice
// (map iteration order is otherwise undefined, and the response is a JSON
// array whose element order a UI could reasonably render in).
var resourceTypeOrder = []string{
	"encounter", "medication", "observation", "condition",
	"diagnostic_report", "procedure", "immunization", "imaging_study",
}

// handleResourceCounts returns, for each UI-facing resource type, the number
// of distinct patients that have at least one Bead of that type — ported
// from v2.2.0's core/api.handleResourceCounts / core/store.GetResourceTypeCounts.
//
// v2 resolved each matched Bead's patient via findPatientRoot (an O(N)
// parent-chain walk per Bead, since v2's schema had no pre-resolved
// patient_root column). v3's beads.patient_root is resolved once at Ingest
// time (specs/DESIGN_v3.md §3, R3.1's "N+1 の根治"), so this is a single
// COUNT(DISTINCT patient_root) per type — the same aggregate v2 computed,
// computed the way this project's schema is designed to compute it.
func (s *Server) handleResourceCounts(w http.ResponseWriter, r *http.Request) {
	s.setCORSHeaders(w, r)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	db := s.eng.Index().SQLDB()
	out := make([]resourceTypeCount, 0, len(resourceTypeOrder))
	for _, rt := range resourceTypeOrder {
		types := resourceTypeBeadTypes[rt]
		placeholders := make([]string, len(types))
		args := make([]any, len(types))
		for i, t := range types {
			placeholders[i] = "?"
			args[i] = t
		}
		query := fmt.Sprintf(
			`SELECT COUNT(DISTINCT COALESCE(patient_root, id)) FROM beads WHERE type IN (%s)`,
			strings.Join(placeholders, ","))

		var count int
		if err := db.QueryRow(query, args...).Scan(&count); err != nil {
			http.Error(w, fmt.Sprintf("Failed to get counts: %v", err), http.StatusInternalServerError)
			return
		}
		out = append(out, resourceTypeCount{ResourceType: rt, PatientCount: count})
	}

	writeJSON(w, http.StatusOK, out)
}

// handleContext serves /beads/context: the default (ancestor) walk mirrors
// v2.2.0's core/store.GetContext, and lookup=reverse mirrors v2's
// GetBeadsByParent (descendant walk) — ported from v2.2.0's
// core/api.handleContext. Both walks are now graph.Bundle.Ancestors/
// Descendants (an in-memory BFS over one patient's already-loaded pack-file
// bundle, per graph/bfs.go's own doc comment naming these as the direct
// replacements for GetContext/GetBeadsByParent), rather than v2's one-SQL-
// query-per-hop loop.
func (s *Server) handleContext(w http.ResponseWriter, r *http.Request) {
	s.setCORSHeaders(w, r)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "Missing 'id' parameter", http.StatusBadRequest)
		return
	}

	depth := 5
	if depthStr := r.URL.Query().Get("depth"); depthStr != "" {
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

	ref, err := s.eng.Index().GetBead(id)
	if err != nil {
		if errors.Is(err, index.ErrNotFound) {
			// v2's GetContext/GetBeadsByParent silently skip a Bead ID that
			// GetFromCAS cannot resolve rather than erroring — an unresolvable
			// start ID walks to an empty result, not a 404/500.
			writeJSON(w, http.StatusOK, []beadView(nil))
			return
		}
		http.Error(w, "Failed to retrieve context", http.StatusInternalServerError)
		return
	}
	if ref.PatientRoot == "" {
		// A shared-Pod Bead has no patient bundle to walk (graph.LoadBundle
		// requires a non-empty patientRoot); v2 had no such split (every Bead
		// lived in one CAS keyed purely by content hash), but a Bead with no
		// resolvable patient bundle here has, by definition, no ancestors/
		// descendants graph.Bundle can reach either — an empty result matches
		// the same "nothing found" outcome v2's walk would reach.
		writeJSON(w, http.StatusOK, []beadView{})
		return
	}

	bd, err := graph.LoadBundle(s.store, ref.PatientRoot)
	if err != nil {
		http.Error(w, "Failed to retrieve context", http.StatusInternalServerError)
		return
	}

	var beads []bead.Bead
	if r.URL.Query().Get("lookup") == "reverse" {
		beads = bd.Descendants(id, depth)
	} else {
		beads = bd.Ancestors(id, depth)
	}

	viewerRoles := s.parseViewerRoles(r)
	filtered, err := clearance.FilterByAccess(s.eng.Index(), beads, viewerRoles)
	if err != nil {
		http.Error(w, fmt.Sprintf("Access filter failed: %v", err), http.StatusInternalServerError)
		return
	}
	s.auditEmergencyAccess(r, filtered, viewerRoles)

	writeJSON(w, http.StatusOK, newBeadViews(filtered))
}

// handleBeads dispatches GET (fetch one Bead) — ported from v2.2.0's
// core/api.handleBeads. v2 also dispatched POST here (bead ingest); this
// package does not register a write path (see doc.go's "Ported vs. excluded
// endpoints" — writes are MCP-only, R6.3), so POST/any other method falls
// through to 405 same as any method v2 itself did not recognize.
//
// Order note: v2's getBeadHandler checked HasAccess(id, roles) (a bare-ID,
// DB-rules-only check) before GetFromCAS, so a denied-but-nonexistent ID
// with a stray clearance_rules row would 403 rather than 404. This handler
// resolves the Bead first (engine.GetBead) because clearance.HasAccess must
// have the full bead.Bead in hand to also honor its embedded Clearance
// overlay (v3-only, no v2 equivalent — see clearance/doc.go's "Two layers"),
// which every masking endpoint in this package (handlePatients/handleSearch/
// handleContext, via clearance.FilterByAccess) already applies; excluding it
// here for the sake of matching v2's exact check order would make this the
// one endpoint in the package that ignores embedded clearance, a worse
// inconsistency than the reordering. For every realistic case (a Bead that
// either exists or genuinely has no clearance state), the resulting status
// code is identical to v2's: 403 for an existing, denied Bead (see
// handlers_test.go); 404 for a Bead absent entirely.
func (s *Server) handleBeads(w http.ResponseWriter, r *http.Request) {
	s.setCORSHeaders(w, r)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "Missing 'id' parameter", http.StatusBadRequest)
		return
	}

	b, err := s.eng.GetBead(id)
	if err != nil {
		http.Error(w, "Bead not found", http.StatusNotFound)
		return
	}

	viewerRoles := s.parseViewerRoles(r)
	hasAccess, err := clearance.HasAccess(s.eng.Index(), b, viewerRoles)
	if err != nil {
		http.Error(w, fmt.Sprintf("Access check failed: %v", err), http.StatusInternalServerError)
		return
	}
	if !hasAccess {
		http.Error(w, "Access denied", http.StatusForbidden)
		return
	}
	s.auditEmergencyAccess(r, []bead.Bead{b}, viewerRoles)

	writeJSON(w, http.StatusOK, newBeadView(b))
}

// --- clearance --------------------------------------------------------

// handleClearance dispatches GET/POST/DELETE for clearance_rules — ported
// from v2.2.0's core/api.handleClearance.
func (s *Server) handleClearance(w http.ResponseWriter, r *http.Request) {
	s.setCORSHeaders(w, r)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.getClearanceHandler(w, r)
	case http.MethodPost:
		s.createClearanceHandler(w, r)
	case http.MethodDelete:
		s.deleteClearanceHandler(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) getClearanceHandler(w http.ResponseWriter, r *http.Request) {
	beadID := r.URL.Query().Get("bead_id")
	if beadID == "" {
		http.Error(w, "Missing 'bead_id' parameter", http.StatusBadRequest)
		return
	}

	rules, err := clearance.GetRules(s.eng.Index(), beadID)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to get clearance rules: %v", err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, newClearanceRuleViews(rules))
}

// createClearanceRequest is v2.2.0's core/api.CreateClearanceRequest JSON
// shape verbatim, matching ui/src/lib/api.ts's CreateClearanceRequest.
type createClearanceRequest struct {
	BeadID       string   `json:"bead_id"`
	DeniedRoles  []string `json:"denied_roles"`
	AllowedRoles []string `json:"allowed_roles,omitempty"`
	Reason       string   `json:"reason,omitempty"`
	ExpiresAt    *string  `json:"expires_at,omitempty"`
}

func (s *Server) createClearanceHandler(w http.ResponseWriter, r *http.Request) {
	var req createClearanceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if req.BeadID == "" || (len(req.DeniedRoles) == 0 && len(req.AllowedRoles) == 0) {
		http.Error(w, "bead_id and at least one of denied_roles / allowed_roles are required", http.StatusBadRequest)
		return
	}

	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		http.Error(w, "X-User-ID header is required to create a clearance rule", http.StatusUnauthorized)
		return
	}

	if _, err := s.eng.GetBead(req.BeadID); err != nil {
		http.Error(w, "bead_id does not exist", http.StatusNotFound)
		return
	}

	for _, role := range req.DeniedRoles {
		if !clearance.IsValidRole(role) {
			http.Error(w, fmt.Sprintf("invalid role: %q", role), http.StatusBadRequest)
			return
		}
		if role == clearance.RoleSystem || role == clearance.RoleEmergency {
			http.Error(w, fmt.Sprintf("role %q cannot be denied (it bypasses clearance)", role), http.StatusBadRequest)
			return
		}
	}
	for _, role := range req.AllowedRoles {
		if !clearance.IsValidRole(role) {
			http.Error(w, fmt.Sprintf("invalid role: %q", role), http.StatusBadRequest)
			return
		}
	}

	now := time.Now()
	ruleID := newClearanceRuleID(req.BeadID, userID, now)

	rule := clearance.Rule{
		ID:           ruleID,
		BeadID:       req.BeadID,
		DeniedRoles:  req.DeniedRoles,
		AllowedRoles: req.AllowedRoles,
		CreatedBy:    userID,
		CreatedAt:    now.Format(time.RFC3339),
		Reason:       req.Reason,
		ExpiresAt:    req.ExpiresAt,
	}
	if err := clearance.SaveRule(s.eng.Index(), rule); err != nil {
		http.Error(w, fmt.Sprintf("Failed to save clearance rule: %v", err), http.StatusInternalServerError)
		return
	}

	viewerRoles := s.parseViewerRoles(r)
	details := fmt.Sprintf("Denied roles: %v, Allowed roles: %v", req.DeniedRoles, req.AllowedRoles)
	if err := clearance.LogAction(s.eng.Index(), req.BeadID, "created", userID, viewerRoles, details, now.Format(time.RFC3339)); err != nil {
		http.Error(w, fmt.Sprintf("Failed to log clearance action: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(newClearanceRuleView(rule))
}

func (s *Server) deleteClearanceHandler(w http.ResponseWriter, r *http.Request) {
	ruleID := r.URL.Query().Get("id")
	if ruleID == "" {
		http.Error(w, "Missing 'id' parameter", http.StatusBadRequest)
		return
	}

	if err := clearance.DeleteRule(s.eng.Index(), ruleID); err != nil {
		http.Error(w, fmt.Sprintf("Failed to delete clearance rule: %v", err), http.StatusInternalServerError)
		return
	}

	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		userID = "unknown"
	}
	viewerRoles := s.parseViewerRoles(r)
	if err := clearance.LogAction(s.eng.Index(), ruleID, "deleted", userID, viewerRoles, "Rule deleted", time.Now().Format(time.RFC3339)); err != nil {
		http.Error(w, fmt.Sprintf("Failed to log clearance action: %v", err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleClearanceCheck checks if a viewer has access to a bead — ported from
// v2.2.0's core/api.handleClearanceCheck / core/store.HasAccess(beadID, ...).
//
// v2's HasAccess took a bare bead_id string and never required the Bead to
// exist (it only consults clearance_rules by ID; a bead_id with no rows and
// no Bead at all simply has no restrictions, so has_access is true) — unlike
// getBeadHandler/createClearanceHandler, which do require existence. This
// package therefore calls clearance.GetRules (DB rules only) +
// clearance.HasAccessWithRules directly rather than routing through
// engine.GetBead + clearance.HasAccess (which additionally requires
// resolving the Bead itself, to also honor its embedded bead.Clearance
// overlay — a v3-only addition with no v2 equivalent, see
// clearance/doc.go's "Two layers"): using the full-Bead path here would
// silently 404 a bead_id this endpoint's frozen v2 contract must accept.
func (s *Server) handleClearanceCheck(w http.ResponseWriter, r *http.Request) {
	s.setCORSHeaders(w, r)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	beadID := r.URL.Query().Get("bead_id")
	if beadID == "" {
		http.Error(w, "Missing 'bead_id' parameter", http.StatusBadRequest)
		return
	}

	viewerRoles := s.parseViewerRoles(r)
	rules, err := clearance.GetRules(s.eng.Index(), beadID)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to check access: %v", err), http.StatusInternalServerError)
		return
	}

	hasAccess := clearance.HasAccessWithRules(rules, viewerRoles)
	writeJSON(w, http.StatusOK, map[string]bool{"has_access": hasAccess})
}

// --- shared helpers -----------------------------------------------------

// loadBeads resolves refs' full content via engine.GetBead, in ref order,
// skipping (not failing the whole request for) any single ref that no longer
// resolves — mirroring v2.2.0's GetPatients/GetBeadsByParent/GetContext loops,
// which each `continue`d past a GetFromCAS/json.Unmarshal failure for one row
// rather than aborting the entire response. Returns nil (not an empty
// non-nil slice) when refs is empty or every ref fails to resolve, so the
// caller's clearance.FilterByAccess -> newBeadViews -> writeJSON pipeline
// emits JSON `null` for zero results exactly as v2.2.0's `var patients
// []types.Bead` result variables did (see newBeadViews' doc comment) —
// ui/src/lib/api.ts's searchPatients/fetchPatientTimeline are not defensive
// against a `[]` vs `null` mismatch here (unlike fetchAllPatients' `response.data
// || []`), so this is required for contract fidelity, not cosmetic.
func (s *Server) loadBeads(refs []index.BeadRef) ([]bead.Bead, error) {
	var out []bead.Bead
	for _, ref := range refs {
		b, err := s.eng.GetBead(ref.ID)
		if err != nil {
			continue
		}
		out = append(out, b)
	}
	return out, nil
}

// auditEmergencyAccess writes a clearance_audit entry for every accessed
// bead that carries an active clearance rule, when the viewer is using the
// `emergency` role — ported from v2.2.0's core/api.auditEmergencyAccess.
func (s *Server) auditEmergencyAccess(r *http.Request, beads []bead.Bead, viewerRoles []string) {
	hasEmergency := false
	for _, role := range viewerRoles {
		if role == clearance.RoleEmergency {
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

	rulesMap, err := clearance.GetRulesForBeads(s.eng.Index(), beadIDs)
	if err != nil {
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
			if clearance.IsRuleActive(rule, now) {
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
		_ = clearance.LogAction(s.eng.Index(), id, "emergency_access", userID, viewerRoles, details, now.Format(time.RFC3339))
	}
}

// writeJSON writes v as an indent-free JSON body with status code, matching
// v2.2.0's core/api handlers' plain json.NewEncoder(w).Encode(v) (no
// pretty-printing — this package's output is machine-consumed by the UI's
// axios client, not eyeballed).
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// newClearanceRuleID generates a clearance rule ID exactly as v2.2.0's
// core/api.createClearanceHandler did: sha256("<bead_id>-<user_id>-<unix_nano>"),
// first 16 bytes hex-encoded (32 hex chars).
func newClearanceRuleID(beadID, userID string, t time.Time) string {
	idData := fmt.Sprintf("%s-%s-%d", beadID, userID, t.UnixNano())
	sum := sha256.Sum256([]byte(idData))
	return hex.EncodeToString(sum[:16])
}
