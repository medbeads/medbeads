package rest

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/medbeads/medbeads/internal/engine"
)

// seedClinicalLinkRowT inserts one clinical_links row directly (the
// projector's own write path is not this test's subject), mirroring
// internal/engine/index's own migrations_0006_test.go direct-INSERT
// convention. beadA/beadB must already satisfy bead_a < bead_b (the table's
// CHECK constraint).
func seedClinicalLinkRowT(t *testing.T, e *engine.Engine, linkID, beadA, beadB, patientRoot, relation, matchedTag, severity string) {
	t.Helper()
	if _, err := e.Index().SQLDB().Exec(`
		INSERT INTO clinical_links
			(link_id, bead_a, bead_b, patient_root, relation, matched_tag, severity,
			 evidence_basis, evidence_bead_ids, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'cooccurrence', '[]', '2026-01-01T00:00:00Z')`,
		linkID, beadA, beadB, patientRoot, relation, matchedTag, severity,
	); err != nil {
		t.Fatalf("seedClinicalLinkRowT(%s): %v", linkID, err)
	}
}

// seedBeadStatusRowT inserts one bead_status row directly.
func seedBeadStatusRowT(t *testing.T, e *engine.Engine, beadID, status, currentBeadID string) {
	t.Helper()
	var current any
	if currentBeadID != "" {
		current = currentBeadID
	}
	if _, err := e.Index().SQLDB().Exec(
		`INSERT INTO bead_status (bead_id, status, current_bead_id) VALUES (?, ?, ?)`,
		beadID, status, current,
	); err != nil {
		t.Fatalf("seedBeadStatusRowT(%s): %v", beadID, err)
	}
}

// orderedPair returns (a, b) sorted lexically ascending, matching
// clinical_links' own bead_a < bead_b CHECK constraint convention.
func orderedPair(x, y string) (a, b string) {
	if x < y {
		return x, y
	}
	return y, x
}

// --- GET /patients/{root}/graph -------------------------------------------

// TestHandleGraph_FullShape builds a small synthetic patient (registration
// -> two child observations, a parent edge each, and one clinical_links row
// between the two children) and asserts the full contract JSON shape:
// patient_root, beads[] (with recorded_at/status/current_bead_id/amends/
// retracts), edges[] (parent DAG only), links[] (patient-scoped
// clinical_links).
func TestHandleGraph_FullShape(t *testing.T) {
	e := openT(t)
	s := newServerT(t, e)

	patient := seedPatient(t, e, "Graph Patient")
	obsA := seedChildBead(t, e, patient, "fhir_observation", map[string]any{"code": "BP"})
	obsB := seedChildBead(t, e, patient, "fhir_observation", map[string]any{"code": "HR"})

	beadA, beadB := orderedPair(obsA.ID, obsB.ID)
	seedClinicalLinkRowT(t, e, "link1", beadA, beadB, patient.ID, "cooccurrence", "loinc:1234", "info")

	r := httptest.NewRequest(http.MethodGet, "/patients/"+patient.ID+"/graph", nil)
	r.SetPathValue("root", patient.ID)
	w := httptest.NewRecorder()
	s.handleGraph(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}

	var got graphResponse
	decodeJSON(t, w, &got)

	if got.PatientRoot != patient.ID {
		t.Errorf("PatientRoot = %q, want %q", got.PatientRoot, patient.ID)
	}
	if len(got.Beads) != 3 {
		t.Fatalf("Beads = %v, want 3 (patient + 2 observations)", got.Beads)
	}

	byID := map[string]graphBeadView{}
	for _, b := range got.Beads {
		byID[b.ID] = b
	}
	if _, ok := byID[patient.ID]; !ok {
		t.Errorf("Beads missing patient %s: %v", patient.ID, got.Beads)
	}
	if _, ok := byID[obsA.ID]; !ok {
		t.Errorf("Beads missing obsA %s: %v", obsA.ID, got.Beads)
	}
	// absent bead_status row = active fallback: Status/CurrentBeadID empty,
	// not an error and not defaulted to some other literal.
	if got := byID[obsA.ID]; got.Status != "" || got.CurrentBeadID != "" {
		t.Errorf("obsA status/current_bead_id = %q/%q, want empty (absent = active)", got.Status, got.CurrentBeadID)
	}

	// 2 parent edges: obsA->patient, obsB->patient.
	if len(got.Edges) != 2 {
		t.Fatalf("Edges = %v, want 2", got.Edges)
	}
	edgeSeen := map[string]bool{}
	for _, edge := range got.Edges {
		edgeSeen[edge.ChildID+"->"+edge.ParentID] = true
	}
	if !edgeSeen[obsA.ID+"->"+patient.ID] || !edgeSeen[obsB.ID+"->"+patient.ID] {
		t.Errorf("Edges = %v, want obsA->patient and obsB->patient", got.Edges)
	}

	// 1 clinical_links row, both endpoints present.
	if len(got.Links) != 1 {
		t.Fatalf("Links = %v, want 1", got.Links)
	}
	link := got.Links[0]
	if link.LinkID != "link1" || link.BeadA != beadA || link.BeadB != beadB {
		t.Errorf("Links[0] = %+v, want {LinkID: link1, BeadA: %s, BeadB: %s}", link, beadA, beadB)
	}
	if link.Relation != "cooccurrence" || link.MatchedTag != "loinc:1234" || link.Severity != "info" {
		t.Errorf("Links[0] relation/tag/severity = %s/%s/%s, want cooccurrence/loinc:1234/info", link.Relation, link.MatchedTag, link.Severity)
	}
}

// TestHandleGraph_AmendsRetractsFromBeadFields confirms beads[].amends/
// beads[].retracts are populated from the Bead's own content.Amends/
// Retracts fields (not from bead_edges), per specs/R7_graph_view.md: "amends/
// retracts は edge ではなく beads[].amends/retracts フィールドで表現".
func TestHandleGraph_AmendsRetractsFromBeadFields(t *testing.T) {
	e := openT(t)
	s := newServerT(t, e)

	patient := seedPatient(t, e, "Patient")
	original := seedChildBead(t, e, patient, "fhir_observation", map[string]any{"value": "120/80"})

	// unsavedBead/seedChildBead don't expose Amends, so build the correcting
	// Bead directly via a raw bead.Bead literal (real, hash-verified
	// content) with Amends set.
	amendBead := unsavedBead("fhir_observation", []string{patient.ID}, map[string]any{"value": "125/82"})
	amendBead.Amends = []string{original.ID}
	amended := ingestT(t, e, amendBead)

	r := httptest.NewRequest(http.MethodGet, "/patients/"+patient.ID+"/graph", nil)
	r.SetPathValue("root", patient.ID)
	w := httptest.NewRecorder()
	s.handleGraph(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	var got graphResponse
	decodeJSON(t, w, &got)

	byID := map[string]graphBeadView{}
	for _, b := range got.Beads {
		byID[b.ID] = b
	}
	if got := byID[amended.ID].Amends; len(got) != 1 || got[0] != original.ID {
		t.Errorf("amended bead's Amends = %v, want [%q]", got, original.ID)
	}
	if got := byID[original.ID].Amends; len(got) != 0 {
		t.Errorf("original bead's Amends = %v, want empty (but non-nil, i.e. JSON [])", got)
	}
}

// TestHandleGraph_MultiTargetAmendsKeepsAllIDs pins the array contract
// (specs/R7_graph_view.md, corrected 2026-07-12): a Bead that amends TWO
// prior Beads must surface both IDs in beads[].amends, not just the first —
// the exact information loss a single-string reduction (this unit's earlier,
// now-corrected implementation) would have caused. Discrimination: a
// regression back to a single-element reduction would fail this test by
// returning len(Amends)==1.
func TestHandleGraph_MultiTargetAmendsKeepsAllIDs(t *testing.T) {
	e := openT(t)
	s := newServerT(t, e)

	patient := seedPatient(t, e, "Patient")
	target1 := seedChildBead(t, e, patient, "fhir_observation", map[string]any{"value": "120/80"})
	target2 := seedChildBead(t, e, patient, "fhir_observation", map[string]any{"value": "72bpm"})

	amendBead := unsavedBead("fhir_observation", []string{patient.ID}, map[string]any{"value": "combined correction"})
	amendBead.Amends = []string{target1.ID, target2.ID}
	amended := ingestT(t, e, amendBead)

	r := httptest.NewRequest(http.MethodGet, "/patients/"+patient.ID+"/graph", nil)
	r.SetPathValue("root", patient.ID)
	w := httptest.NewRecorder()
	s.handleGraph(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	var got graphResponse
	decodeJSON(t, w, &got)

	byID := map[string]graphBeadView{}
	for _, b := range got.Beads {
		byID[b.ID] = b
	}
	gotAmends := byID[amended.ID].Amends
	if len(gotAmends) != 2 {
		t.Fatalf("Amends = %v, want 2 entries (both amend targets kept)", gotAmends)
	}
	want := map[string]bool{target1.ID: true, target2.ID: true}
	for _, id := range gotAmends {
		if !want[id] {
			t.Errorf("Amends contains unexpected id %q", id)
		}
		delete(want, id)
	}
	if len(want) != 0 {
		t.Errorf("Amends missing expected ids: %v", want)
	}
}

// TestHandleGraph_ClearanceDropsBeadAndDanglingEdgeLink is this unit's
// central clearance-masking assertion: unlike handlePatients/handleContext
// (which mask-and-keep), handleGraph must DROP a restricted Bead entirely,
// and drop any edge/link naming it as an endpoint — no dangling references.
// Discrimination: without the drop-not-mask fix, this test would see the
// restricted Bead present (possibly masked) in Beads, or an Edge/Link
// pointing at an ID absent from Beads.
func TestHandleGraph_ClearanceDropsBeadAndDanglingEdgeLink(t *testing.T) {
	e := openT(t)
	s := newServerT(t, e)

	patient := seedPatient(t, e, "Patient")
	open := seedChildBead(t, e, patient, "fhir_observation", map[string]any{"code": "BP"})
	restricted := seedChildBead(t, e, patient, "fhir_observation", map[string]any{"code": "HIV-panel"})
	saveRuleT(t, e, "r1", restricted.ID, []string{"insurance"})

	beadA, beadB := orderedPair(open.ID, restricted.ID)
	seedClinicalLinkRowT(t, e, "link1", beadA, beadB, patient.ID, "cooccurrence", "tag1", "info")

	r := httptest.NewRequest(http.MethodGet, "/patients/"+patient.ID+"/graph", nil)
	r.SetPathValue("root", patient.ID)
	r.Header.Set("X-Viewer-Roles", "insurance")
	w := httptest.NewRecorder()
	s.handleGraph(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	var got graphResponse
	decodeJSON(t, w, &got)

	ids := map[string]bool{}
	for _, b := range got.Beads {
		ids[b.ID] = true
	}
	if ids[restricted.ID] {
		t.Errorf("restricted bead %s must be DROPPED (not masked) from Beads, got %v", restricted.ID, got.Beads)
	}
	if !ids[open.ID] || !ids[patient.ID] {
		t.Errorf("open beads must remain: got %v", got.Beads)
	}

	for _, edge := range got.Edges {
		if edge.ChildID == restricted.ID || edge.ParentID == restricted.ID {
			t.Errorf("edge %+v dangles to dropped bead %s", edge, restricted.ID)
		}
	}
	for _, link := range got.Links {
		if link.BeadA == restricted.ID || link.BeadB == restricted.ID {
			t.Errorf("link %+v dangles to dropped bead %s", link, restricted.ID)
		}
	}
	// The restricted-endpoint link must be gone entirely (its other endpoint
	// being open is not enough to keep it — get_links' own inheritance rule).
	if len(got.Links) != 0 {
		t.Errorf("Links = %v, want 0 (the only link had a restricted endpoint)", got.Links)
	}
}

// TestHandleGraph_StatusNormalization_DropsRetractedLinkEndpoint confirms a
// clinical_links row whose endpoint is retracted (bead_status.status =
// 'retracted') is dropped entirely — mirroring get_links' U5b status
// normalization, applied here to both endpoints (graphResolveLinkEndpoint).
// Discrimination: without this check, the link would incorrectly survive
// with a stale reference to a retracted Bead.
func TestHandleGraph_StatusNormalization_DropsRetractedLinkEndpoint(t *testing.T) {
	e := openT(t)
	s := newServerT(t, e)

	patient := seedPatient(t, e, "Patient")
	openBead := seedChildBead(t, e, patient, "fhir_observation", map[string]any{"code": "BP"})
	retractedBead := seedChildBead(t, e, patient, "fhir_observation", map[string]any{"code": "stale"})
	seedBeadStatusRowT(t, e, retractedBead.ID, "retracted", "")

	beadA, beadB := orderedPair(openBead.ID, retractedBead.ID)
	seedClinicalLinkRowT(t, e, "link1", beadA, beadB, patient.ID, "cooccurrence", "tag1", "info")

	r := httptest.NewRequest(http.MethodGet, "/patients/"+patient.ID+"/graph", nil)
	r.SetPathValue("root", patient.ID)
	w := httptest.NewRecorder()
	s.handleGraph(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	var got graphResponse
	decodeJSON(t, w, &got)

	if len(got.Links) != 0 {
		t.Errorf("Links = %v, want 0 (retracted endpoint must drop the link)", got.Links)
	}
	// The retracted Bead itself is still a real, indexed Bead — beads[] is
	// not status-filtered (only links[] endpoints are, per contract), so it
	// must still appear in Beads with status="retracted".
	byID := map[string]graphBeadView{}
	for _, b := range got.Beads {
		byID[b.ID] = b
	}
	if byID[retractedBead.ID].Status != "retracted" {
		t.Errorf("retracted bead's Status = %q, want retracted", byID[retractedBead.ID].Status)
	}
}

// TestHandleGraph_StatusNormalization_SubstitutesAmendedLinkEndpoint
// confirms an amended link endpoint is substituted to its current_bead_id
// (not dropped, and not left pointing at the stale amended ID) — the other
// half of graphResolveLinkEndpoint's decision table from the retracted case
// above.
func TestHandleGraph_StatusNormalization_SubstitutesAmendedLinkEndpoint(t *testing.T) {
	e := openT(t)
	s := newServerT(t, e)

	patient := seedPatient(t, e, "Patient")
	openBead := seedChildBead(t, e, patient, "fhir_observation", map[string]any{"code": "BP"})
	amendedBead := seedChildBead(t, e, patient, "fhir_observation", map[string]any{"code": "old"})
	currentBead := seedChildBead(t, e, patient, "fhir_observation", map[string]any{"code": "new"})
	seedBeadStatusRowT(t, e, amendedBead.ID, "amended", currentBead.ID)

	beadA, beadB := orderedPair(openBead.ID, amendedBead.ID)
	seedClinicalLinkRowT(t, e, "link1", beadA, beadB, patient.ID, "cooccurrence", "tag1", "info")

	r := httptest.NewRequest(http.MethodGet, "/patients/"+patient.ID+"/graph", nil)
	r.SetPathValue("root", patient.ID)
	w := httptest.NewRecorder()
	s.handleGraph(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	var got graphResponse
	decodeJSON(t, w, &got)

	if len(got.Links) != 1 {
		t.Fatalf("Links = %v, want 1 (amended endpoint substituted, not dropped)", got.Links)
	}
	link := got.Links[0]
	if link.BeadA == amendedBead.ID || link.BeadB == amendedBead.ID {
		t.Errorf("Links[0] = %+v, still references the stale amended ID %s, want substitution to %s", link, amendedBead.ID, currentBead.ID)
	}
	if link.BeadA != currentBead.ID && link.BeadB != currentBead.ID {
		t.Errorf("Links[0] = %+v, want one endpoint substituted to current_bead_id %s", link, currentBead.ID)
	}
}

// TestHandleGraph_EmptyPatientIsEmptyGraphNot404 confirms a patient_root
// with zero indexed Beads returns an empty (200) graph rather than a 404 —
// mirroring handleContext's own "unresolvable start walks to empty" choice.
func TestHandleGraph_EmptyPatientIsEmptyGraphNot404(t *testing.T) {
	e := openT(t)
	s := newServerT(t, e)

	r := httptest.NewRequest(http.MethodGet, "/patients/nonexistent-root/graph", nil)
	r.SetPathValue("root", "nonexistent-root")
	w := httptest.NewRecorder()
	s.handleGraph(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var got graphResponse
	decodeJSON(t, w, &got)
	if len(got.Beads) != 0 || len(got.Edges) != 0 || len(got.Links) != 0 {
		t.Errorf("got = %+v, want all-empty arrays", got)
	}
}

func TestHandleGraph_MissingRootIsBadRequest(t *testing.T) {
	e := openT(t)
	s := newServerT(t, e)

	r := httptest.NewRequest(http.MethodGet, "/patients//graph", nil)
	w := httptest.NewRecorder()
	s.handleGraph(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleGraph_MethodNotAllowed(t *testing.T) {
	e := openT(t)
	s := newServerT(t, e)
	patient := seedPatient(t, e, "Patient")

	r := httptest.NewRequest(http.MethodPost, "/patients/"+patient.ID+"/graph", nil)
	r.SetPathValue("root", patient.ID)
	w := httptest.NewRecorder()
	s.handleGraph(w, r)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

// TestHandleGraph_RegisteredOnMux confirms the route is actually wired into
// Mux() under the Go 1.22+ {root} path-param pattern, not just callable
// directly as s.handleGraph in tests.
func TestHandleGraph_RegisteredOnMux(t *testing.T) {
	e := openT(t)
	s := newServerT(t, e)
	patient := seedPatient(t, e, "Patient")

	mux := s.Mux()
	r := httptest.NewRequest(http.MethodGet, "/patients/"+patient.ID+"/graph", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	var got graphResponse
	decodeJSON(t, w, &got)
	if got.PatientRoot != patient.ID {
		t.Errorf("PatientRoot = %q, want %q", got.PatientRoot, patient.ID)
	}
}
