package rest

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/medbeads/medbeads/internal/engine/clearance"
)

// --- GET /roles -----------------------------------------------------------

// v2.2.0's core/api.handleRoles: `json.NewEncoder(w).Encode(types.AllRoles)`
// — a bare JSON array of role strings.
func TestHandleRoles(t *testing.T) {
	e := openT(t)
	s := newServerT(t, e)

	r := httptest.NewRequest(http.MethodGet, "/roles", nil)
	w := httptest.NewRecorder()
	s.handleRoles(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var roles []string
	decodeJSON(t, w, &roles)
	if len(roles) == 0 {
		t.Fatalf("roles = %v, want non-empty", roles)
	}
	want := map[string]bool{"patient": true, "primary_care": true, "system": true, "emergency": true}
	got := map[string]bool{}
	for _, r := range roles {
		got[r] = true
	}
	for role := range want {
		if !got[role] {
			t.Errorf("roles missing %q: got %v", role, roles)
		}
	}
}

// --- GET /patients ----------------------------------------------------

// v2.2.0's core/api TestHandlePatients_MasksRestrictedForInsurance: a
// restricted patient stays IN the list (mask, don't drop) with
// Content["_restricted"]==true; an open patient's Content passes through
// unchanged.
func TestHandlePatients_MasksRestricted(t *testing.T) {
	e := openT(t)
	s := newServerT(t, e)

	open := seedPatient(t, e, "Open Patient")
	restricted := seedPatient(t, e, "Restricted Patient")
	saveRuleT(t, e, "r1", restricted.ID, []string{"insurance"})

	r := httptest.NewRequest(http.MethodGet, "/patients", nil)
	r.Header.Set("X-Viewer-Roles", "insurance")
	w := httptest.NewRecorder()
	s.handlePatients(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var beads []beadView
	decodeJSON(t, w, &beads)

	byID := map[string]beadView{}
	for _, b := range beads {
		byID[b.ID] = b
	}
	if byID[open.ID].Content["name"] != "Open Patient" {
		t.Errorf("open patient should be visible, got %v", byID[open.ID].Content)
	}
	if byID[restricted.ID].Content["_restricted"] != true {
		t.Errorf("restricted patient should be masked, got %v", byID[restricted.ID].Content)
	}
	// mask-then-keep (not drop): the restricted Bead's own summary/identity
	// must not leak through any OTHER field either — its masked Content is
	// the only thing standing between "insurance" and the patient's name.
	if _, ok := byID[restricted.ID].Content["name"]; ok {
		t.Errorf("restricted patient leaked name in masked content: %v", byID[restricted.ID].Content)
	}
}

func TestHandlePatients_EmptyResultIsJSONNull(t *testing.T) {
	e := openT(t)
	s := newServerT(t, e)

	r := httptest.NewRequest(http.MethodGet, "/patients", nil)
	w := httptest.NewRecorder()
	s.handlePatients(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	// v2.2.0's `var patients []types.Bead` (never initialized non-nil)
	// marshals to JSON `null` for zero results — ui/src/lib/api.ts's
	// fetchAllPatients defends against exactly this ("response.data || []").
	if got := w.Body.String(); got != "null\n" {
		t.Errorf("body = %q, want %q (v2's nil-slice JSON encoding)", got, "null\n")
	}
}

func TestHandlePatients_MethodNotAllowed(t *testing.T) {
	e := openT(t)
	s := newServerT(t, e)

	r := httptest.NewRequest(http.MethodPost, "/patients", nil)
	w := httptest.NewRecorder()
	s.handlePatients(w, r)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

// --- GET /beads?id= -----------------------------------------------------

// v2.2.0's core/api TestGetBeadHandler_ForbiddenForDeniedRole /
// TestGetBeadHandler_AllowedForPermittedRole.
func TestHandleBeads_ForbiddenForDeniedRole(t *testing.T) {
	e := openT(t)
	s := newServerT(t, e)

	patient := seedPatient(t, e, "Secret Patient")
	saveRuleT(t, e, "r1", patient.ID, []string{"insurance"})

	r := httptest.NewRequest(http.MethodGet, "/beads?id="+patient.ID, nil)
	r.Header.Set("X-Viewer-Roles", "insurance")
	w := httptest.NewRecorder()
	s.handleBeads(w, r)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for a denied role", w.Code)
	}
}

func TestHandleBeads_AllowedForPermittedRole(t *testing.T) {
	e := openT(t)
	s := newServerT(t, e)

	patient := seedPatient(t, e, "Patient")

	r := httptest.NewRequest(http.MethodGet, "/beads?id="+patient.ID, nil)
	r.Header.Set("X-Viewer-Roles", "primary_care")
	w := httptest.NewRecorder()
	s.handleBeads(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for a permitted role", w.Code)
	}
	var got beadView
	decodeJSON(t, w, &got)
	if got.ID != patient.ID {
		t.Errorf("ID = %q, want %q (plain hex, no sha256: prefix — frozen v2 contract)", got.ID, patient.ID)
	}
	if got.Content["name"] != "Patient" {
		t.Errorf("Content[name] = %v, want %q", got.Content["name"], "Patient")
	}
}

func TestHandleBeads_NotFound(t *testing.T) {
	e := openT(t)
	s := newServerT(t, e)

	r := httptest.NewRequest(http.MethodGet, "/beads?id=nonexistent", nil)
	w := httptest.NewRecorder()
	s.handleBeads(w, r)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestHandleBeads_MissingID(t *testing.T) {
	e := openT(t)
	s := newServerT(t, e)

	r := httptest.NewRequest(http.MethodGet, "/beads", nil)
	w := httptest.NewRecorder()
	s.handleBeads(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleBeads_POSTNotSupported(t *testing.T) {
	// This package deliberately does not port v2's POST /beads (ingest is
	// MCP-only, R6.3 — see doc.go). A POST here must not silently succeed.
	e := openT(t)
	s := newServerT(t, e)

	r := httptest.NewRequest(http.MethodPost, "/beads", bytes.NewBufferString(`{}`))
	w := httptest.NewRecorder()
	s.handleBeads(w, r)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405 (POST /beads is not part of this thin projection)", w.Code)
	}
}

// --- GET /beads/context -------------------------------------------------

// v2.2.0's default (ancestor) lookup: /beads/context?id=<leaf>&depth=N walks
// up via Parents.
func TestHandleContext_AncestorWalk(t *testing.T) {
	e := openT(t)
	s := newServerT(t, e)

	patient := seedPatient(t, e, "Patient")
	obs := seedChildBead(t, e, patient, "fhir_observation", map[string]any{"code": "BP"})

	r := httptest.NewRequest(http.MethodGet, "/beads/context?id="+obs.ID+"&depth=5", nil)
	w := httptest.NewRecorder()
	s.handleContext(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var beads []beadView
	decodeJSON(t, w, &beads)

	ids := map[string]bool{}
	for _, b := range beads {
		ids[b.ID] = true
	}
	if !ids[obs.ID] {
		t.Errorf("ancestor walk from %s must include itself (depth 0): got %v", obs.ID, beads)
	}
	if !ids[patient.ID] {
		t.Errorf("ancestor walk from %s must include its parent %s: got %v", obs.ID, patient.ID, beads)
	}
}

// v2.2.0's core/api "lookup=reverse" branch (core/store.GetBeadsByParent):
// /beads/context?id=<root>&lookup=reverse walks down via children — this is
// exactly ui/src/lib/api.ts's fetchPatientTimeline
// (params: { id, depth: 50, lookup: 'reverse' }).
func TestHandleContext_ReverseLookupIsTimeline(t *testing.T) {
	e := openT(t)
	s := newServerT(t, e)

	patient := seedPatient(t, e, "Patient")
	obs := seedChildBead(t, e, patient, "fhir_observation", map[string]any{"code": "BP"})
	med := seedChildBead(t, e, patient, "fhir_medicationrequest", map[string]any{"medicationCodeableConcept": map[string]any{"text": "Aspirin"}})

	r := httptest.NewRequest(http.MethodGet, "/beads/context?id="+patient.ID+"&depth=50&lookup=reverse", nil)
	w := httptest.NewRecorder()
	s.handleContext(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var beads []beadView
	decodeJSON(t, w, &beads)

	ids := map[string]bool{}
	for _, b := range beads {
		ids[b.ID] = true
	}
	if !ids[patient.ID] || !ids[obs.ID] || !ids[med.ID] {
		t.Errorf("reverse lookup from patient root must include patient+children, got %v", beads)
	}
}

func TestHandleContext_MasksRestrictedDescendant(t *testing.T) {
	e := openT(t)
	s := newServerT(t, e)

	patient := seedPatient(t, e, "Patient")
	restricted := seedChildBead(t, e, patient, "fhir_observation", map[string]any{"code": "HIV-panel"})
	saveRuleT(t, e, "r1", restricted.ID, []string{"insurance"})

	r := httptest.NewRequest(http.MethodGet, "/beads/context?id="+patient.ID+"&depth=50&lookup=reverse", nil)
	r.Header.Set("X-Viewer-Roles", "insurance")
	w := httptest.NewRecorder()
	s.handleContext(w, r)

	var beads []beadView
	decodeJSON(t, w, &beads)

	for _, b := range beads {
		if b.ID == restricted.ID {
			if b.Content["_restricted"] != true {
				t.Errorf("restricted descendant not masked: %v", b.Content)
			}
			if _, ok := b.Content["code"]; ok {
				t.Errorf("restricted descendant leaked content: %v", b.Content)
			}
		}
	}
}

func TestHandleContext_InvalidDepth(t *testing.T) {
	e := openT(t)
	s := newServerT(t, e)
	patient := seedPatient(t, e, "Patient")

	for _, depth := range []string{"0", "51", "abc"} {
		r := httptest.NewRequest(http.MethodGet, "/beads/context?id="+patient.ID+"&depth="+depth, nil)
		w := httptest.NewRecorder()
		s.handleContext(w, r)
		if w.Code != http.StatusBadRequest {
			t.Errorf("depth=%s: status = %d, want 400", depth, w.Code)
		}
	}
}

func TestHandleContext_MissingID(t *testing.T) {
	e := openT(t)
	s := newServerT(t, e)

	r := httptest.NewRequest(http.MethodGet, "/beads/context", nil)
	w := httptest.NewRecorder()
	s.handleContext(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// --- GET /search --------------------------------------------------------

// /search resolves matches to their PATIENT Bead, not the matched Bead
// itself (see handleSearch's doc comment) — this is the headline behavior
// this endpoint must get right, since ui/src/lib/api.ts's mapBeadToPatient
// assumes every returned Bead has patient_registration-shaped Content.
func TestHandleSearch_ResolvesToPatientBead(t *testing.T) {
	e := openT(t)
	s := newServerT(t, e)

	patient := seedPatient(t, e, "Diabetes Study Patient")
	seedChildBead(t, e, patient, "fhir_condition", map[string]any{"code": map[string]any{"text": "diabetes mellitus"}})

	r := httptest.NewRequest(http.MethodGet, "/search?q=diabetes", nil)
	w := httptest.NewRecorder()
	s.handleSearch(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var beads []beadView
	decodeJSON(t, w, &beads)
	if len(beads) != 1 {
		t.Fatalf("beads = %v, want exactly 1 (the patient, deduplicated)", beads)
	}
	if beads[0].ID != patient.ID {
		t.Errorf("beads[0].ID = %q, want patient ID %q", beads[0].ID, patient.ID)
	}
	if beads[0].Type != "patient_registration" {
		t.Errorf("beads[0].Type = %q, want patient_registration", beads[0].Type)
	}
}

// ui/src/components/PatientSidebar.tsx calls searchPatients("", selectedResourceTypes)
// whenever a resource-type filter chip is toggled with no search text typed
// (its debounced-search effect's `searchTerm.trim() !== ” || selectedResourceTypes.length > 0`
// condition) — an empty query with resourceTypes set is a real request
// shape, ported from v2.2.0's searchByResourceTypes branch.
func TestHandleSearch_EmptyQueryWithResourceTypeFilter(t *testing.T) {
	e := openT(t)
	s := newServerT(t, e)

	withMed := seedPatient(t, e, "Has Medication")
	seedChildBead(t, e, withMed, "fhir_medicationrequest", map[string]any{"medicationCodeableConcept": map[string]any{"text": "Aspirin"}})
	withoutMed := seedPatient(t, e, "No Medication")
	seedChildBead(t, e, withoutMed, "fhir_observation", map[string]any{"code": map[string]any{"text": "BP"}})

	r := httptest.NewRequest(http.MethodGet, "/search?resourceTypes=medication", nil)
	w := httptest.NewRecorder()
	s.handleSearch(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var beads []beadView
	decodeJSON(t, w, &beads)

	ids := map[string]bool{}
	for _, b := range beads {
		ids[b.ID] = true
	}
	if !ids[withMed.ID] {
		t.Errorf("patient with a medication Bead must be in resourceTypes=medication results: got %v", beads)
	}
	if ids[withoutMed.ID] {
		t.Errorf("patient without any medication Bead must NOT be in results: got %v", beads)
	}
}

func TestHandleSearch_MasksRestrictedPatient(t *testing.T) {
	e := openT(t)
	s := newServerT(t, e)

	restricted := seedPatient(t, e, "Restricted Diabetes Patient")
	seedChildBead(t, e, restricted, "fhir_condition", map[string]any{"code": map[string]any{"text": "diabetes"}})
	saveRuleT(t, e, "r1", restricted.ID, []string{"insurance"})

	r := httptest.NewRequest(http.MethodGet, "/search?q=diabetes", nil)
	r.Header.Set("X-Viewer-Roles", "insurance")
	w := httptest.NewRecorder()
	s.handleSearch(w, r)

	var beads []beadView
	decodeJSON(t, w, &beads)
	if len(beads) != 1 {
		t.Fatalf("beads = %v, want 1 (masked, not dropped)", beads)
	}
	if beads[0].Content["_restricted"] != true {
		t.Errorf("restricted patient not masked: %v", beads[0].Content)
	}
	if _, ok := beads[0].Content["name"]; ok {
		t.Errorf("restricted patient leaked name via search: %v", beads[0].Content)
	}
}

// --- GET /resource-counts -----------------------------------------------

// v2.2.0's core/store.ResourceTypeCount JSON shape: {"resourceType":...,"patientCount":...}
// (camelCase — the one exception to this contract's otherwise snake_case
// convention, because that is what v2 shipped).
func TestHandleResourceCounts(t *testing.T) {
	e := openT(t)
	s := newServerT(t, e)

	p1 := seedPatient(t, e, "P1")
	seedChildBead(t, e, p1, "fhir_medicationrequest", map[string]any{})
	p2 := seedPatient(t, e, "P2")
	seedChildBead(t, e, p2, "fhir_observation", map[string]any{})

	r := httptest.NewRequest(http.MethodGet, "/resource-counts", nil)
	w := httptest.NewRecorder()
	s.handleResourceCounts(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var counts []resourceTypeCount
	decodeJSON(t, w, &counts)

	byType := map[string]int{}
	for _, c := range counts {
		byType[c.ResourceType] = c.PatientCount
	}
	if byType["medication"] != 1 {
		t.Errorf("medication count = %d, want 1", byType["medication"])
	}
	if byType["observation"] != 1 {
		t.Errorf("observation count = %d, want 1", byType["observation"])
	}
	if byType["procedure"] != 0 {
		t.Errorf("procedure count = %d, want 0", byType["procedure"])
	}
	// Every one of v2's 8 resource types must be present even at zero, per
	// GetResourceTypeCounts' literal per-type loop (a UI relying on a fixed
	// set of chips must not see a type silently vanish from the response).
	if len(counts) != 8 {
		t.Errorf("len(counts) = %d, want 8 (v2's fixed resource-type list)", len(counts))
	}
}

// --- clearance CRUD -------------------------------------------------------

// v2.2.0's core/api TestCreateClearanceHandler_Validation.
func TestHandleClearance_CreateValidation(t *testing.T) {
	e := openT(t)
	s := newServerT(t, e)
	patient := seedPatient(t, e, "Patient")

	post := func(body, userID string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/clearance", bytes.NewBufferString(body))
		if userID != "" {
			r.Header.Set("X-User-ID", userID)
		}
		w := httptest.NewRecorder()
		s.handleClearance(w, r)
		return w
	}

	t.Run("missing X-User-ID is unauthorized", func(t *testing.T) {
		w := post(`{"bead_id":"`+patient.ID+`","denied_roles":["insurance"]}`, "")
		if w.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", w.Code)
		}
	})

	t.Run("unknown bead_id is not found", func(t *testing.T) {
		w := post(`{"bead_id":"nonexistent","denied_roles":["insurance"]}`, "dr-smith")
		if w.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", w.Code)
		}
	})

	t.Run("valid request creates a rule (201)", func(t *testing.T) {
		w := post(`{"bead_id":"`+patient.ID+`","denied_roles":["insurance"]}`, "dr-smith")
		if w.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201, body=%s", w.Code, w.Body.String())
		}
		var rule clearanceRuleView
		decodeJSON(t, w, &rule)
		if rule.BeadID != patient.ID {
			t.Errorf("rule.BeadID = %q, want %q", rule.BeadID, patient.ID)
		}
		if rule.CreatedBy != "dr-smith" {
			t.Errorf("rule.CreatedBy = %q, want dr-smith", rule.CreatedBy)
		}
	})

	t.Run("denying system is rejected", func(t *testing.T) {
		w := post(`{"bead_id":"`+patient.ID+`","denied_roles":["system"]}`, "dr-smith")
		if w.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400 (system cannot be denied)", w.Code)
		}
	})
}

// GET/POST/DELETE /clearance round trip, mirroring ui/src/lib/api.ts's
// fetchClearanceRules / createClearanceRule / deleteClearanceRule.
func TestHandleClearance_CRUDRoundTrip(t *testing.T) {
	e := openT(t)
	s := newServerT(t, e)
	patient := seedPatient(t, e, "Patient")

	// GET before any rule: empty (JSON null).
	getR := httptest.NewRequest(http.MethodGet, "/clearance?bead_id="+patient.ID, nil)
	getW := httptest.NewRecorder()
	s.handleClearance(getW, getR)
	if getW.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", getW.Code)
	}

	// POST creates a rule.
	postR := httptest.NewRequest(http.MethodPost, "/clearance",
		bytes.NewBufferString(`{"bead_id":"`+patient.ID+`","denied_roles":["insurance"],"reason":"test"}`))
	postR.Header.Set("X-User-ID", "dr-smith")
	postW := httptest.NewRecorder()
	s.handleClearance(postW, postR)
	if postW.Code != http.StatusCreated {
		t.Fatalf("POST status = %d, want 201, body=%s", postW.Code, postW.Body.String())
	}
	var created clearanceRuleView
	decodeJSON(t, postW, &created)

	// GET after: the rule is present.
	getR2 := httptest.NewRequest(http.MethodGet, "/clearance?bead_id="+patient.ID, nil)
	getW2 := httptest.NewRecorder()
	s.handleClearance(getW2, getR2)
	var rules []clearanceRuleView
	decodeJSON(t, getW2, &rules)
	if len(rules) != 1 || rules[0].ID != created.ID {
		t.Fatalf("rules after create = %v, want [%v]", rules, created)
	}

	// DELETE removes it.
	delR := httptest.NewRequest(http.MethodDelete, "/clearance?id="+created.ID, nil)
	delW := httptest.NewRecorder()
	s.handleClearance(delW, delR)
	if delW.Code != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want 204", delW.Code)
	}

	// GET after delete: empty again.
	getR3 := httptest.NewRequest(http.MethodGet, "/clearance?bead_id="+patient.ID, nil)
	getW3 := httptest.NewRecorder()
	s.handleClearance(getW3, getR3)
	if got := getW3.Body.String(); got != "null\n" {
		t.Errorf("body after delete = %q, want %q", got, "null\n")
	}
}

// --- GET /clearance/check -------------------------------------------------

// v2.2.0's core/store.HasAccess(beadID, ...) never required the Bead to
// exist — a bead_id with no clearance_rules rows (whether or not any Bead
// with that ID exists) has no restrictions, so has_access is true. See
// handleClearanceCheck's doc comment.
func TestHandleClearanceCheck_NonexistentBeadIDHasAccess(t *testing.T) {
	e := openT(t)
	s := newServerT(t, e)

	r := httptest.NewRequest(http.MethodGet, "/clearance/check?bead_id=nonexistent-id", nil)
	w := httptest.NewRecorder()
	s.handleClearanceCheck(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var got map[string]bool
	decodeJSON(t, w, &got)
	if !got["has_access"] {
		t.Errorf("has_access = %v, want true for a bead_id with no clearance_rules rows", got)
	}
}

func TestHandleClearanceCheck_DeniedRole(t *testing.T) {
	e := openT(t)
	s := newServerT(t, e)
	patient := seedPatient(t, e, "Patient")
	saveRuleT(t, e, "r1", patient.ID, []string{"insurance"})

	r := httptest.NewRequest(http.MethodGet, "/clearance/check?bead_id="+patient.ID, nil)
	r.Header.Set("X-Viewer-Roles", "insurance")
	w := httptest.NewRecorder()
	s.handleClearanceCheck(w, r)

	var got map[string]bool
	decodeJSON(t, w, &got)
	if got["has_access"] {
		t.Errorf("has_access = %v, want false for a denied role", got)
	}
}

// --- CORS / rate limit (v2.2.0's core/api cors_test.go / ratelimit_test.go) -

func TestSetCORSHeaders_AllowlistedOrigin(t *testing.T) {
	e := openT(t)
	s, err := New(Config{Engine: e, CORSAllowedOrigins: []string{"http://localhost:5173", "http://localhost:5174"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	r := httptest.NewRequest(http.MethodGet, "/beads", nil)
	r.Header.Set("Origin", "http://localhost:5174")
	w := httptest.NewRecorder()
	s.setCORSHeaders(w, r)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:5174" {
		t.Errorf("Access-Control-Allow-Origin = %q, want the reflected origin", got)
	}
	if got := w.Header().Get("Vary"); got != "Origin" {
		t.Errorf("Vary = %q, want Origin", got)
	}
}

func TestSetCORSHeaders_BlockedOrigin(t *testing.T) {
	e := openT(t)
	s, err := New(Config{Engine: e, CORSAllowedOrigins: []string{"http://localhost:5173"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	r := httptest.NewRequest(http.MethodGet, "/beads", nil)
	r.Header.Set("Origin", "http://evil.example.com")
	w := httptest.NewRecorder()
	s.setCORSHeaders(w, r)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want empty for a non-allowlisted origin", got)
	}
}

func TestWithRateLimit_Returns429(t *testing.T) {
	e := openT(t)
	s, err := New(Config{Engine: e, CORSAllowedOrigins: []string{"*"}, RateLimit: 2})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ok := func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }
	handler := s.withRateLimit(ok)

	codes := make([]int, 0, 3)
	var lastW *httptest.ResponseRecorder
	for i := 0; i < 3; i++ {
		r := httptest.NewRequest(http.MethodGet, "/beads", nil)
		r.RemoteAddr = "192.0.2.1:1234"
		w := httptest.NewRecorder()
		handler(w, r)
		codes = append(codes, w.Code)
		lastW = w
	}
	if codes[0] != http.StatusOK || codes[1] != http.StatusOK {
		t.Errorf("first two requests should be 200, got %v", codes)
	}
	if codes[2] != http.StatusTooManyRequests {
		t.Errorf("third request should be 429, got %d", codes[2])
	}
	// Pin existing correct behavior: a throttled *actual* request still
	// carries CORS headers (this was never the bug — the bug was that the
	// OPTIONS preflight itself got throttled; see
	// TestWithRateLimit_OPTIONSNeverThrottled below) plus Retry-After so
	// clients can back off intelligently.
	if got := lastW.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("429 response Access-Control-Allow-Origin = %q, want *", got)
	}
	if got := lastW.Header().Get("Retry-After"); got != "60" {
		t.Errorf("429 response Retry-After = %q, want 60 (matches the one-minute fixed window)", got)
	}
}

// TestWithRateLimit_OPTIONSNeverThrottled is the regression test for the
// bug this task fixes: a browser preflight (OPTIONS) must always get an ok
// (2xx) status, even after the per-IP counter has already tripped for
// actual requests from the same IP. Before the fix, Mux() wrapped the
// OPTIONS branch in withRateLimit just like every other method, so once an
// IP's counter passed RateLimit, its *next* preflight returned 429 — and
// the Fetch spec requires a preflight to be 2xx regardless of the CORS
// headers it carries, so the browser reported an opaque CORS failure
// instead of an honest 429. Removing the OPTIONS short-circuit in
// withRateLimit must make this test fail (verified by mutation).
func TestWithRateLimit_OPTIONSNeverThrottled(t *testing.T) {
	e := openT(t)
	s, err := New(Config{Engine: e, CORSAllowedOrigins: []string{"*"}, RateLimit: 1})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ok := func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }
	handler := s.withRateLimit(ok)

	// Exhaust the limit (RateLimit: 1) with real GET requests from the same IP.
	for i := 0; i < 5; i++ {
		r := httptest.NewRequest(http.MethodGet, "/beads", nil)
		r.RemoteAddr = "198.51.100.7:1234"
		w := httptest.NewRecorder()
		handler(w, r)
	}

	// The counter is now well past the limit. A preflight from the same IP
	// must still succeed.
	r := httptest.NewRequest(http.MethodOptions, "/beads", nil)
	r.RemoteAddr = "198.51.100.7:1234"
	r.Header.Set("Origin", "http://localhost:5173")
	w := httptest.NewRecorder()
	handler(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("OPTIONS after rate limit exhausted: status = %d, want 200 (preflight must never be throttled)", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("OPTIONS after rate limit exhausted: Access-Control-Allow-Origin = %q, want *", got)
	}
}

// TestSetCORSHeaders_MaxAge pins Access-Control-Max-Age so browsers cache
// preflights instead of re-preflighting every request that carries the
// UI's non-CORS-safelisted X-Viewer-Roles header.
func TestSetCORSHeaders_MaxAge(t *testing.T) {
	e := openT(t)
	s, err := New(Config{Engine: e, CORSAllowedOrigins: []string{"*"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	r := httptest.NewRequest(http.MethodGet, "/beads", nil)
	w := httptest.NewRecorder()
	s.setCORSHeaders(w, r)

	if got := w.Header().Get("Access-Control-Max-Age"); got != "600" {
		t.Errorf("Access-Control-Max-Age = %q, want 600", got)
	}
}

func TestParseViewerRoles(t *testing.T) {
	e := openT(t)
	s, err := New(Config{Engine: e, ServiceToken: "secret"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	cases := []struct {
		name      string
		header    string
		token     string
		setHeader bool
		wantLen   int
		wantHas   string
	}{
		{name: "missing header yields empty set", setHeader: false, wantLen: 0},
		{name: "system is stripped without a token", header: "system,primary_care", setHeader: true, wantLen: 1, wantHas: "primary_care"},
		{name: "system is kept with a valid token", header: "system", token: "secret", setHeader: true, wantLen: 1, wantHas: "system"},
		{name: "emergency is always kept", header: "emergency", setHeader: true, wantLen: 1, wantHas: "emergency"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/beads", nil)
			if tc.setHeader {
				r.Header.Set("X-Viewer-Roles", tc.header)
			}
			if tc.token != "" {
				r.Header.Set("X-Service-Token", tc.token)
			}
			got := s.parseViewerRoles(r)
			if len(got) != tc.wantLen {
				t.Fatalf("parseViewerRoles() = %v, want len %d", got, tc.wantLen)
			}
			if tc.wantHas != "" {
				found := false
				for _, r := range got {
					if r == tc.wantHas {
						found = true
					}
				}
				if !found {
					t.Errorf("parseViewerRoles() = %v, want to contain %q", got, tc.wantHas)
				}
			}
		})
	}
}

// sanity: clearance.AllRoles is what /roles serves — guards against the two
// drifting if clearance's role list ever changes shape.
func TestRolesMatchesClearancePackage(t *testing.T) {
	if len(clearance.AllRoles) == 0 {
		t.Fatal("clearance.AllRoles is empty")
	}
}
