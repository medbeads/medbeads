package rest

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestIntegration_UIRoundTrip exercises the exact UI navigation flow this
// unit's task calls for: ingest a small patient into a real Engine, then
// walk it through this package's HTTP handlers the way
// ui/src/components/PatientSidebar.tsx (fetchAllPatients) ->
// ui/src/App.tsx (fetchPatientTimeline) -> a detail click (implicit —
// the timeline item IS the Bead detail, per TimelineItem.data) actually
// does. No mux/http.Server is spun up: handlers are called directly
// (matching this package's other httptest-based tests and mcpserver's own
// in-process convention), since Server.Mux()'s registration itself is
// exercised by TestMux_RegistersAllPaths below.
func TestIntegration_UIRoundTrip(t *testing.T) {
	e := openT(t)
	s := newServerT(t, e)

	patient := seedPatient(t, e, "Round Trip Patient")
	encounter := seedChildBead(t, e, patient, "fhir_encounter", map[string]any{
		"period": map[string]any{"start": "2026-01-02T00:00:00Z"},
		"type":   []any{map[string]any{"text": "Annual Checkup"}},
	})
	medication := seedChildBead(t, e, encounter, "fhir_medicationrequest", map[string]any{
		"authoredOn":                "2026-01-02T00:10:00Z",
		"medicationCodeableConcept": map[string]any{"text": "Metformin"},
	})

	// 1. Patient search sidebar: GET /patients.
	patientsR := httptest.NewRequest(http.MethodGet, "/patients", nil)
	patientsW := httptest.NewRecorder()
	s.handlePatients(patientsW, patientsR)
	if patientsW.Code != http.StatusOK {
		t.Fatalf("GET /patients: status = %d, body=%s", patientsW.Code, patientsW.Body.String())
	}
	var patients []beadView
	decodeJSON(t, patientsW, &patients)
	found := false
	for _, p := range patients {
		if p.ID == patient.ID {
			found = true
			if p.Content["name"] != "Round Trip Patient" {
				t.Errorf("patient content = %v, want name=Round Trip Patient", p.Content)
			}
		}
	}
	if !found {
		t.Fatalf("ingested patient %s not present in GET /patients: %v", patient.ID, patients)
	}

	// 2. Timeline: GET /beads/context?id=<patient>&depth=50&lookup=reverse
	// (ui/src/lib/api.ts's fetchPatientTimeline's exact params).
	timelineR := httptest.NewRequest(http.MethodGet,
		"/beads/context?id="+patient.ID+"&depth=50&lookup=reverse", nil)
	timelineW := httptest.NewRecorder()
	s.handleContext(timelineW, timelineR)
	if timelineW.Code != http.StatusOK {
		t.Fatalf("GET /beads/context: status = %d, body=%s", timelineW.Code, timelineW.Body.String())
	}
	var timeline []beadView
	decodeJSON(t, timelineW, &timeline)

	timelineIDs := map[string]beadView{}
	for _, b := range timeline {
		timelineIDs[b.ID] = b
	}
	for _, wantID := range []string{patient.ID, encounter.ID, medication.ID} {
		if _, ok := timelineIDs[wantID]; !ok {
			t.Errorf("timeline missing Bead %s: got %v", wantID, timeline)
		}
	}

	// 3. Bead detail panel: GET /beads?id=<medication> — the exact Bead a
	// UI detail-panel click on a timeline item resolves to (TimelineItem.data.id).
	detailR := httptest.NewRequest(http.MethodGet, "/beads?id="+medication.ID, nil)
	detailW := httptest.NewRecorder()
	s.handleBeads(detailW, detailR)
	if detailW.Code != http.StatusOK {
		t.Fatalf("GET /beads?id=%s: status = %d, body=%s", medication.ID, detailW.Code, detailW.Body.String())
	}
	var detail beadView
	decodeJSON(t, detailW, &detail)
	if detail.ID != medication.ID {
		t.Errorf("detail.ID = %q, want %q", detail.ID, medication.ID)
	}
	if got := detail.Content["medicationCodeableConcept"].(map[string]any)["text"]; got != "Metformin" {
		t.Errorf("detail content medication text = %v, want Metformin", got)
	}
}

// TestMux_RegistersAllPaths checks Server.Mux() actually wires every one of
// v2.2.0's frozen paths (docs/requirements.md R6.1's "REST は... 現行契約を
// 凍結"), by dispatching an OPTIONS request (every handler in this package
// answers OPTIONS with 200 pre-flight, see setCORSHeaders callers) through
// the real mux rather than calling handler methods directly — this is what
// actually proves the routing table cmd/medbeadsd's serve subcommand mounts
// is complete, which handlers_test.go's direct-call tests do not.
func TestMux_RegistersAllPaths(t *testing.T) {
	e := openT(t)
	s := newServerT(t, e)
	mux := s.Mux()

	paths := []string{
		"/beads", "/beads/context", "/patients", "/search",
		"/resource-counts", "/clearance", "/clearance/check", "/roles",
	}
	for _, p := range paths {
		r := httptest.NewRequest(http.MethodOptions, p, nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Errorf("OPTIONS %s: status = %d, want 200 (mux does not route this v2-frozen path)", p, w.Code)
		}
	}
}
