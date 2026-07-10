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

	root, err := e.Ingest(unsavedBead("patient_registration", nil, map[string]any{"name": "Synthea Test Patient"}))
	if err != nil {
		t.Fatalf("Ingest (root): %v", err)
	}
	if root.ID == "" {
		t.Fatal("Ingest (root) did not assign an ID")
	}

	var children []bead.Bead
	for i := 0; i < 5; i++ {
		c, err := e.Ingest(unsavedBead("fhir_observation", []string{root.ID}, map[string]any{"note": "obs"}))
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

	root, err := e.Ingest(unsavedBead("patient_registration", nil, nil))
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

	root, err := e.Ingest(unsavedBead("patient_registration", nil, nil))
	if err != nil {
		t.Fatalf("Ingest (root): %v", err)
	}
	child, err := e.Ingest(unsavedBead("fhir_observation", []string{root.ID}, nil))
	if err != nil {
		t.Fatalf("Ingest (child): %v", err)
	}
	grandchild, err := e.Ingest(unsavedBead("fhir_observation", []string{child.ID}, nil))
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

	rootA, err := e.Ingest(unsavedBead("patient_registration", nil, nil))
	if err != nil {
		t.Fatalf("Ingest (rootA): %v", err)
	}
	rootB, err := e.Ingest(unsavedBead("patient_registration", nil, nil))
	if err != nil {
		t.Fatalf("Ingest (rootB): %v", err)
	}

	// A Bead whose parents span two different patients' roots cannot honestly
	// carry either as its patient_root: it must fall back to the shared Pod.
	merge, err := e.Ingest(unsavedBead("fhir_observation", []string{rootA.ID, rootB.ID}, nil))
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

	b, err := e.Ingest(unsavedBead("drug_master", nil, map[string]any{"drug": "meropenem"}))
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
	_, err := e.Ingest(unsavedBead("fhir_observation", []string{unknownParent}, nil))
	if err == nil {
		t.Fatal("Ingest with an unknown parent succeeded, want error")
	}
}

// --- amends/retracts validation (specs/DESIGN_v3.1_draft.md §2) ------------

// TestIngest_RejectsUnknownAmendsTarget checks that Amends is subject to the
// identical existence check Parents already gets: a Bead naming a not-yet-
// indexed amends target must be rejected, not silently accepted.
func TestIngest_RejectsUnknownAmendsTarget(t *testing.T) {
	e := openT(t)

	root, err := e.Ingest(unsavedBead("patient_registration", nil, map[string]any{"name": "amends test"}))
	if err != nil {
		t.Fatalf("Ingest (root): %v", err)
	}

	unknownTarget := "0000000000000000000000000000000000000000000000000000000000000000"[:64]
	b := unsavedBead("clinical_note", []string{root.ID}, map[string]any{"raw_text": "correction"})
	b.Amends = []string{unknownTarget}
	if _, err := e.Ingest(b); err == nil {
		t.Fatal("Ingest with an unknown amends target succeeded, want error")
	}
}

// TestIngest_RejectsUnknownRetractsTarget mirrors
// TestIngest_RejectsUnknownAmendsTarget for Retracts.
func TestIngest_RejectsUnknownRetractsTarget(t *testing.T) {
	e := openT(t)

	root, err := e.Ingest(unsavedBead("patient_registration", nil, map[string]any{"name": "retracts test"}))
	if err != nil {
		t.Fatalf("Ingest (root): %v", err)
	}

	unknownTarget := "0000000000000000000000000000000000000000000000000000000000000000"[:64]
	b := unsavedBead("retraction", []string{root.ID}, map[string]any{"reason_code": "entered-in-error"})
	b.Retracts = []string{unknownTarget}
	if _, err := e.Ingest(b); err == nil {
		t.Fatal("Ingest with an unknown retracts target succeeded, want error")
	}
}

// TestIngest_RejectsCrossPatientAmends checks specs/DESIGN_v3.1_draft.md
// §2's "cross-patient の amends/retracts は禁止(ingest 時拒否)": a Bead whose
// resolved patient_root differs from an amends target's patient_root must be
// rejected, even though both Beads individually exist and are otherwise
// ingestable.
func TestIngest_RejectsCrossPatientAmends(t *testing.T) {
	e := openT(t)

	rootA, err := e.Ingest(unsavedBead("patient_registration", nil, map[string]any{"name": "patient A"}))
	if err != nil {
		t.Fatalf("Ingest (rootA): %v", err)
	}
	noteA, err := e.Ingest(unsavedBead("clinical_note", []string{rootA.ID}, map[string]any{"raw_text": "note A"}))
	if err != nil {
		t.Fatalf("Ingest (noteA): %v", err)
	}

	rootB, err := e.Ingest(unsavedBead("patient_registration", nil, map[string]any{"name": "patient B"}))
	if err != nil {
		t.Fatalf("Ingest (rootB): %v", err)
	}

	// A Bead under patient B's tree that amends a Bead belonging to patient
	// A: both noteA and the amendment individually exist and are validly
	// indexed, but the cross-patient reference itself must be rejected.
	amendment := unsavedBead("clinical_note", []string{rootB.ID}, map[string]any{"raw_text": "correction"})
	amendment.Amends = []string{noteA.ID}
	if _, err := e.Ingest(amendment); err == nil {
		t.Fatal("Ingest with a cross-patient amends target succeeded, want error")
	}
}

// TestIngest_RejectsCrossPatientRetracts mirrors
// TestIngest_RejectsCrossPatientAmends for Retracts.
func TestIngest_RejectsCrossPatientRetracts(t *testing.T) {
	e := openT(t)

	rootA, err := e.Ingest(unsavedBead("patient_registration", nil, map[string]any{"name": "patient A"}))
	if err != nil {
		t.Fatalf("Ingest (rootA): %v", err)
	}
	noteA, err := e.Ingest(unsavedBead("clinical_note", []string{rootA.ID}, map[string]any{"raw_text": "note A"}))
	if err != nil {
		t.Fatalf("Ingest (noteA): %v", err)
	}

	rootB, err := e.Ingest(unsavedBead("patient_registration", nil, map[string]any{"name": "patient B"}))
	if err != nil {
		t.Fatalf("Ingest (rootB): %v", err)
	}

	retraction := unsavedBead("retraction", []string{rootB.ID}, map[string]any{"reason_code": "entered-in-error"})
	retraction.Retracts = []string{noteA.ID}
	if _, err := e.Ingest(retraction); err == nil {
		t.Fatal("Ingest with a cross-patient retracts target succeeded, want error")
	}
}

// TestIngest_AcceptsSamePatientAmendsAndRetracts is the positive-path
// counterpart to the rejection tests above: an amends/retracts target that
// shares the amending/retracting Bead's own patient_root must be accepted
// (retraction type Beads are explicitly receivable — specs/
// DESIGN_v3.1_draft.md §2's "retraction 型 Bead の受理"), and the resulting
// Bead's Amends/Retracts fields must round-trip through GetBead unchanged.
func TestIngest_AcceptsSamePatientAmendsAndRetracts(t *testing.T) {
	e := openT(t)

	root, err := e.Ingest(unsavedBead("patient_registration", nil, map[string]any{"name": "same patient"}))
	if err != nil {
		t.Fatalf("Ingest (root): %v", err)
	}
	note, err := e.Ingest(unsavedBead("clinical_note", []string{root.ID}, map[string]any{"raw_text": "original"}))
	if err != nil {
		t.Fatalf("Ingest (note): %v", err)
	}

	amendment := unsavedBead("clinical_note", []string{root.ID}, map[string]any{"raw_text": "corrected"})
	amendment.Amends = []string{note.ID}
	savedAmendment, err := e.Ingest(amendment)
	if err != nil {
		t.Fatalf("Ingest (amendment): %v", err)
	}
	if len(savedAmendment.Amends) != 1 || savedAmendment.Amends[0] != note.ID {
		t.Errorf("savedAmendment.Amends = %v, want [%s]", savedAmendment.Amends, note.ID)
	}

	note2, err := e.Ingest(unsavedBead("clinical_note", []string{root.ID}, map[string]any{"raw_text": "entered in error"}))
	if err != nil {
		t.Fatalf("Ingest (note2): %v", err)
	}
	retraction := unsavedBead("retraction", []string{root.ID}, map[string]any{
		"reason_code": "entered-in-error", "authorized_by": "did:medbeads:doctor:12345",
	})
	retraction.Retracts = []string{note2.ID}
	savedRetraction, err := e.Ingest(retraction)
	if err != nil {
		t.Fatalf("Ingest (retraction): %v", err)
	}
	if len(savedRetraction.Retracts) != 1 || savedRetraction.Retracts[0] != note2.ID {
		t.Errorf("savedRetraction.Retracts = %v, want [%s]", savedRetraction.Retracts, note2.ID)
	}

	gotAmendment, err := e.GetBead(savedAmendment.ID)
	if err != nil {
		t.Fatalf("GetBead(amendment): %v", err)
	}
	if len(gotAmendment.Amends) != 1 || gotAmendment.Amends[0] != note.ID {
		t.Errorf("GetBead(amendment).Amends = %v, want [%s]", gotAmendment.Amends, note.ID)
	}

	gotRetraction, err := e.GetBead(savedRetraction.ID)
	if err != nil {
		t.Fatalf("GetBead(retraction): %v", err)
	}
	if len(gotRetraction.Retracts) != 1 || gotRetraction.Retracts[0] != note2.ID {
		t.Errorf("GetBead(retraction).Retracts = %v, want [%s]", gotRetraction.Retracts, note2.ID)
	}
}

// --- duplicate ingest: idempotent -------------------------------------------

func TestIngest_DuplicateIsIdempotent(t *testing.T) {
	e := openT(t)

	root, err := e.Ingest(unsavedBead("patient_registration", nil, map[string]any{"name": "dup test"}))
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

	b := unsavedBead("patient_registration", nil, nil)
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
