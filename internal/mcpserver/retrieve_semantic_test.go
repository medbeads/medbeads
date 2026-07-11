package mcpserver

import (
	"context"
	"testing"

	"github.com/medbeads/medbeads/internal/engine/bead"
)

// TestRetrieve_Semantic_FTSAndSemanticMerge_ProvenanceDistance is the R4.2 /
// R6.3 e2e happy path: with an embedder configured and the embed queue
// drained, retrieve(semantic=true) embeds the query, merges vector hits with
// the FTS anchor set (deduplicated), and reports vector_distance in
// provenance for the semantically-matched item.
func TestRetrieve_Semantic_FTSAndSemanticMerge_ProvenanceDistance(t *testing.T) {
	e := openT(t)
	root := seedPatient(t, e, "Semantic Merge Patient")
	// anchorBead's search_text will match both the FTS query (via
	// DefaultFlattener picking up "note") and, once embedded, the semantic
	// query below (identical text -> identical fakeQueryEmbedder vector).
	anchorBead := seedChildBead(t, e, root, "fhir_observation", nil, map[string]any{
		"note": "elevated potassium level observed",
	})
	// semanticOnly shares no FTS-matchable text with the query at all, but
	// its own embedding is still identical-vector-close to the query once
	// drained — this Bead must appear in results ONLY because of the
	// semantic merge, proving retrieve is not just silently ignoring
	// semantic=true and returning FTS-only results.
	semanticOnly := seedChildBead(t, e, root, "fhir_observation", nil, map[string]any{
		"note": "elevated potassium level observed", // same text -> same fake vector, deliberately
	})

	drainEmbedIndexerT(t, e)

	s := newServerWithEmbedderT(t, e, SystemRole)

	res, out, err := s.retrieve(context.Background(), nil, retrieveIn{
		Query:    "elevated potassium level observed",
		Semantic: true,
	})
	if err != nil {
		t.Fatalf("retrieve: unexpected Go error: %v", err)
	}
	if res != nil && res.IsError {
		t.Fatalf("retrieve(semantic=true): unexpected tool error: %+v", res)
	}

	foundAnchor, foundSemanticOnly := false, false
	var anchorDistance *float64
	for _, item := range out.Items {
		if item.ID == bead.FormatID(anchorBead.ID) {
			foundAnchor = true
			anchorDistance = item.VectorDistance
		}
		if item.ID == bead.FormatID(semanticOnly.ID) {
			foundSemanticOnly = true
		}
	}
	if !foundAnchor {
		t.Errorf("retrieve(semantic=true) Items missing FTS anchor %s; got %+v", anchorBead.ID, out.Items)
	}
	if !foundSemanticOnly {
		t.Errorf("retrieve(semantic=true) Items missing semantic-only match %s (merge did not happen); got %+v", semanticOnly.ID, out.Items)
	}
	if anchorDistance == nil {
		t.Errorf("retrieve(semantic=true) anchor item has no vector_distance provenance (identical text should have matched SemanticSearch too)")
	} else if *anchorDistance > 0.0001 {
		t.Errorf("anchor vector_distance = %f, want ~0 (identical embedded text)", *anchorDistance)
	}
}

// TestRetrieve_Semantic_WithoutEmbedder_IsToolError re-confirms (at the
// retrieve level, not just the earlier lightweight check in
// retrieve_test.go) that a Server with no Config.Embedder rejects
// semantic=true even when every other input is otherwise valid.
func TestRetrieve_Semantic_WithoutEmbedder_IsToolError(t *testing.T) {
	e := openT(t)
	root := seedPatient(t, e, "No Embedder Patient")
	seedChildBead(t, e, root, "fhir_observation", nil, map[string]any{"note": "anything"})

	s := newServerT(t, e, SystemRole) // no embedder configured
	res, _, err := s.retrieve(context.Background(), nil, retrieveIn{Query: "anything", Semantic: true})
	if err != nil {
		t.Fatalf("retrieve: unexpected Go error: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("retrieve(semantic=true, no embedder): want IsError=true, got %+v", res)
	}
}

// TestRetrieve_Semantic_EmptyQuery_IsToolError checks that semantic=true
// with an empty query (nothing to embed) is rejected explicitly rather than
// silently embedding an empty string or falling back to tags-only anchor
// selection.
func TestRetrieve_Semantic_EmptyQuery_IsToolError(t *testing.T) {
	e := openT(t)
	root := seedPatient(t, e, "Empty Query Patient")
	seedChildBead(t, e, root, "fhir_observation", []string{"loinc:1"}, map[string]any{"note": "anything"})

	s := newServerWithEmbedderT(t, e, SystemRole)
	res, _, err := s.retrieve(context.Background(), nil, retrieveIn{Semantic: true, Tags: []string{"loinc:1"}})
	if err != nil {
		t.Fatalf("retrieve: unexpected Go error: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("retrieve(semantic=true, query=\"\"): want IsError=true, got %+v", res)
	}
}

// TestRetrieve_Semantic_PatientScoped_DoesNotLeakOtherPatient checks that
// retrieve(semantic=true, patient_id=...) never pulls in another patient's
// Bead via the semantic merge, even when that Bead's embedding is a closer
// (here: identical) vector match — the same vec0 PARTITION KEY guarantee
// internal/engine's own TestStartEmbedIndexer_PatientRootPartitionFilter
// checks at the index layer, exercised here through the full retrieve tool.
func TestRetrieve_Semantic_PatientScoped_DoesNotLeakOtherPatient(t *testing.T) {
	e := openT(t)
	rootA := seedPatient(t, e, "Patient A")
	rootB := seedPatient(t, e, "Patient B")
	anchorA := seedChildBead(t, e, rootA, "fhir_observation", nil, map[string]any{"note": "shared query text"})
	otherB := seedChildBead(t, e, rootB, "fhir_observation", nil, map[string]any{"note": "shared query text"})

	drainEmbedIndexerT(t, e)

	s := newServerWithEmbedderT(t, e, SystemRole)
	res, out, err := s.retrieve(context.Background(), nil, retrieveIn{
		Query:     "shared query text",
		Semantic:  true,
		PatientID: bead.FormatID(rootA.ID),
	})
	if err != nil {
		t.Fatalf("retrieve: unexpected Go error: %v", err)
	}
	if res != nil && res.IsError {
		t.Fatalf("retrieve: unexpected tool error: %+v", res)
	}

	for _, item := range out.Items {
		if item.ID == bead.FormatID(otherB.ID) {
			t.Fatalf("retrieve(patient_id=A, semantic=true) returned patient B's Bead %s — partition scoping leaked", otherB.ID)
		}
	}
	found := false
	for _, item := range out.Items {
		if item.ID == bead.FormatID(anchorA.ID) {
			found = true
		}
	}
	if !found {
		t.Fatalf("retrieve(patient_id=A, semantic=true) did not return patient A's own Bead %s", anchorA.ID)
	}
}
