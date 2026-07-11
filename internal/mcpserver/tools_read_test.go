package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/medbeads/medbeads/internal/engine"
	"github.com/medbeads/medbeads/internal/engine/bead"
	"github.com/medbeads/medbeads/internal/engine/clearance"
	"github.com/medbeads/medbeads/internal/engine/projector"
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
// antigen.Extract(b.Type, b.Content), and its result lives in bead_tags
// (queried here via e.Index().GetTags), not on the Bead.
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

	// The projection (bead_tags, populated by IndexBead's
	// antigen.Extract call) must carry the deterministically-derived tag.
	wantAntigen := "snomed:44054006"
	gotAntigens, err := e.Index().GetTags(plainID)
	if err != nil {
		t.Fatalf("e.Index().GetTags(%s): %v", plainID, err)
	}
	found := false
	for _, ag := range gotAntigens {
		if ag == wantAntigen {
			found = true
		}
	}
	if !found {
		t.Errorf("GetTags(%s) = %v, want to contain %q", plainID, gotAntigens, wantAntigen)
	}
}

// --- data-reviewer regression: beadRefView summary leakage --------------
//
// clearance.FilterByAccess masks a restricted Bead's Content in place; it
// does not shrink the slice it returns, and it has no way to know about
// separately-fetched index metadata (beads.summary) a caller might attach
// alongside the (now-masked) Bead. list_patients/search_beads/get_timeline/
// search_tags all populate beadRefView.Summary from an index-derived
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

func TestSearchTags_DropsRestrictedSummary(t *testing.T) {
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
	_, out, err := viewer.searchTags(context.Background(), nil, searchTagsIn{Tag: "snomed:restricted-marker"})
	if err != nil {
		t.Fatalf("searchTags: %v", err)
	}
	for _, ref := range out.Beads {
		if ref.ID == bead.FormatID(restricted.ID) {
			t.Fatalf("viewer search_tags included the restricted Bead at all (want dropped): %+v", ref)
		}
		if strings.Contains(ref.Summary, restrictedSummaryMarker) {
			t.Fatalf("viewer search_tags leaked restricted marker via Summary: %+v", ref)
		}
	}

	system := newServerT(t, e, SystemRole)
	_, systemOut, err := system.searchTags(context.Background(), nil, searchTagsIn{Tag: "snomed:restricted-marker"})
	if err != nil {
		t.Fatalf("system searchTags: %v", err)
	}
	found := false
	for _, ref := range systemOut.Beads {
		if ref.ID == bead.FormatID(restricted.ID) {
			found = true
		}
	}
	if !found {
		t.Fatalf("system search_tags did not include the restricted Bead; want it present (system bypasses clearance)")
	}
}

// --- U3c: get_links (clinical_links read path) ---------------------------

// seedCooccurrenceLinkT ingests the built-in cooccurrence link_rule Bead and
// runs projector.Reproject over e's already-indexed bead_tags, returning the
// projector.Result. Mirrors internal/engine/projector/reproject_test.go's
// seedCooccurrenceRule + Reproject call sequence — this package's tests need
// real clinical_links rows (not hand-inserted ones) so get_links exercises
// the real GetClinicalLinks -> clearance pipeline against actual U3b output.
func seedCooccurrenceLinkT(t *testing.T, e *engine.Engine) projector.Result {
	t.Helper()
	rule := ingestT(t, e, projector.BuildCooccurrenceRuleBead("2026-01-01T00:00:00Z"))
	res, err := projector.Reproject(e.Index(), engineReaderT{e}, []string{rule.ID}, "test-code-v1", "2026-07-11T00:00:00Z")
	if err != nil {
		t.Fatalf("Reproject: %v", err)
	}
	return res
}

// engineReaderT adapts *engine.Engine to projector's unexported beadReader
// interface (Go structural typing), mirroring internal/engine/projector/
// reproject_test.go's identically-purposed engineReader.
type engineReaderT struct{ e *engine.Engine }

func (r engineReaderT) GetBead(id string) (projector.BeadContent, error) {
	b, err := r.e.GetBead(id)
	if err != nil {
		return projector.BeadContent{}, err
	}
	return projector.BeadContent{Content: b.Content}, nil
}

// TestGetLinks_ReturnsClinicalLinksForBead checks get_links' headline case:
// a risk:/atc: cooccurrence pair projected by projector.Reproject is
// returned by get_links for either endpoint Bead, with relation/severity/
// evidence_basis/matched_tag/rule_version populated from the real
// clinical_links row (not hand-asserted).
func TestGetLinks_ReturnsClinicalLinksForBead(t *testing.T) {
	e := openT(t)
	root := seedPatient(t, e, "Get Links Patient")
	padWithNoiseBeads(t, e, root, 10)

	rx := seedChildBead(t, e, root, "fhir_medicationrequest",
		[]string{"risk:nephrotoxic", "atc:c09aa03"}, map[string]any{"drug": "meropenem"})
	lab := seedChildBead(t, e, root, "fhir_observation",
		[]string{"risk:nephrotoxic"}, map[string]any{"test": "eGFR"})

	seedCooccurrenceLinkT(t, e)

	s := newServerT(t, e, SystemRole)
	_, out, err := s.getLinks(context.Background(), nil, getLinksIn{ID: rx.ID})
	if err != nil {
		t.Fatalf("getLinks: %v", err)
	}
	if len(out.Links) != 1 {
		t.Fatalf("getLinks(rx).Links = %d, want 1: %+v", len(out.Links), out.Links)
	}
	link := out.Links[0]
	if link.OtherBeadID != bead.FormatID(lab.ID) {
		t.Errorf("OtherBeadID = %q, want %q", link.OtherBeadID, bead.FormatID(lab.ID))
	}
	if link.Relation != "clinical_correlation" {
		t.Errorf("Relation = %q, want clinical_correlation", link.Relation)
	}
	if link.Severity != "info" {
		t.Errorf("Severity = %q, want info", link.Severity)
	}
	if link.EvidenceBasis != "cooccurrence" {
		t.Errorf("EvidenceBasis = %q, want cooccurrence", link.EvidenceBasis)
	}
	if link.MatchedTag != "risk:nephrotoxic" {
		t.Errorf("MatchedTag = %q, want risk:nephrotoxic", link.MatchedTag)
	}
	if link.RuleVersion == "" {
		t.Errorf("RuleVersion is empty, want the link_rule Bead ID")
	}

	// Same link queried from the other endpoint (lab) must resolve back to rx.
	_, labOut, err := s.getLinks(context.Background(), nil, getLinksIn{ID: lab.ID})
	if err != nil {
		t.Fatalf("getLinks(lab): %v", err)
	}
	if len(labOut.Links) != 1 || labOut.Links[0].OtherBeadID != bead.FormatID(rx.ID) {
		t.Fatalf("getLinks(lab).Links = %+v, want one link back to rx (%s)", labOut.Links, bead.FormatID(rx.ID))
	}
}

// TestGetLinks_DropsRestrictedLink checks clearance inheritance: a
// clinical_links row whose other endpoint is access-denied for the viewer
// role is dropped entirely (mirrors TestGetSiblingLinks_DropsRestrictedPair's
// identical discipline, applied to the new clinical_links read path instead
// of sibling_pairs).
func TestGetLinks_DropsRestrictedLink(t *testing.T) {
	e := openT(t)
	root := seedPatient(t, e, "Get Links Clearance Patient")
	padWithNoiseBeads(t, e, root, 10)

	rx := seedChildBead(t, e, root, "fhir_medicationrequest",
		[]string{"risk:nephrotoxic", "atc:c09aa03"}, map[string]any{"drug": "meropenem"})
	lab := seedChildBead(t, e, root, "fhir_observation",
		[]string{"risk:nephrotoxic"}, map[string]any{"test": "eGFR"})

	seedCooccurrenceLinkT(t, e)

	if err := clearance.SaveRule(e.Index(), clearance.Rule{
		ID:          "rule-" + lab.ID,
		BeadID:      lab.ID,
		DeniedRoles: []string{"viewer"},
		CreatedBy:   "test",
		CreatedAt:   "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("SaveRule: %v", err)
	}

	viewer := newServerT(t, e, DefaultRole)
	_, viewerOut, err := viewer.getLinks(context.Background(), nil, getLinksIn{ID: rx.ID})
	if err != nil {
		t.Fatalf("viewer getLinks: %v", err)
	}
	for _, link := range viewerOut.Links {
		if link.OtherBeadID == bead.FormatID(lab.ID) {
			t.Fatalf("viewer get_links exposed the restricted link: %+v", link)
		}
	}
	if len(viewerOut.Links) != 0 {
		t.Fatalf("viewer getLinks(rx).Links = %+v, want empty (the only link's other endpoint is restricted)", viewerOut.Links)
	}

	system := newServerT(t, e, SystemRole)
	_, systemOut, err := system.getLinks(context.Background(), nil, getLinksIn{ID: rx.ID})
	if err != nil {
		t.Fatalf("system getLinks: %v", err)
	}
	found := false
	for _, link := range systemOut.Links {
		if link.OtherBeadID == bead.FormatID(lab.ID) {
			found = true
		}
	}
	if !found {
		t.Fatalf("system get_links did not include the link; want it present (system bypasses clearance). Links=%+v", systemOut.Links)
	}
}

// --- U5a regression: apc_trigger/get_siblings/get_sibling_links/apc_status
// tools removed entirely ---------------------------------------------------

// TestU5a_ApcToolsRemoved checks specs/U5_api_retrieve.md's U5a removal is
// complete at the MCP tool-registration surface: none of the old
// apc_trigger/get_siblings/get_sibling_links/apc_status tools are registered
// for either role, for any Server built after package apc's deletion.
func TestU5a_ApcToolsRemoved(t *testing.T) {
	e := openT(t)
	removed := []string{"apc_trigger", "get_siblings", "get_sibling_links", "apc_status"}

	for _, role := range []string{DefaultRole, SystemRole} {
		s := newServerT(t, e, role)
		cs := connectInMemoryT(t, s)

		tools, err := cs.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatalf("ListTools(role=%s): %v", role, err)
		}
		for _, tool := range tools.Tools {
			for _, name := range removed {
				if tool.Name == name {
					t.Errorf("ListTools(role=%s) includes removed tool %q; want absent", role, name)
				}
			}
		}
	}
}
