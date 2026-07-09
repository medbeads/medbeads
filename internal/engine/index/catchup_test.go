package index

import (
	"path/filepath"
	"testing"

	"github.com/medbeads/medbeads/internal/engine/bead"
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

// appendDuplicateFrame writes a second, byte-for-byte-independent frame for
// the exact same Bead ID directly to podPath, simulating the data-reviewer's
// reproduction: "Pod append succeeds -> crash before index commit -> retried
// ingest appends *again* -> crash again", leaving two frames for one
// content-addressed Bead ID in the same Pod, neither yet indexed. It returns
// the appended frame's offset for callers that want to reason about which
// frame (first vs. second) survives indexing.
func appendDuplicateFrame(t *testing.T, podPath string, dup bead.Bead) int64 {
	t.Helper()
	w, err := pod.OpenWriter(podPath)
	if err != nil {
		t.Fatalf("appendDuplicateFrame: OpenWriter: %v", err)
	}
	defer w.Close()

	res, err := w.Append(dup, pod.CodecZstd, pod.NewMeta(dup.ID))
	if err != nil {
		t.Fatalf("appendDuplicateFrame: Append: %v", err)
	}
	return res.Offset
}

// TestCatchUp_DuplicateFrame_SameBeadTwiceInOnePod is the data-reviewer's
// repro at the index layer: two frames for the same Bead ID exist in one
// Pod, neither indexed yet (watermark 0). CatchUp must succeed — not fail
// with "UNIQUE constraint failed: beads.id" — and must leave exactly one
// beads row (and one beads_fts row) for that ID, pointing at the *first*
// frame's offset (see write.go's ON CONFLICT DO NOTHING doc comment: the
// earliest-written frame wins).
func TestCatchUp_DuplicateFrame_SameBeadTwiceInOnePod(t *testing.T) {
	dataDir := t.TempDir()
	store := pod.NewStore(dataDir)

	root, err := bead.WithID(bead.Bead{
		Type:      "patient_registration",
		Timestamp: "2026-01-01T00:00:00Z",
		Content:   map[string]any{"name": "dup-frame patient"},
	})
	if err != nil {
		t.Fatalf("bead.WithID (root): %v", err)
	}
	podPath, err := store.EnsurePatientPodDir(root.ID)
	if err != nil {
		t.Fatalf("EnsurePatientPodDir: %v", err)
	}

	w, err := pod.OpenWriter(podPath)
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	firstRes, err := w.Append(root, pod.CodecZstd, pod.NewMeta(root.ID))
	if err != nil {
		t.Fatalf("Append (first frame): %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Second frame: identical content -> identical ID (root re-ingested
	// after a crash, per the reviewer's scenario). Content-addressing means
	// this is not a new Bead, just a redundant copy of the same bytes.
	secondOffset := appendDuplicateFrame(t, podPath, root)
	if secondOffset == firstRes.Offset {
		t.Fatal("test precondition failed: second frame did not land at a new offset")
	}

	db := openT(t)
	if err := CatchUp(db, podPath); err != nil {
		t.Fatalf("CatchUp on a pod with a duplicate frame: %v (must recover, not fail permanently)", err)
	}

	if got := countRows(t, db.sqlDB, "SELECT COUNT(*) FROM beads WHERE id = ?", root.ID); got != 1 {
		t.Errorf("beads rows for %s = %d, want 1 (no duplicate row)", root.ID, got)
	}
	if got := countRows(t, db.sqlDB, "SELECT COUNT(*) FROM beads_fts"); got != 1 {
		t.Errorf("beads_fts rows = %d, want 1 (no duplicate FTS row)", got)
	}

	ref, err := db.GetBead(root.ID)
	if err != nil {
		t.Fatalf("GetBead(%s): %v", root.ID, err)
	}
	if ref.Offset != firstRes.Offset {
		t.Errorf("GetBead(%s).Offset = %d, want %d (first-written frame must win)", root.ID, ref.Offset, firstRes.Offset)
	}

	// The watermark must still advance past both frames (the whole Pod is
	// fully scanned), even though only the first frame produced a row.
	scan, err := pod.Scan(podPath, false)
	if err != nil {
		t.Fatalf("pod.Scan: %v", err)
	}
	watermark, err := db.PodWatermark(podPath)
	if err != nil {
		t.Fatalf("PodWatermark: %v", err)
	}
	if watermark != scan.ValidUpto {
		t.Errorf("watermark = %d, want %d (pod end, both frames scanned)", watermark, scan.ValidUpto)
	}
}

// TestReindex_DuplicateFrame_SameBeadTwiceInOnePod is the same repro as
// TestCatchUp_DuplicateFrame_SameBeadTwiceInOnePod but through Reindex (the
// "rebuild index.db from scratch" path, R1.4) rather than CatchUp — the
// data-reviewer's report specifically named both Open (which drives
// CatchUp) and reindex as permanently failing before this fix.
func TestReindex_DuplicateFrame_SameBeadTwiceInOnePod(t *testing.T) {
	dataDir := t.TempDir()
	podPath, beads := writeRealPod(t, dataDir)
	root := beads[0]

	firstScan, err := pod.Scan(podPath, false)
	if err != nil {
		t.Fatalf("pod.Scan (before duplicate): %v", err)
	}
	_ = appendDuplicateFrame(t, podPath, root)

	db, err := Reindex(dataDir, filepath.Join(dataDir, "index.db"), DefaultFlattener{})
	if err != nil {
		t.Fatalf("Reindex on a pod with a duplicate frame: %v (must recover, not fail permanently)", err)
	}
	defer db.Close()

	if got := countRows(t, db.sqlDB, "SELECT COUNT(*) FROM beads WHERE id = ?", root.ID); got != 1 {
		t.Errorf("beads rows for %s = %d, want 1 (no duplicate row)", root.ID, got)
	}
	if got := countRows(t, db.sqlDB, "SELECT COUNT(*) FROM beads"); got != len(beads) {
		t.Errorf("total beads rows = %d, want %d (duplicate frame must not add a row)", got, len(beads))
	}

	ref, err := db.GetBead(root.ID)
	if err != nil {
		t.Fatalf("GetBead(%s): %v", root.ID, err)
	}
	// root is beads[0], written first in writeRealPod, so its original
	// frame's offset (captured before the duplicate append) must still be
	// what beads.offset points at.
	wantOffset := int64(0)
	if firstScan.Records != nil && len(firstScan.Records) > 0 {
		wantOffset = firstScan.Records[0].Offset
	}
	if ref.Offset != wantOffset {
		t.Errorf("GetBead(%s).Offset = %d, want %d (first-written frame must win)", root.ID, ref.Offset, wantOffset)
	}
}
