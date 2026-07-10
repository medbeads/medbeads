package mcpserver

import (
	"context"
	"testing"

	"github.com/medbeads/medbeads/internal/engine/bead"
	"github.com/medbeads/medbeads/internal/engine/clearance"
)

// TestRagSearch_HappyPath_ReturnsL0ContentAndDistance is R6.3's e2e: embed
// query -> pure vector top-k -> L0 content + distance, no chain expansion.
func TestRagSearch_HappyPath_ReturnsL0ContentAndDistance(t *testing.T) {
	e := openT(t)
	root := seedPatient(t, e, "RagSearch Patient")
	target := seedChildBead(t, e, root, "fhir_observation", nil, map[string]any{
		"note": "elevated potassium level observed",
	})
	// A second, unrelated Bead so this test also proves rag_search is doing
	// an actual ranked search, not just returning everything.
	seedChildBead(t, e, root, "fhir_observation", nil, map[string]any{
		"note": "routine annual physical exam, no abnormal findings",
	})

	drainEmbedIndexerT(t, e)

	s := newServerWithEmbedderT(t, e, SystemRole)
	res, out, err := s.ragSearch(context.Background(), nil, ragSearchIn{
		Query: "elevated potassium level observed",
		K:     5,
	})
	if err != nil {
		t.Fatalf("ragSearch: unexpected Go error: %v", err)
	}
	if res != nil && res.IsError {
		t.Fatalf("ragSearch: unexpected tool error: %+v", res)
	}
	if len(out.Results) == 0 {
		t.Fatal("ragSearch: want at least one result, got none")
	}

	top := out.Results[0]
	if top.ID != bead.FormatID(target.ID) {
		t.Errorf("ragSearch top result = %s, want %s (nearest/identical-text match should rank first)", top.ID, bead.FormatID(target.ID))
	}
	if top.Distance > 0.0001 {
		t.Errorf("top result distance = %f, want ~0 (identical embedded text)", top.Distance)
	}
	if top.Content == nil || top.Content["note"] != "elevated potassium level observed" {
		t.Errorf("top result Content = %+v, want full L0 content with the original note", top.Content)
	}
	if top.Type != "fhir_observation" {
		t.Errorf("top result Type = %q, want fhir_observation", top.Type)
	}
}

// TestRagSearch_PatientScoped checks the patient_id filter uses the same
// vec0 PARTITION KEY pre-filter retrieve/SemanticSearch use.
func TestRagSearch_PatientScoped(t *testing.T) {
	e := openT(t)
	rootA := seedPatient(t, e, "RagSearch Patient A")
	rootB := seedPatient(t, e, "RagSearch Patient B")
	beadA := seedChildBead(t, e, rootA, "fhir_observation", nil, map[string]any{"note": "shared text"})
	beadB := seedChildBead(t, e, rootB, "fhir_observation", nil, map[string]any{"note": "shared text"})

	drainEmbedIndexerT(t, e)

	s := newServerWithEmbedderT(t, e, SystemRole)
	_, out, err := s.ragSearch(context.Background(), nil, ragSearchIn{
		Query:     "shared text",
		PatientID: bead.FormatID(rootA.ID),
		K:         10,
	})
	if err != nil {
		t.Fatalf("ragSearch: %v", err)
	}
	for _, r := range out.Results {
		if r.ID == bead.FormatID(beadB.ID) {
			t.Fatalf("ragSearch(patient_id=A) returned patient B's Bead %s — partition scoping leaked", beadB.ID)
		}
	}
	found := false
	for _, r := range out.Results {
		if r.ID == bead.FormatID(beadA.ID) {
			found = true
		}
	}
	if !found {
		t.Fatalf("ragSearch(patient_id=A) did not return patient A's own Bead %s", beadA.ID)
	}
}

// TestRagSearch_RestrictedBead_IsDropped is the clearance leakage-regression
// check the task explicitly calls for: a Bead with a clearance_rules row
// denying "viewer" must be entirely absent from a viewer-role Server's
// rag_search results — not masked-and-included (which would still leak its
// existence, type, timestamp, and vector distance to the query), matching
// this package's uniform mask-then-drop policy (render.go's accessible doc
// comment) — and must still appear for a system-role Server (clearance
// bypass), so this also confirms the drop is clearance-driven, not a bug
// that drops every result.
func TestRagSearch_RestrictedBead_IsDropped(t *testing.T) {
	e := openT(t)
	root := seedPatient(t, e, "RagSearch Clearance Patient")
	restricted := seedChildBead(t, e, root, "fhir_observation", nil, map[string]any{
		"note": "HIV panel result positive",
	})

	if err := clearance.SaveRule(e.Index(), clearance.Rule{
		ID:          "rag-search-rule-1",
		BeadID:      restricted.ID,
		DeniedRoles: []string{"viewer"},
		CreatedBy:   "test",
		CreatedAt:   "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("SaveRule: %v", err)
	}

	drainEmbedIndexerT(t, e)

	viewer := newServerWithEmbedderT(t, e, DefaultRole)
	_, viewerOut, err := viewer.ragSearch(context.Background(), nil, ragSearchIn{
		Query: "HIV panel result positive",
		K:     10,
	})
	if err != nil {
		t.Fatalf("viewer ragSearch: %v", err)
	}
	for _, r := range viewerOut.Results {
		if r.ID == bead.FormatID(restricted.ID) {
			t.Fatalf("viewer ragSearch leaked restricted Bead %s into results: %+v", restricted.ID, r)
		}
	}

	system := newServerWithEmbedderT(t, e, SystemRole)
	_, systemOut, err := system.ragSearch(context.Background(), nil, ragSearchIn{
		Query: "HIV panel result positive",
		K:     10,
	})
	if err != nil {
		t.Fatalf("system ragSearch: %v", err)
	}
	found := false
	for _, r := range systemOut.Results {
		if r.ID == bead.FormatID(restricted.ID) {
			found = true
		}
	}
	if !found {
		t.Fatalf("system (clearance-bypass) ragSearch did not return restricted Bead %s — test setup is broken, not proving the drop is clearance-driven", restricted.ID)
	}
}

// TestRagSearch_WithoutEmbedder_IsToolError checks the "embedder 未設定時は
// 明示エラー" requirement.
func TestRagSearch_WithoutEmbedder_IsToolError(t *testing.T) {
	e := openT(t)
	seedPatient(t, e, "No Embedder RagSearch Patient")

	s := newServerT(t, e, SystemRole) // no embedder configured
	res, _, err := s.ragSearch(context.Background(), nil, ragSearchIn{Query: "anything"})
	if err != nil {
		t.Fatalf("ragSearch: unexpected Go error: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("ragSearch(no embedder): want IsError=true, got %+v", res)
	}
}

// TestRagSearch_EmptyQuery_IsToolError checks the empty-query guard.
func TestRagSearch_EmptyQuery_IsToolError(t *testing.T) {
	e := openT(t)
	seedPatient(t, e, "Empty Query RagSearch Patient")

	s := newServerWithEmbedderT(t, e, SystemRole)
	res, _, err := s.ragSearch(context.Background(), nil, ragSearchIn{Query: ""})
	if err != nil {
		t.Fatalf("ragSearch: unexpected Go error: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("ragSearch(query=\"\"): want IsError=true, got %+v", res)
	}
}

// TestRagSearch_RegisteredAsReadTool checks rag_search is registered
// unconditionally by registerReadTools (a viewer-role Server sees it in
// tools/list too, unlike create_bead/apc_trigger which registerWriteTools
// gates to system role only) — this only inspects the MCP server's own
// registered-tools list, not clearance behavior (covered above).
func TestRagSearch_RegisteredAsReadTool(t *testing.T) {
	e := openT(t)
	s := newServerT(t, e, DefaultRole)

	client := connectInMemoryT(t, s)
	tools, err := client.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	found := false
	for _, tool := range tools.Tools {
		if tool.Name == "rag_search" {
			found = true
		}
	}
	if !found {
		t.Fatalf("rag_search not found in viewer-role tools/list: %+v", tools.Tools)
	}
}
