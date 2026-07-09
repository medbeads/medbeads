package index

import (
	"path/filepath"
	"testing"

	"github.com/medbeads/medbeads/internal/engine/pod"
)

// TestCatchUp_ResumesFromWatermark rewinds a fully-indexed Pod's watermark
// back to just after the first record, then calls CatchUp and checks that
// only the remaining (previously un-indexed) records are added — not
// duplicated, not re-processed from the start.
func TestCatchUp_ResumesFromWatermark(t *testing.T) {
	dataDir := t.TempDir()
	podPath, beads := writeRealPod(t, dataDir)

	db, err := Reindex(dataDir, filepath.Join(dataDir, "index.db"), DefaultFlattener{})
	if err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	defer db.Close()

	if got := countRows(t, db.sqlDB, "SELECT COUNT(*) FROM beads"); got != len(beads) {
		t.Fatalf("beads count after Reindex = %d, want %d", got, len(beads))
	}

	// Rewind the watermark to just after the first record (root), as if the
	// index had crashed after indexing only that one.
	scan, err := pod.Scan(podPath, true)
	if err != nil {
		t.Fatalf("pod.Scan: %v", err)
	}
	firstRecordEnd := scan.Records[0].Length
	if _, err := db.sqlDB.Exec(`UPDATE pods SET indexed_upto = ? WHERE path = ?`, firstRecordEnd, podPath); err != nil {
		t.Fatalf("rewind watermark: %v", err)
	}

	// Also delete the rows the rewound watermark claims are un-indexed, so
	// this test actually exercises re-adding them (rather than relying
	// solely on INSERT OR IGNORE / PRIMARY KEY conflict skipping to mask a
	// bug in indexPodFrom's offset filtering).
	f := DefaultFlattener{}
	for _, b := range beads[1:] {
		// beads_fts is contentless (content='') and has no id/search_text
		// column retrievable via SELECT (see migrations/0001_init.sql), and
		// a contentless table rejects plain DELETE entirely — removing a
		// row requires FTS5's special "delete" command, which must repeat
		// the exact search_text originally indexed. Recomputing it via the
		// same deterministic Flattener used at index time (rather than
		// trying to read it back from beads_fts) sidesteps that
		// unretrievability.
		var beadRowID int64
		if err := db.sqlDB.QueryRow(`SELECT rowid FROM beads WHERE id = ?`, b.ID).Scan(&beadRowID); err != nil {
			t.Fatalf("lookup rowid for %s: %v", b.ID, err)
		}
		searchText, _ := f.Flatten(b)
		if _, err := db.sqlDB.Exec(
			`INSERT INTO beads_fts(beads_fts, rowid, search_text) VALUES ('delete', ?, ?)`,
			beadRowID, searchText,
		); err != nil {
			t.Fatalf("delete fts for %s: %v", b.ID, err)
		}
		if _, err := db.sqlDB.Exec(`DELETE FROM beads WHERE id = ?`, b.ID); err != nil {
			t.Fatalf("delete bead %s: %v", b.ID, err)
		}
		if _, err := db.sqlDB.Exec(`DELETE FROM bead_edges WHERE child_id = ?`, b.ID); err != nil {
			t.Fatalf("delete edges for %s: %v", b.ID, err)
		}
		if _, err := db.sqlDB.Exec(`DELETE FROM bead_antigens WHERE bead_id = ?`, b.ID); err != nil {
			t.Fatalf("delete antigens for %s: %v", b.ID, err)
		}
	}

	if err := CatchUp(db, podPath); err != nil {
		t.Fatalf("CatchUp: %v", err)
	}

	if got := countRows(t, db.sqlDB, "SELECT COUNT(*) FROM beads"); got != len(beads) {
		t.Errorf("beads count after CatchUp = %d, want %d (all records recovered)", got, len(beads))
	}
	for _, b := range beads {
		if _, err := db.GetBead(b.ID); err != nil {
			t.Errorf("GetBead(%s) after CatchUp: %v", b.ID, err)
		}
	}

	watermark, err := db.PodWatermark(podPath)
	if err != nil {
		t.Fatalf("PodWatermark: %v", err)
	}
	if watermark != scan.ValidUpto {
		t.Errorf("watermark after CatchUp = %d, want %d (pod end)", watermark, scan.ValidUpto)
	}
}

// TestCatchUp_IsIdempotent runs CatchUp twice in a row against an
// already-fully-indexed Pod and checks the second call adds nothing (no
// duplicate rows, watermark unchanged).
func TestCatchUp_IsIdempotent(t *testing.T) {
	dataDir := t.TempDir()
	podPath, beads := writeRealPod(t, dataDir)

	db, err := Reindex(dataDir, filepath.Join(dataDir, "index.db"), DefaultFlattener{})
	if err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	defer db.Close()

	beforeCount := countRows(t, db.sqlDB, "SELECT COUNT(*) FROM beads")
	beforeWatermark, err := db.PodWatermark(podPath)
	if err != nil {
		t.Fatalf("PodWatermark (before): %v", err)
	}

	if err := CatchUp(db, podPath); err != nil {
		t.Fatalf("CatchUp (idempotent call): %v", err)
	}

	afterCount := countRows(t, db.sqlDB, "SELECT COUNT(*) FROM beads")
	afterWatermark, err := db.PodWatermark(podPath)
	if err != nil {
		t.Fatalf("PodWatermark (after): %v", err)
	}

	if afterCount != beforeCount {
		t.Errorf("beads count changed on idempotent CatchUp: before=%d after=%d", beforeCount, afterCount)
	}
	if afterWatermark != beforeWatermark {
		t.Errorf("watermark changed on idempotent CatchUp: before=%d after=%d", beforeWatermark, afterWatermark)
	}
	if afterCount != len(beads) {
		t.Errorf("beads count = %d, want %d", afterCount, len(beads))
	}
}

// TestCatchUp_RecoversCrashOnlyAppendedToPod simulates the R1.3 crash-
// recovery scenario end to end: index a Pod, then append one more Bead to
// the Pod directly (bypassing IndexBead entirely, as a crash between "Pod
// append+fsync" and "SQLite index commit" would leave things) — the new
// Bead exists in the Pod but not yet in index.db. CatchUp must recover it.
func TestCatchUp_RecoversCrashOnlyAppendedToPod(t *testing.T) {
	dataDir := t.TempDir()
	podPath, beads := writeRealPod(t, dataDir)

	db, err := Reindex(dataDir, filepath.Join(dataDir, "index.db"), DefaultFlattener{})
	if err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	defer db.Close()

	// Append a new Bead straight to the Pod file, never calling IndexBead —
	// this is the "crash before index commit" state.
	root := beads[0]
	w, err := pod.OpenWriter(podPath)
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	crashBead := testBead(t, "fhir_observation", "post-crash observation", []string{root.ID}, nil, nil)
	if _, err := w.Append(crashBead, pod.CodecZstd, pod.NewMeta(root.ID)); err != nil {
		t.Fatalf("Append (crash bead): %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := db.GetBead(crashBead.ID); err == nil {
		t.Fatal("GetBead(crashBead) succeeded before CatchUp, want ErrNotFound (test setup invalid)")
	}

	if err := CatchUp(db, podPath); err != nil {
		t.Fatalf("CatchUp: %v", err)
	}

	if _, err := db.GetBead(crashBead.ID); err != nil {
		t.Errorf("GetBead(crashBead) after CatchUp: %v", err)
	}
	if got := countRows(t, db.sqlDB, "SELECT COUNT(*) FROM beads"); got != len(beads)+1 {
		t.Errorf("beads count after CatchUp = %d, want %d", got, len(beads)+1)
	}
}

// TestCatchUp_UnregisteredPod checks CatchUp against a Pod path with no
// prior pods row at all (never indexed once): it must register the Pod and
// index every record, equivalent to indexing from offset 0.
func TestCatchUp_UnregisteredPod(t *testing.T) {
	dataDir := t.TempDir()
	podPath, beads := writeRealPod(t, dataDir)

	db := openT(t)

	if _, err := db.PodWatermark(podPath); err == nil {
		t.Fatal("PodWatermark before CatchUp succeeded, want ErrNotFound")
	}

	if err := CatchUp(db, podPath); err != nil {
		t.Fatalf("CatchUp (never-seen pod): %v", err)
	}
	for _, b := range beads {
		if _, err := db.GetBead(b.ID); err != nil {
			t.Errorf("GetBead(%s) after CatchUp: %v", b.ID, err)
		}
	}
}
