package index

import (
	"testing"
)

// TestIndexBead_RoundTrip writes one Bead with parents and antigens, then
// reads it back through GetBead / GetEdges / GetAntigens / Search, checking
// every piece IndexBead is responsible for landed correctly (R3.1/R3.2/R3.3).
func TestIndexBead_RoundTrip(t *testing.T) {
	db := openT(t)

	parent := testBead(t, "patient_registration", "root patient", nil, nil, nil)
	indexBeadT(t, db, parent, BeadLocation{
		PodPath: "pods/aa/aabb.pod", PatientRoot: parent.ID, Offset: 0, Length: 100,
	})

	child := testBead(t, "fhir_observation", "hemoglobin 12.3 g/dL",
		[]string{parent.ID}, []string{"loinc:718-7", "organ:renal"}, nil)
	indexBeadT(t, db, child, BeadLocation{
		PodPath: "pods/aa/aabb.pod", PatientRoot: parent.ID, Offset: 100, Length: 80,
	})

	ref, err := db.GetBead(child.ID)
	if err != nil {
		t.Fatalf("GetBead: %v", err)
	}
	if ref.PatientRoot != parent.ID {
		t.Errorf("ref.PatientRoot = %q, want %q", ref.PatientRoot, parent.ID)
	}
	if ref.Type != "fhir_observation" {
		t.Errorf("ref.Type = %q, want fhir_observation", ref.Type)
	}
	if ref.PodPath != "pods/aa/aabb.pod" {
		t.Errorf("ref.PodPath = %q, want pods/aa/aabb.pod", ref.PodPath)
	}
	if ref.Offset != 100 || ref.Length != 80 {
		t.Errorf("ref.Offset/Length = %d/%d, want 100/80", ref.Offset, ref.Length)
	}
	if ref.Summary == "" {
		t.Error("ref.Summary is empty, want a DefaultFlattener-derived summary")
	}

	edges, err := db.GetEdges(child.ID)
	if err != nil {
		t.Fatalf("GetEdges: %v", err)
	}
	if len(edges) != 1 || edges[0] != parent.ID {
		t.Errorf("GetEdges(child) = %v, want [%s]", edges, parent.ID)
	}

	antigens, err := db.GetAntigens(child.ID)
	if err != nil {
		t.Fatalf("GetAntigens: %v", err)
	}
	wantAntigens := []string{"loinc:718-7", "organ:renal"}
	if len(antigens) != len(wantAntigens) {
		t.Fatalf("GetAntigens(child) = %v, want %v", antigens, wantAntigens)
	}
	for i, a := range wantAntigens {
		if antigens[i] != a {
			t.Errorf("GetAntigens(child)[%d] = %q, want %q", i, antigens[i], a)
		}
	}

	results, err := db.Search("hemoglobin", 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	found := false
	for _, r := range results {
		if r.BeadID == child.ID {
			found = true
			if r.PatientRoot != parent.ID {
				t.Errorf("SearchResult.PatientRoot = %q, want %q", r.PatientRoot, parent.ID)
			}
		}
	}
	if !found {
		t.Errorf("Search(hemoglobin) did not return child bead %s: %+v", child.ID, results)
	}

	patientBeads, err := db.ListPatientBeads(parent.ID)
	if err != nil {
		t.Fatalf("ListPatientBeads: %v", err)
	}
	if len(patientBeads) != 2 {
		t.Fatalf("ListPatientBeads(%s) returned %d beads, want 2", parent.ID, len(patientBeads))
	}
	if patientBeads[0].ID != parent.ID || patientBeads[1].ID != child.ID {
		t.Errorf("ListPatientBeads order = [%s, %s], want [%s, %s] (timestamp order)",
			patientBeads[0].ID, patientBeads[1].ID, parent.ID, child.ID)
	}
}

// TestIndexBead_SharedPod checks that a Bead written with PatientRoot=""
// (the shared Pod case) is stored with a NULL patient_root and surfaces via
// ListSharedBeads rather than any patient's ListPatientBeads.
func TestIndexBead_SharedPod(t *testing.T) {
	db := openT(t)

	b := testBead(t, "drug_master", "meropenem", nil, []string{"rxnorm:6919"}, nil)
	indexBeadT(t, db, b, BeadLocation{
		PodPath: "pods/_shared.pod", PatientRoot: "", Offset: 0, Length: 50,
	})

	ref, err := db.GetBead(b.ID)
	if err != nil {
		t.Fatalf("GetBead: %v", err)
	}
	if ref.PatientRoot != "" {
		t.Errorf("ref.PatientRoot = %q, want empty (shared pod)", ref.PatientRoot)
	}

	shared, err := db.ListSharedBeads()
	if err != nil {
		t.Fatalf("ListSharedBeads: %v", err)
	}
	if len(shared) != 1 || shared[0].ID != b.ID {
		t.Errorf("ListSharedBeads = %+v, want [%s]", shared, b.ID)
	}
}

// TestGetBead_NotFound checks the ErrNotFound contract for an unknown ID.
func TestGetBead_NotFound(t *testing.T) {
	db := openT(t)
	_, err := db.GetBead("0000000000000000000000000000000000000000000000000000000000000000"[:64])
	if err == nil {
		t.Fatal("GetBead(unknown) = nil error, want ErrNotFound")
	}
}

// TestIndexBead_DuplicateEdgeAntigen_IsIdempotent checks that indexing the
// same Bead's edges/antigens twice (as CatchUp replaying an overlapping
// watermark range would) does not error and does not duplicate rows — the
// INSERT OR IGNORE contract write.go documents.
func TestIndexBead_DuplicateEdgeAntigen_IsIdempotent(t *testing.T) {
	db := openT(t)

	parent := testBead(t, "patient_registration", "root", nil, nil, nil)
	indexBeadT(t, db, parent, BeadLocation{PodPath: "p.pod", PatientRoot: parent.ID, Offset: 0, Length: 10})

	child := testBead(t, "fhir_observation", "note", []string{parent.ID}, []string{"organ:renal"}, nil)
	loc := BeadLocation{PodPath: "p.pod", PatientRoot: parent.ID, Offset: 10, Length: 10}

	tx1, err := db.sqlDB.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := IndexBead(tx1, child, loc, DefaultFlattener{}); err != nil {
		t.Fatalf("IndexBead (first): %v", err)
	}
	if err := tx1.Commit(); err != nil {
		t.Fatalf("Commit (first): %v", err)
	}

	// Re-inserting edges/antigens for the same bead_id (simulating a replay
	// that skips the beads INSERT via a different path) must not error.
	tx2, err := db.sqlDB.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := tx2.Exec(
		`INSERT OR IGNORE INTO bead_edges (child_id, parent_id, edge_type) VALUES (?, ?, 'parent')`,
		child.ID, parent.ID,
	); err != nil {
		t.Fatalf("duplicate edge insert: %v", err)
	}
	if _, err := tx2.Exec(
		`INSERT OR IGNORE INTO bead_antigens (antigen, bead_id, patient_root) VALUES (?, ?, ?)`,
		"organ:renal", child.ID, parent.ID,
	); err != nil {
		t.Fatalf("duplicate antigen insert: %v", err)
	}
	if err := tx2.Commit(); err != nil {
		t.Fatalf("Commit (second): %v", err)
	}

	edges, err := db.GetEdges(child.ID)
	if err != nil {
		t.Fatalf("GetEdges: %v", err)
	}
	if len(edges) != 1 {
		t.Errorf("GetEdges after duplicate insert = %v, want 1 row (no duplication)", edges)
	}
	antigens, err := db.GetAntigens(child.ID)
	if err != nil {
		t.Fatalf("GetAntigens: %v", err)
	}
	if len(antigens) != 1 {
		t.Errorf("GetAntigens after duplicate insert = %v, want 1 row (no duplication)", antigens)
	}
}
