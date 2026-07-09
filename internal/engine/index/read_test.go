package index

import "testing"

// TestListPatients_ReturnsOnlyRegistrations mirrors v2.2.0's
// core/store/graph_test.go TestGetPatients: only type='patient_registration'
// Beads are returned, non-patient children are excluded. ListPatients is the
// "v2 GetPatients の移植先" addition specs/DESIGN_v3.md §6 /
// docs/requirements.md R4.3 calls for (see internal/engine/graph's task
// report for the full v2-test migration table).
func TestListPatients_ReturnsOnlyRegistrations(t *testing.T) {
	db := openT(t)

	p1 := testBead(t, "patient_registration", "Patient One", nil, nil, nil)
	indexBeadT(t, db, p1, BeadLocation{PodPath: "pods/_shared.pod", PatientRoot: p1.ID, Offset: 0, Length: 100})

	p2 := testBead(t, "patient_registration", "Patient Two", nil, nil, nil)
	indexBeadT(t, db, p2, BeadLocation{PodPath: "pods/_shared.pod", PatientRoot: p2.ID, Offset: 100, Length: 100})

	// A non-patient child bead must not be returned by ListPatients.
	child := testBead(t, "fhir_encounter", "visit", []string{p1.ID}, nil, nil)
	indexBeadT(t, db, child, BeadLocation{PodPath: "pods/_shared.pod", PatientRoot: p1.ID, Offset: 200, Length: 100})

	refs, err := db.ListPatients()
	if err != nil {
		t.Fatalf("ListPatients: %v", err)
	}
	if len(refs) != 2 {
		t.Fatalf("ListPatients returned %d beads, want 2", len(refs))
	}
	got := make(map[string]bool, len(refs))
	for _, r := range refs {
		if r.Type != "patient_registration" {
			t.Errorf("ListPatients returned a non-patient bead of type %q", r.Type)
		}
		got[r.ID] = true
	}
	if !got[p1.ID] || !got[p2.ID] {
		t.Errorf("ListPatients missing seeded patients, got %v", got)
	}
}
