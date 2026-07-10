package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/medbeads/medbeads/internal/engine"
	"github.com/medbeads/medbeads/internal/engine/apc"
	"github.com/medbeads/medbeads/internal/engine/bead"
	"github.com/medbeads/medbeads/internal/engine/clearance"
)

// TestGetBead_SHA256PrefixRoundTrip checks the task's headline get_bead
// requirement: a Bead ingested with a plain-hex ID is retrievable via
// get_bead using either the plain-hex or "sha256:"-prefixed form (bead.
// ParseID accepts both), and the response's own ID is always
// "sha256:"-prefixed (bead.FormatID) — the API/display-layer convention
// specs/DESIGN_v3.md §4 mandates and this package's doc.go documents as its
// one conversion point.
func TestGetBead_SHA256PrefixRoundTrip(t *testing.T) {
	e := openT(t)
	p := seedPatient(t, e, "Round Trip Patient")

	s := newServerT(t, e, SystemRole)

	for _, id := range []string{p.ID, bead.FormatID(p.ID)} {
		_, out, err := s.getBead(context.Background(), nil, getBeadIn{ID: id})
		if err != nil {
			t.Fatalf("getBead(%q): %v", id, err)
		}
		if out.Bead.ID != bead.FormatID(p.ID) {
			t.Errorf("getBead(%q).Bead.ID = %q, want %q", id, out.Bead.ID, bead.FormatID(p.ID))
		}
		if out.Bead.Type != "patient_registration" {
			t.Errorf("getBead(%q).Bead.Type = %q, want patient_registration", id, out.Bead.Type)
		}
	}
}

// TestGetBead_UnknownID checks get_bead surfaces a not-found lookup as a
// tool-level error (IsError=true via toolError), not a Go/MCP protocol
// error, per mcp.CallToolResult's own doc comment on why tool failures
// belong in Content.
func TestGetBead_UnknownID(t *testing.T) {
	e := openT(t)
	s := newServerT(t, e, SystemRole)

	unknownID := strings.Repeat("0", bead.HexIDLen)
	res, _, err := s.getBead(context.Background(), nil, getBeadIn{ID: unknownID})
	if err != nil {
		t.Fatalf("getBead: unexpected Go error: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("getBead(unknown id): want IsError=true result, got %+v", res)
	}
}

// TestClearanceFilter_ViewerBlockedSystemBypasses is the task's headline
// clearance requirement: a Bead with a DB clearance_rules row denying
// "viewer" is masked ({"_restricted": true}) for a viewer-role Server but
// returned unmasked for a system-role Server (clearance.HasAccessWithRules'
// own RoleSystem bypass — this package does not special-case it, it just
// passes the role through).
func TestClearanceFilter_ViewerBlockedSystemBypasses(t *testing.T) {
	e := openT(t)
	p := seedPatient(t, e, "Clearance Patient")
	restricted := seedChildBead(t, e, p, "fhir_observation", nil, map[string]any{"test": "HIV panel"})

	if err := clearance.SaveRule(e.Index(), clearance.Rule{
		ID:          "rule-1",
		BeadID:      restricted.ID,
		DeniedRoles: []string{"viewer"},
		CreatedBy:   "test",
		CreatedAt:   "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("SaveRule: %v", err)
	}

	viewer := newServerT(t, e, DefaultRole)
	_, viewerOut, err := viewer.getBead(context.Background(), nil, getBeadIn{ID: restricted.ID})
	if err != nil {
		t.Fatalf("viewer getBead: %v", err)
	}
	if _, restrictedFlag := viewerOut.Bead.Content["_restricted"]; !restrictedFlag {
		t.Errorf("viewer getBead(restricted).Content = %+v, want {\"_restricted\": true}", viewerOut.Bead.Content)
	}

	system := newServerT(t, e, SystemRole)
	_, systemOut, err := system.getBead(context.Background(), nil, getBeadIn{ID: restricted.ID})
	if err != nil {
		t.Fatalf("system getBead: %v", err)
	}
	if _, restrictedFlag := systemOut.Bead.Content["_restricted"]; restrictedFlag {
		t.Errorf("system getBead(restricted).Content = %+v, want unmasked", systemOut.Bead.Content)
	}
	if got := systemOut.Bead.Content["test"]; got != "HIV panel" {
		t.Errorf("system getBead(restricted).Content[test] = %v, want %q", got, "HIV panel")
	}
}

// TestCreateBead_NotRegisteredForViewerRole checks the lead's "書き込みは
// system ロール限定" decision at the strictest level: New must not register
// create_bead in tools/list at all for a non-system role, not merely reject
// it at call time. This is checked via the MCP protocol surface itself
// (ListTools over an in-memory transport), since the whole point is that a
// viewer-role client cannot even discover the tool exists.
func TestCreateBead_NotRegisteredForViewerRole(t *testing.T) {
	e := openT(t)
	s := newServerT(t, e, DefaultRole)
	cs := connectInMemoryT(t, s)

	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	for _, tool := range tools.Tools {
		if tool.Name == "create_bead" {
			t.Fatalf("ListTools for viewer role includes create_bead; want it absent")
		}
	}
}

// TestCreateBead_RegisteredForSystemRole is TestCreateBead_
// NotRegisteredForViewerRole's converse: a system-role Server does register
// create_bead.
func TestCreateBead_RegisteredForSystemRole(t *testing.T) {
	e := openT(t)
	s := newServerT(t, e, SystemRole)
	cs := connectInMemoryT(t, s)

	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	for _, tool := range tools.Tools {
		if tool.Name == "create_bead" {
			return
		}
	}
	t.Fatalf("ListTools for system role does not include create_bead")
}

// TestCreateBead_AntigensAreDeterministicallyDerived checks the v3.1
// projection-time tag derivation requirement (specs/DESIGN_v3.1_draft.md
// §2/§5): create_bead never accepts or computes antigens/tags at all
// (createBeadIn has no antigens field, and the returned/persisted Bead
// itself carries no Antigens field either — see bead.Bead's doc comment);
// tag derivation runs only when index.IndexBead projects the Bead, via
// antigen.Extract(b.Type, b.Content), and its result lives in bead_antigens
// (queried here via e.Index().GetAntigens), not on the Bead.
func TestCreateBead_AntigensAreDeterministicallyDerived(t *testing.T) {
	e := openT(t)
	p := seedPatient(t, e, "Antigen Patient")
	s := newServerT(t, e, SystemRole)

	content := map[string]any{
		"code": map[string]any{
			"coding": []any{
				map[string]any{"system": "http://snomed.info/sct", "code": "44054006"},
			},
		},
	}

	_, out, err := s.createBead(context.Background(), nil, createBeadIn{
		Type:      "fhir_condition",
		Timestamp: "2026-01-01T01:00:00Z",
		Parents:   []string{p.ID},
		Content:   content,
	})
	if err != nil {
		t.Fatalf("createBead: %v", err)
	}

	plainID, err := bead.ParseID(out.Bead.ID)
	if err != nil {
		t.Fatalf("bead.ParseID(%q): %v", out.Bead.ID, err)
	}

	// The Bead itself (正本) carries no antigens — get_bead-equivalent access
	// must never expose a tag field (specs/DESIGN_v3.1_draft.md §5: "get_bead
	// は正本のみ、タグを含まない").
	saved, err := e.GetBead(plainID)
	if err != nil {
		t.Fatalf("e.GetBead(%s): %v", plainID, err)
	}
	if saved.Content == nil {
		t.Fatalf("e.GetBead(%s) returned no Content", plainID)
	}

	// The projection (bead_antigens, populated by IndexBead's
	// antigen.Extract call) must carry the deterministically-derived tag.
	wantAntigen := "snomed:44054006"
	gotAntigens, err := e.Index().GetAntigens(plainID)
	if err != nil {
		t.Fatalf("e.Index().GetAntigens(%s): %v", plainID, err)
	}
	found := false
	for _, ag := range gotAntigens {
		if ag == wantAntigen {
			found = true
		}
	}
	if !found {
		t.Errorf("GetAntigens(%s) = %v, want to contain %q", plainID, gotAntigens, wantAntigen)
	}
}

// --- data-reviewer regression: beadRefView summary leakage --------------
//
// clearance.FilterByAccess masks a restricted Bead's Content in place; it
// does not shrink the slice it returns, and it has no way to know about
// separately-fetched index metadata (beads.summary) a caller might attach
// alongside the (now-masked) Bead. list_patients/search_beads/get_timeline/
// search_antigens all populate beadRefView.Summary from an index-derived
// string fetched *before* filtering — these tests pin the fix: a restricted
// Bead's ref must be dropped entirely (Summary must never leak), not merely
// have its Content masked while its ref (with the pre-filter Summary) is
// still emitted.

const restrictedSummaryMarker = "TOP SECRET DIAGNOSIS MARKER"

// seedRestrictedPatientBead seeds a patient plus one child Bead denying
// "viewer" access, returning the patient and the restricted Bead. The
// restricted Bead's own content contains restrictedSummaryMarker so a test
// can assert that string never appears in a viewer-role response.
func seedRestrictedPatientBead(t *testing.T, e *engine.Engine, patientName string) (patient, restricted bead.Bead) {
	t.Helper()
	patient = seedPatient(t, e, patientName)
	restricted = seedChildBead(t, e, patient, "fhir_observation", nil, map[string]any{
		"note": restrictedSummaryMarker,
	})
	if err := clearance.SaveRule(e.Index(), clearance.Rule{
		ID:          "rule-" + restricted.ID,
		BeadID:      restricted.ID,
		DeniedRoles: []string{"viewer"},
		CreatedBy:   "test",
		CreatedAt:   "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("SaveRule: %v", err)
	}
	return patient, restricted
}

func TestListPatients_DropsRestrictedSummary(t *testing.T) {
	e := openT(t)
	// list_patients only ever returns patient_registration Beads, so make
	// the *patient* itself the restricted Bead (list_patients has nothing
	// else to leak a summary for).
	patient := seedPatient(t, e, restrictedSummaryMarker)
	if err := clearance.SaveRule(e.Index(), clearance.Rule{
		ID:          "rule-" + patient.ID,
		BeadID:      patient.ID,
		DeniedRoles: []string{"viewer"},
		CreatedBy:   "test",
		CreatedAt:   "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("SaveRule: %v", err)
	}

	viewer := newServerT(t, e, DefaultRole)
	_, out, err := viewer.listPatients(context.Background(), nil, listPatientsIn{})
	if err != nil {
		t.Fatalf("listPatients: %v", err)
	}
	for _, ref := range out.Patients {
		if ref.ID == bead.FormatID(patient.ID) {
			t.Fatalf("viewer list_patients included the restricted patient at all (want dropped): %+v", ref)
		}
		if strings.Contains(ref.Summary, restrictedSummaryMarker) {
			t.Fatalf("viewer list_patients leaked restricted marker via Summary: %+v", ref)
		}
	}

	system := newServerT(t, e, SystemRole)
	_, systemOut, err := system.listPatients(context.Background(), nil, listPatientsIn{})
	if err != nil {
		t.Fatalf("system listPatients: %v", err)
	}
	found := false
	for _, ref := range systemOut.Patients {
		if ref.ID == bead.FormatID(patient.ID) {
			found = true
		}
	}
	if !found {
		t.Fatalf("system list_patients did not include the restricted patient; want it present (system bypasses clearance)")
	}
}

func TestSearchBeads_DropsRestrictedSummary(t *testing.T) {
	e := openT(t)
	_, restricted := seedRestrictedPatientBead(t, e, "Search Beads Patient")

	viewer := newServerT(t, e, DefaultRole)
	_, out, err := viewer.searchBeads(context.Background(), nil, searchBeadsIn{Query: "note"})
	if err != nil {
		t.Fatalf("searchBeads: %v", err)
	}
	for _, ref := range out.Results {
		if ref.ID == bead.FormatID(restricted.ID) {
			t.Fatalf("viewer search_beads included the restricted Bead at all (want dropped): %+v", ref)
		}
		if strings.Contains(ref.Summary, restrictedSummaryMarker) {
			t.Fatalf("viewer search_beads leaked restricted marker via Summary: %+v", ref)
		}
	}
}

func TestGetTimeline_DropsRestrictedSummary(t *testing.T) {
	e := openT(t)
	patient, restricted := seedRestrictedPatientBead(t, e, "Timeline Patient")

	viewer := newServerT(t, e, DefaultRole)
	_, out, err := viewer.getTimeline(context.Background(), nil, getTimelineIn{PatientID: patient.ID})
	if err != nil {
		t.Fatalf("getTimeline: %v", err)
	}
	for _, ref := range out.Beads {
		if ref.ID == bead.FormatID(restricted.ID) {
			t.Fatalf("viewer get_timeline included the restricted Bead at all (want dropped): %+v", ref)
		}
		if strings.Contains(ref.Summary, restrictedSummaryMarker) {
			t.Fatalf("viewer get_timeline leaked restricted marker via Summary: %+v", ref)
		}
	}

	system := newServerT(t, e, SystemRole)
	_, systemOut, err := system.getTimeline(context.Background(), nil, getTimelineIn{PatientID: patient.ID})
	if err != nil {
		t.Fatalf("system getTimeline: %v", err)
	}
	found := false
	for _, ref := range systemOut.Beads {
		if ref.ID == bead.FormatID(restricted.ID) {
			found = true
		}
	}
	if !found {
		t.Fatalf("system get_timeline did not include the restricted Bead; want it present (system bypasses clearance)")
	}
}

func TestSearchAntigens_DropsRestrictedSummary(t *testing.T) {
	e := openT(t)
	patient := seedPatient(t, e, "Antigen Search Patient")
	restricted := seedChildBead(t, e, patient, "fhir_observation",
		[]string{"snomed:restricted-marker"},
		map[string]any{"note": restrictedSummaryMarker})
	if err := clearance.SaveRule(e.Index(), clearance.Rule{
		ID:          "rule-" + restricted.ID,
		BeadID:      restricted.ID,
		DeniedRoles: []string{"viewer"},
		CreatedBy:   "test",
		CreatedAt:   "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("SaveRule: %v", err)
	}

	viewer := newServerT(t, e, DefaultRole)
	_, out, err := viewer.searchAntigens(context.Background(), nil, searchAntigensIn{Antigen: "snomed:restricted-marker"})
	if err != nil {
		t.Fatalf("searchAntigens: %v", err)
	}
	for _, ref := range out.Beads {
		if ref.ID == bead.FormatID(restricted.ID) {
			t.Fatalf("viewer search_antigens included the restricted Bead at all (want dropped): %+v", ref)
		}
		if strings.Contains(ref.Summary, restrictedSummaryMarker) {
			t.Fatalf("viewer search_antigens leaked restricted marker via Summary: %+v", ref)
		}
	}

	system := newServerT(t, e, SystemRole)
	_, systemOut, err := system.searchAntigens(context.Background(), nil, searchAntigensIn{Antigen: "snomed:restricted-marker"})
	if err != nil {
		t.Fatalf("system searchAntigens: %v", err)
	}
	found := false
	for _, ref := range systemOut.Beads {
		if ref.ID == bead.FormatID(restricted.ID) {
			found = true
		}
	}
	if !found {
		t.Fatalf("system search_antigens did not include the restricted Bead; want it present (system bypasses clearance)")
	}
}

// --- data-reviewer regression: get_sibling_links clearance ---------------

// TestGetSiblingLinks_DropsRestrictedPair checks that a sibling_pairs row
// naming a restricted other Bead is dropped entirely for a viewer session
// (neither the other Bead's ID nor its matched_antigen leaks), while a
// system session sees the full row.
func TestGetSiblingLinks_DropsRestrictedPair(t *testing.T) {
	e := openT(t)
	root := seedPatient(t, e, "Sibling Links Patient")
	padWithNoiseBeads(t, e, root, 10)

	rx := seedChildBead(t, e, root, "fhir_medicationrequest",
		[]string{"risk:nephrotoxic", "organ:renal"},
		map[string]any{"drug": "meropenem"})
	lab := seedChildBead(t, e, root, "fhir_observation",
		[]string{"risk:nephrotoxic", "organ:renal"},
		map[string]any{"test": "eGFR"})

	if err := clearance.SaveRule(e.Index(), clearance.Rule{
		ID:          "rule-" + lab.ID,
		BeadID:      lab.ID,
		DeniedRoles: []string{"viewer"},
		CreatedBy:   "test",
		CreatedAt:   "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("SaveRule: %v", err)
	}

	scanner := apc.New(e, e.Index(), apc.Default())
	for i := 0; i < 10; i++ {
		res, err := scanner.Scan()
		if err != nil {
			t.Fatalf("Scan: %v", err)
		}
		if res.BeadsScanned == 0 {
			break
		}
	}

	viewer := newServerT(t, e, DefaultRole)
	_, viewerOut, err := viewer.getSiblingLinks(context.Background(), nil, getSiblingLinksIn{ID: rx.ID})
	if err != nil {
		t.Fatalf("viewer getSiblingLinks: %v", err)
	}
	for _, link := range viewerOut.Links {
		if link.OtherBeadID == bead.FormatID(lab.ID) {
			t.Fatalf("viewer get_sibling_links exposed the restricted sibling pair: %+v", link)
		}
	}

	system := newServerT(t, e, SystemRole)
	_, systemOut, err := system.getSiblingLinks(context.Background(), nil, getSiblingLinksIn{ID: rx.ID})
	if err != nil {
		t.Fatalf("system getSiblingLinks: %v", err)
	}
	found := false
	for _, link := range systemOut.Links {
		if link.OtherBeadID == bead.FormatID(lab.ID) {
			found = true
		}
	}
	if !found {
		t.Fatalf("system get_sibling_links did not include the sibling pair; want it present (system bypasses clearance). Links=%+v", systemOut.Links)
	}
}

// --- data-reviewer regression: apc_trigger is a write tool ---------------

// TestApcTrigger_NotRegisteredForViewerRole checks apc_trigger is gated
// alongside create_bead (system role only), since apc.Scanner.Scan durably
// ingests sibling_link Beads — a write, not a read, despite its empty input
// schema.
func TestApcTrigger_NotRegisteredForViewerRole(t *testing.T) {
	e := openT(t)
	s := newServerT(t, e, DefaultRole)
	cs := connectInMemoryT(t, s)

	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	for _, tool := range tools.Tools {
		if tool.Name == "apc_trigger" {
			t.Fatalf("ListTools for viewer role includes apc_trigger; want it absent")
		}
	}
}

// TestApcTrigger_RegisteredForSystemRole is TestApcTrigger_
// NotRegisteredForViewerRole's converse.
func TestApcTrigger_RegisteredForSystemRole(t *testing.T) {
	e := openT(t)
	s := newServerT(t, e, SystemRole)
	cs := connectInMemoryT(t, s)

	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	for _, tool := range tools.Tools {
		if tool.Name == "apc_trigger" {
			return
		}
	}
	t.Fatalf("ListTools for system role does not include apc_trigger")
}
