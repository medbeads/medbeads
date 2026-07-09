package engine

import (
	"testing"

	"github.com/medbeads/medbeads/internal/engine/bead"
	"github.com/medbeads/medbeads/internal/engine/index"
)

// collectIDs returns the set of Bead IDs in beads, for order-independent
// membership checks (mirrors v2.2.0's core/store/graph_test.go collectIDs).
func collectIDs(beads []bead.Bead) map[string]bool {
	m := make(map[string]bool, len(beads))
	for _, b := range beads {
		m[b.ID] = true
	}
	return m
}

// --- Ingest round-trip ------------------------------------------------------

func TestIngest_RoundTrip_RegistrationAndChildren(t *testing.T) {
	e := openT(t)

	root, err := e.Ingest(unsavedBead("patient_registration", nil, nil, map[string]any{"name": "Synthea Test Patient"}))
	if err != nil {
		t.Fatalf("Ingest (root): %v", err)
	}
	if root.ID == "" {
		t.Fatal("Ingest (root) did not assign an ID")
	}

	var children []bead.Bead
	for i := 0; i < 5; i++ {
		c, err := e.Ingest(unsavedBead("fhir_observation", []string{root.ID}, []string{"loinc:718-7"}, map[string]any{"note": "obs"}))
		if err != nil {
			t.Fatalf("Ingest (child %d): %v", i, err)
		}
		children = append(children, c)
	}

	// GetBead round-trip: every Bead (root + children) must read back
	// verified and byte-identical in the fields that matter.
	gotRoot, err := e.GetBead(root.ID)
	if err != nil {
		t.Fatalf("GetBead(root): %v", err)
	}
	if gotRoot.ID != root.ID || gotRoot.Type != "patient_registration" {
		t.Errorf("GetBead(root) = %+v, want ID=%s Type=patient_registration", gotRoot, root.ID)
	}

	for _, c := range children {
		got, err := e.GetBead(c.ID)
		if err != nil {
			t.Fatalf("GetBead(%s): %v", c.ID, err)
		}
		if got.ID != c.ID || len(got.Parents) != 1 || got.Parents[0] != root.ID {
			t.Errorf("GetBead(%s) = %+v, want Parents=[%s]", c.ID, got, root.ID)
		}
	}

	// ListPatientBeads must return root + all children, ordered by
	// timestamp (unsavedBead assigns strictly increasing timestamps).
	all, err := e.ListPatientBeads(root.ID)
	if err != nil {
		t.Fatalf("ListPatientBeads: %v", err)
	}
	if len(all) != 1+len(children) {
		t.Fatalf("ListPatientBeads returned %d beads, want %d", len(all), 1+len(children))
	}
	got := collectIDs(all)
	if !got[root.ID] {
		t.Error("ListPatientBeads missing root")
	}
	for _, c := range children {
		if !got[c.ID] {
			t.Errorf("ListPatientBeads missing child %s", c.ID)
		}
	}
	if all[0].ID != root.ID {
		t.Errorf("ListPatientBeads[0].ID = %s, want root %s (earliest timestamp)", all[0].ID, root.ID)
	}
}

// --- patient_root resolution: full branch coverage --------------------------

func TestResolvePatientRoot_RegistrationIsItsOwnRoot(t *testing.T) {
	e := openT(t)

	root, err := e.Ingest(unsavedBead("patient_registration", nil, nil, nil))
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	ref, err := e.idx.GetBead(root.ID)
	if err != nil {
		t.Fatalf("GetBead: %v", err)
	}
	if ref.PatientRoot != root.ID {
		t.Errorf("patient_root = %q, want %q (registration is its own root)", ref.PatientRoot, root.ID)
	}
}

func TestResolvePatientRoot_SingleParentInherits(t *testing.T) {
	e := openT(t)

	root, err := e.Ingest(unsavedBead("patient_registration", nil, nil, nil))
	if err != nil {
		t.Fatalf("Ingest (root): %v", err)
	}
	child, err := e.Ingest(unsavedBead("fhir_observation", []string{root.ID}, nil, nil))
	if err != nil {
		t.Fatalf("Ingest (child): %v", err)
	}
	grandchild, err := e.Ingest(unsavedBead("fhir_observation", []string{child.ID}, nil, nil))
	if err != nil {
		t.Fatalf("Ingest (grandchild): %v", err)
	}

	for _, id := range []string{child.ID, grandchild.ID} {
		ref, err := e.idx.GetBead(id)
		if err != nil {
			t.Fatalf("GetBead(%s): %v", id, err)
		}
		if ref.PatientRoot != root.ID {
			t.Errorf("patient_root for %s = %q, want %q (inherited)", id, ref.PatientRoot, root.ID)
		}
	}
}

func TestResolvePatientRoot_MultipleRootsFallBackToShared(t *testing.T) {
	e := openT(t)

	rootA, err := e.Ingest(unsavedBead("patient_registration", nil, nil, nil))
	if err != nil {
		t.Fatalf("Ingest (rootA): %v", err)
	}
	rootB, err := e.Ingest(unsavedBead("patient_registration", nil, nil, nil))
	if err != nil {
		t.Fatalf("Ingest (rootB): %v", err)
	}

	// A Bead whose parents span two different patients' roots cannot honestly
	// carry either as its patient_root: it must fall back to the shared Pod.
	merge, err := e.Ingest(unsavedBead("fhir_observation", []string{rootA.ID, rootB.ID}, nil, nil))
	if err != nil {
		t.Fatalf("Ingest (merge): %v", err)
	}

	ref, err := e.idx.GetBead(merge.ID)
	if err != nil {
		t.Fatalf("GetBead(merge): %v", err)
	}
	if ref.PatientRoot != "" {
		t.Errorf("patient_root for cross-patient merge = %q, want \"\" (shared)", ref.PatientRoot)
	}

	shared, err := e.idx.ListSharedBeads()
	if err != nil {
		t.Fatalf("ListSharedBeads: %v", err)
	}
	if !collectIDsFromRefs(shared)[merge.ID] {
		t.Errorf("ListSharedBeads missing %s", merge.ID)
	}
}

// collectIDsFromRefs returns the set of Bead IDs in refs, mirroring
// collectIDs but for index.BeadRef (which has no bead.Bead to unwrap).
func collectIDsFromRefs(refs []index.BeadRef) map[string]bool {
	m := make(map[string]bool, len(refs))
	for _, r := range refs {
		m[r.ID] = true
	}
	return m
}

func TestResolvePatientRoot_NoParentsNonRegistrationIsShared(t *testing.T) {
	e := openT(t)

	b, err := e.Ingest(unsavedBead("drug_master", nil, []string{"rxnorm:6919"}, map[string]any{"drug": "meropenem"}))
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	ref, err := e.idx.GetBead(b.ID)
	if err != nil {
		t.Fatalf("GetBead: %v", err)
	}
	if ref.PatientRoot != "" {
		t.Errorf("patient_root = %q, want \"\" (no parents, not a registration)", ref.PatientRoot)
	}
}

// --- DAG acyclicity: unknown parent is rejected -----------------------------

func TestIngest_RejectsUnknownParent(t *testing.T) {
	e := openT(t)

	unknownParent := "0000000000000000000000000000000000000000000000000000000000000000"[:64]
	_, err := e.Ingest(unsavedBead("fhir_observation", []string{unknownParent}, nil, nil))
	if err == nil {
		t.Fatal("Ingest with an unknown parent succeeded, want error")
	}
}

// --- duplicate ingest: idempotent -------------------------------------------

func TestIngest_DuplicateIsIdempotent(t *testing.T) {
	e := openT(t)

	root, err := e.Ingest(unsavedBead("patient_registration", nil, nil, map[string]any{"name": "dup test"}))
	if err != nil {
		t.Fatalf("Ingest (first): %v", err)
	}

	// Re-ingest the exact same (now ID'd) Bead a second time.
	again, err := e.Ingest(root)
	if err != nil {
		t.Fatalf("Ingest (duplicate): %v", err)
	}
	if again.ID != root.ID {
		t.Errorf("duplicate Ingest returned ID %s, want %s", again.ID, root.ID)
	}

	all, err := e.ListPatientBeads(root.ID)
	if err != nil {
		t.Fatalf("ListPatientBeads: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("ListPatientBeads after duplicate ingest = %d beads, want 1 (no duplication)", len(all))
	}
}

// TestIngest_TamperedIDRejected checks that an already-ID'd Bead whose ID
// does not match its recomputed content hash is rejected by Ingest's
// bead.Verify step, never reaching the parent/patient_root/write logic.
func TestIngest_TamperedIDRejected(t *testing.T) {
	e := openT(t)

	b := unsavedBead("patient_registration", nil, nil, nil)
	withID, err := bead.WithID(b)
	if err != nil {
		t.Fatalf("bead.WithID: %v", err)
	}
	withID.ID = withID.ID[:len(withID.ID)-1] + "0" // flip the last hex digit
	if withID.ID == "" {
		t.Fatal("test precondition failed: mutated ID is empty")
	}

	_, err = e.Ingest(withID)
	if err == nil {
		t.Fatal("Ingest with a tampered ID succeeded, want error")
	}
}
