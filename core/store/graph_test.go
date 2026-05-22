package store

import (
	"testing"

	"github.com/sojin25/medbeads/core/types"
)

// collectIDs returns the set of bead ids in a slice for order-independent checks.
func collectIDs(beads []types.Bead) map[string]bool {
	m := make(map[string]bool, len(beads))
	for _, b := range beads {
		m[b.ID] = true
	}
	return m
}

func TestGetContext_WalksAncestors(t *testing.T) {
	setupTestStore(t)

	// Chain: A (root) <- B <- C <- D.
	a := seedPatient(t, "A")
	b := seedChildBead(t, a, "encounter", map[string]interface{}{"n": "B"})
	c := seedChildBead(t, b, "observation", map[string]interface{}{"n": "C"})
	d := seedChildBead(t, c, "observation", map[string]interface{}{"n": "D"})

	beads, err := GetContext(d, 10)
	if err != nil {
		t.Fatalf("GetContext: %v", err)
	}
	got := collectIDs(beads)
	for _, want := range []string{a, b, c, d} {
		if !got[want] {
			t.Errorf("GetContext(D, 10) missing %q", want)
		}
	}
}

func TestGetContext_RespectsDepth(t *testing.T) {
	setupTestStore(t)

	a := seedPatient(t, "A")
	b := seedChildBead(t, a, "encounter", map[string]interface{}{"n": "B"})
	c := seedChildBead(t, b, "observation", map[string]interface{}{"n": "C"})
	d := seedChildBead(t, c, "observation", map[string]interface{}{"n": "D"})

	// depth=1 from D should reach D and its direct parent C, but not the root A.
	beads, err := GetContext(d, 1)
	if err != nil {
		t.Fatalf("GetContext: %v", err)
	}
	got := collectIDs(beads)
	if !got[d] || !got[c] {
		t.Errorf("GetContext(D, 1) should include D and C, got %v", got)
	}
	if got[a] {
		t.Errorf("GetContext(D, 1) should not reach the root A, got %v", got)
	}
}

func TestGetBeadsByParent_WalksDescendants(t *testing.T) {
	setupTestStore(t)

	a := seedPatient(t, "A")
	b := seedChildBead(t, a, "encounter", map[string]interface{}{"n": "B"})
	c := seedChildBead(t, b, "observation", map[string]interface{}{"n": "C"})

	beads, err := GetBeadsByParent(a, 10)
	if err != nil {
		t.Fatalf("GetBeadsByParent: %v", err)
	}
	got := collectIDs(beads)
	for _, want := range []string{a, b, c} {
		if !got[want] {
			t.Errorf("GetBeadsByParent(A, 10) missing %q", want)
		}
	}
}

func TestGetPatients(t *testing.T) {
	setupTestStore(t)

	p1 := seedPatient(t, "Patient One")
	p2 := seedPatient(t, "Patient Two")
	// A non-patient child bead must not be returned by GetPatients.
	seedChildBead(t, p1, "encounter", map[string]interface{}{"n": "visit"})

	patients, err := GetPatients()
	if err != nil {
		t.Fatalf("GetPatients: %v", err)
	}
	if len(patients) != 2 {
		t.Fatalf("GetPatients returned %d beads, want 2", len(patients))
	}
	got := collectIDs(patients)
	for _, p := range patients {
		if p.Type != "patient_registration" {
			t.Errorf("GetPatients returned a non-patient bead of type %q", p.Type)
		}
	}
	if !got[p1] || !got[p2] {
		t.Errorf("GetPatients missing seeded patients, got %v", got)
	}
}
