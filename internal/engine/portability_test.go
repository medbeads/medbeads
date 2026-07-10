package engine

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/medbeads/medbeads/internal/engine/index"
)

// TestOpen_RelativeDataDir_PortableAcrossCwd is this task's headline
// pods.path portability regression test: building a data directory via a
// *relative* -data path (e.g. `-data ./medbeads_data`, exactly as
// docs/requirements.md's real repro used), closing it, then reopening the
// exact same on-disk data directory from a *different* current working
// directory, must still let GetBead/ListPatientBeads succeed.
//
// Before this task's fix, pods.path stored whatever literal path string was
// passed to Ingest's Pod writer (relative-to-whatever-cwd-was-current-at-
// write-time, or absolute), so a later process with a different cwd could
// not resolve it — this test reproduces exactly that shape (relative
// dataDir at write time, different cwd at read time) and asserts it now
// works.
func TestOpen_RelativeDataDir_PortableAcrossCwd(t *testing.T) {
	// Two distinct, real directories the "writer" and "reader" processes
	// will each treat as their own cwd — Chdir'ing between them is what
	// makes a *relative* -data argument actually resolve to two different
	// absolute locations if it were still stored verbatim (the bug this
	// test guards against).
	writerCwd := t.TempDir()
	readerCwd := t.TempDir()

	origCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origCwd) })

	const relDataDir = "medbeads_data" // mirrors this task's "-data ./medbeads_data" repro

	// --- "writer" process: relative -data, ingest, close. ---
	if err := os.Chdir(writerCwd); err != nil {
		t.Fatalf("Chdir(writerCwd): %v", err)
	}
	e := mustOpen(t, relDataDir)

	root, err := e.Ingest(unsavedBead("patient_registration", nil, map[string]any{"name": "portability test"}))
	if err != nil {
		t.Fatalf("Ingest (root): %v", err)
	}
	child, err := e.Ingest(unsavedBead("fhir_observation", []string{root.ID}, map[string]any{"note": "child"}))
	if err != nil {
		t.Fatalf("Ingest (child): %v", err)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("Close (writer): %v", err)
	}

	// Sanity check: the data actually landed under writerCwd/medbeads_data,
	// not readerCwd — otherwise this test would not be exercising the bug
	// at all.
	if _, err := os.Stat(filepath.Join(writerCwd, relDataDir, "index.db")); err != nil {
		t.Fatalf("expected index.db under writerCwd/%s: %v", relDataDir, err)
	}

	// --- "reader" process: same relative -data string, different cwd. ---
	if err := os.Chdir(readerCwd); err != nil {
		t.Fatalf("Chdir(readerCwd): %v", err)
	}
	// The exact same on-disk data directory writerCwd created, reached via
	// a relative path resolved from readerCwd instead — this is the
	// "別 cwd から serve すると全 Pod が開けない" repro shape.
	dataDirFromReader := filepath.Join(writerCwd, relDataDir)
	e2 := mustOpen(t, dataDirFromReader)

	gotRoot, err := e2.GetBead(root.ID)
	if err != nil {
		t.Fatalf("GetBead(root) from a different cwd: %v", err)
	}
	if gotRoot.ID != root.ID {
		t.Errorf("GetBead(root).ID = %s, want %s", gotRoot.ID, root.ID)
	}

	gotChild, err := e2.GetBead(child.ID)
	if err != nil {
		t.Fatalf("GetBead(child) from a different cwd: %v", err)
	}
	if gotChild.ID != child.ID {
		t.Errorf("GetBead(child).ID = %s, want %s", gotChild.ID, child.ID)
	}

	all, err := e2.ListPatientBeads(root.ID)
	if err != nil {
		t.Fatalf("ListPatientBeads from a different cwd: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("ListPatientBeads from a different cwd = %d beads, want 2", len(all))
	}
}

// TestOpen_RelativeDataDir_ReindexPortableAcrossCwd is the same portability
// guarantee via the Reindex path (`medbeadsd reindex`) rather than the live
// Ingest path: reindexing from a relative -data argument must also produce
// pods.path rows a different-cwd process can resolve.
func TestOpen_RelativeDataDir_ReindexPortableAcrossCwd(t *testing.T) {
	writerCwd := t.TempDir()
	readerCwd := t.TempDir()

	origCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origCwd) })

	const relDataDir = "medbeads_data"

	if err := os.Chdir(writerCwd); err != nil {
		t.Fatalf("Chdir(writerCwd): %v", err)
	}
	e := mustOpen(t, relDataDir)
	root, err := e.Ingest(unsavedBead("patient_registration", nil, map[string]any{"name": "reindex portability test"}))
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reindex with the same relative -data string a real `medbeadsd reindex
	// -data ./medbeads_data` invocation would use (cmd/medbeadsd/main.go's
	// runReindex passes *dataDir straight through to index.Reindex,
	// unmodified).
	dbPath := filepath.Join(relDataDir, "index.db")
	rebuilt, err := index.Reindex(relDataDir, dbPath, index.DefaultFlattener{})
	if err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	if err := rebuilt.Close(); err != nil {
		t.Fatalf("Close (rebuilt): %v", err)
	}

	if err := os.Chdir(readerCwd); err != nil {
		t.Fatalf("Chdir(readerCwd): %v", err)
	}
	absDataDir := filepath.Join(writerCwd, relDataDir)
	e2 := mustOpen(t, absDataDir)
	got, err := e2.GetBead(root.ID)
	if err != nil {
		t.Fatalf("GetBead after Reindex, from a different cwd: %v", err)
	}
	if got.ID != root.ID {
		t.Errorf("GetBead(root).ID = %s, want %s", got.ID, root.ID)
	}
}
