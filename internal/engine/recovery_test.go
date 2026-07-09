package engine

import (
	"testing"

	"github.com/medbeads/medbeads/internal/engine/bead"
	"github.com/medbeads/medbeads/internal/engine/pod"
)

// TestOpen_CatchUpRecoversPodOnlyWrite simulates the R1.3 crash scenario end
// to end at the Engine level: append a Bead directly to a patient's Pod file
// (bypassing Ingest/IndexBead entirely — exactly the state a crash between
// "Pod append+fsync" and "IndexBead commit" would leave), close the Engine,
// then Open a fresh Engine against the same data directory and confirm
// GetBead/ListPatientBeads recover the orphaned write via CatchUp.
func TestOpen_CatchUpRecoversPodOnlyWrite(t *testing.T) {
	dir := t.TempDir()

	e := mustOpen(t, dir)
	root, err := e.Ingest(unsavedBead("patient_registration", nil, nil, map[string]any{"name": "crash test"}))
	if err != nil {
		t.Fatalf("Ingest (root): %v", err)
	}

	// Append a second Bead straight to the Pod file via the Store's path
	// resolution, never calling Ingest — this is the "crash before index
	// commit" state that Ingest's own writer registry would normally
	// prevent from happening this way, but a real crash bypasses Ingest
	// entirely, so this is the faithful way to simulate it.
	store := pod.NewStore(dir)
	podPath, err := store.PatientPodPath(root.ID)
	if err != nil {
		t.Fatalf("PatientPodPath: %v", err)
	}
	crashBead, err := bead.WithID(bead.Bead{
		Type:      "fhir_observation",
		Timestamp: nextTimestamp(),
		Parents:   []string{root.ID},
		Content:   map[string]any{"note": "orphaned write"},
	})
	if err != nil {
		t.Fatalf("bead.WithID: %v", err)
	}
	// Ingest already opened this Pod path through its own writer registry
	// (to write root); open a second raw *pod.Writer here only to simulate
	// bytes a *different, now-dead* process wrote before crashing. Close
	// Engine e first so its registry's Writer handle isn't fighting this one
	// for the same *os.File.
	if err := e.Close(); err != nil {
		t.Fatalf("Close (before simulated crash write): %v", err)
	}

	w, err := pod.OpenWriter(podPath)
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	if _, err := w.Append(crashBead, pod.CodecZstd, pod.NewMeta(root.ID)); err != nil {
		t.Fatalf("Append (crash bead): %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close (raw writer): %v", err)
	}

	// Re-open: Open's CatchUp must index the orphaned write.
	e2 := mustOpen(t, dir)

	got, err := e2.GetBead(crashBead.ID)
	if err != nil {
		t.Fatalf("GetBead(crashBead) after reopen: %v", err)
	}
	if got.ID != crashBead.ID {
		t.Errorf("GetBead(crashBead).ID = %s, want %s", got.ID, crashBead.ID)
	}

	all, err := e2.ListPatientBeads(root.ID)
	if err != nil {
		t.Fatalf("ListPatientBeads: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("ListPatientBeads after recovery = %d beads, want 2 (root + orphaned write)", len(all))
	}
}

// mustOpen is like openT but lets the caller Close explicitly partway
// through a test (e.g. to simulate a crash then a clean re-open), while
// still registering a best-effort t.Cleanup Close for safety.
func mustOpen(t *testing.T, dir string) *Engine {
	t.Helper()
	e, err := Open(dir)
	if err != nil {
		t.Fatalf("Open(%s): %v", dir, err)
	}
	t.Cleanup(func() { _ = e.Close() })
	return e
}

// TestOpen_RecoversDuplicateFrameFromRetriedCrashedIngest reproduces the
// data-reviewer's finding end to end at the Engine level: "Pod append
// succeeds -> crash before index commit -> retried ingest re-appends the
// same content (so the same Bead ID) -> crash again", leaving two frames
// for one Bead ID in the same patient Pod, with neither ever indexed.
// Before the ON CONFLICT (id) DO NOTHING fix (write.go), Open's CatchUp hit
// "UNIQUE constraint failed: beads.id" on the second frame and failed
// permanently — this test asserts Open now succeeds and GetBead reads the
// Bead back correctly.
func TestOpen_RecoversDuplicateFrameFromRetriedCrashedIngest(t *testing.T) {
	dir := t.TempDir()
	store := pod.NewStore(dir)

	root, err := bead.WithID(bead.Bead{
		Type:      "patient_registration",
		Timestamp: nextTimestamp(),
		Content:   map[string]any{"name": "duplicate-frame recovery"},
	})
	if err != nil {
		t.Fatalf("bead.WithID (root): %v", err)
	}
	podPath, err := store.EnsurePatientPodDir(root.ID)
	if err != nil {
		t.Fatalf("EnsurePatientPodDir: %v", err)
	}

	// Simulate "Pod append succeeds, then the process crashes before index
	// commit" twice in a row for the *same* Bead: no Engine/Ingest call
	// ever gets to run IndexBead for either frame — this is what a crashed
	// process followed by an equally-crash-prone retry would leave behind,
	// entirely bypassing Ingest's own in-memory duplicate check (which only
	// sees what's already indexed).
	w1, err := pod.OpenWriter(podPath)
	if err != nil {
		t.Fatalf("OpenWriter (first crash write): %v", err)
	}
	if _, err := w1.Append(root, pod.CodecZstd, pod.NewMeta(root.ID)); err != nil {
		t.Fatalf("Append (first frame): %v", err)
	}
	if err := w1.Close(); err != nil {
		t.Fatalf("Close (first writer): %v", err)
	}

	w2, err := pod.OpenWriter(podPath)
	if err != nil {
		t.Fatalf("OpenWriter (retried crash write): %v", err)
	}
	if _, err := w2.Append(root, pod.CodecZstd, pod.NewMeta(root.ID)); err != nil {
		t.Fatalf("Append (duplicate frame, retried ingest): %v", err)
	}
	if err := w2.Close(); err != nil {
		t.Fatalf("Close (second writer): %v", err)
	}

	// Open must succeed (CatchUp must recover this Pod, not fail
	// permanently on the duplicate frame's UNIQUE constraint).
	e := mustOpen(t, dir)

	got, err := e.GetBead(root.ID)
	if err != nil {
		t.Fatalf("GetBead(%s) after recovery: %v", root.ID, err)
	}
	if got.ID != root.ID {
		t.Errorf("GetBead(%s).ID = %s, want %s", root.ID, got.ID, root.ID)
	}

	all, err := e.ListPatientBeads(root.ID)
	if err != nil {
		t.Fatalf("ListPatientBeads: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("ListPatientBeads after recovery = %d beads, want 1 (duplicate frame must not double-count)", len(all))
	}

	// A subsequent Ingest of the exact same root (as if the surviving
	// process retried a third time after Open recovered) must still be
	// treated as idempotent, not as yet another duplicate frame appended
	// blindly — this exercises Ingest's own already-indexed short-circuit
	// now that the index is caught up.
	again, err := e.Ingest(root)
	if err != nil {
		t.Fatalf("Ingest (post-recovery retry): %v", err)
	}
	if again.ID != root.ID {
		t.Errorf("post-recovery Ingest returned ID %s, want %s", again.ID, root.ID)
	}
	allAfter, err := e.ListPatientBeads(root.ID)
	if err != nil {
		t.Fatalf("ListPatientBeads (after post-recovery retry): %v", err)
	}
	if len(allAfter) != 1 {
		t.Errorf("ListPatientBeads after post-recovery Ingest = %d beads, want 1", len(allAfter))
	}

	// The "earliest frame wins" offset semantics themselves are asserted at
	// the index layer (index/catchup_test.go's
	// TestCatchUp_DuplicateFrame_SameBeadTwiceInOnePod), since
	// engine.GetBead returns a verified bead.Bead with no exposed
	// offset/length to compare here — this test's job is Engine-level
	// end-to-end recovery (Open succeeds, GetBead/ListPatientBeads/Ingest
	// all behave correctly afterward), which is what the assertions above
	// cover.
}
