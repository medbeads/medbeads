package index

import (
	"path/filepath"
	"sort"
	"testing"

	"github.com/medbeads/medbeads/internal/engine/bead"
	"github.com/medbeads/medbeads/internal/engine/pod"
)

// writeRealPod writes a small, realistic patient sub-graph (registration +
// two children, one of which is a sibling-less second child so bead_edges
// has more than one row) to a real Pod file via pod.Writer, returning the
// Beads in write order and the Pod's path. This is the fixture both
// TestReindex_MatchesManualIndexBead and TestCatchUp_* build on.
func writeRealPod(t *testing.T, dataDir string) (podPath string, beads []bead.Bead) {
	t.Helper()

	store := pod.NewStore(dataDir)
	root, err := bead.WithID(bead.Bead{
		Type:      "patient_registration",
		Timestamp: "2026-01-01T00:00:00Z",
		Content:   map[string]any{"name": "Synthea Test Patient"},
	})
	if err != nil {
		t.Fatalf("bead.WithID (root): %v", err)
	}

	path, err := store.EnsurePatientPodDir(root.ID)
	if err != nil {
		t.Fatalf("EnsurePatientPodDir: %v", err)
	}
	w, err := pod.OpenWriter(path)
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	if _, err := w.Append(root, pod.CodecZstd, pod.NewMeta(root.ID)); err != nil {
		t.Fatalf("Append (root): %v", err)
	}
	beads = append(beads, root)

	obs1, err := bead.WithID(bead.Bead{
		Type:      "fhir_observation",
		Timestamp: "2026-01-02T00:00:00Z",
		Parents:   []string{root.ID},
		Antigens:  []string{"loinc:718-7", "organ:renal"},
		Content:   map[string]any{"note": "hemoglobin 12.3 g/dL"},
	})
	if err != nil {
		t.Fatalf("bead.WithID (obs1): %v", err)
	}
	if _, err := w.Append(obs1, pod.CodecZstd, pod.NewMeta(root.ID)); err != nil {
		t.Fatalf("Append (obs1): %v", err)
	}
	beads = append(beads, obs1)

	obs2, err := bead.WithID(bead.Bead{
		Type:      "fhir_medicationrequest",
		Timestamp: "2026-01-03T00:00:00Z",
		Parents:   []string{root.ID, obs1.ID},
		Antigens:  []string{"rxnorm:6919"},
		Content:   map[string]any{"drug": "メロペネム 1g 点滴静注 8時間毎"},
	})
	if err != nil {
		t.Fatalf("bead.WithID (obs2): %v", err)
	}
	if _, err := w.Append(obs2, pod.CodecZstd, pod.NewMeta(root.ID)); err != nil {
		t.Fatalf("Append (obs2): %v", err)
	}
	beads = append(beads, obs2)

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return path, beads
}

// TestReindex_MatchesManualIndexBead is the task's headline Reindex test:
// build a real Pod with pod.Writer, Reindex it from scratch, and compare
// every row (beads, edges, antigens, FTS) against a second database built
// by manually calling IndexBead for the same Beads — the two must be
// identical, proving Reindex faithfully reconstructs what a live write path
// would have produced.
func TestReindex_MatchesManualIndexBead(t *testing.T) {
	dataDir := t.TempDir()
	store := pod.NewStore(dataDir)
	podPath, beads := writeRealPod(t, dataDir)
	// pods.path is stored dataDir-relative by every real write path
	// (engine.Ingest / index.Reindex / index.CatchUp — see this task's
	// pods.path portability fix); this hand-built "reference" DB call must
	// match that convention too, or this test would only be proving Reindex
	// matches an already-nonstandard manual construction rather than a real
	// write path's actual behavior.
	relPodPath, err := store.RelPath(podPath)
	if err != nil {
		t.Fatalf("RelPath: %v", err)
	}

	// --- reference DB: manual IndexBead calls, mirroring what a live
	// store/ingest layer would have done at write time. ---
	refDB, err := Open(filepath.Join(dataDir, "ref.db"))
	if err != nil {
		t.Fatalf("Open (ref): %v", err)
	}
	defer refDB.Close()

	scan, err := pod.Scan(podPath, true)
	if err != nil {
		t.Fatalf("pod.Scan: %v", err)
	}
	if scan.Damaged {
		t.Fatalf("pod.Scan reported damage on a freshly-written pod: %v", scan.DamageErr)
	}
	if len(scan.Records) != len(beads) {
		t.Fatalf("pod.Scan returned %d records, want %d", len(scan.Records), len(beads))
	}
	for _, rec := range scan.Records {
		b, err := decodeRecordBead(rec)
		if err != nil {
			t.Fatalf("decodeRecordBead: %v", err)
		}
		indexBeadT(t, refDB, b, BeadLocation{
			PodPath:     relPodPath,
			PatientRoot: rec.Meta.PatientRoot,
			Offset:      rec.Offset,
			Length:      rec.Length,
		})
	}

	// --- Reindex path under test. ---
	reDB, err := Reindex(dataDir, filepath.Join(dataDir, "index.db"), DefaultFlattener{})
	if err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	defer reDB.Close()

	// Every original Bead must resolve identically through both DBs.
	for _, b := range beads {
		wantRef, err := refDB.GetBead(b.ID)
		if err != nil {
			t.Fatalf("refDB.GetBead(%s): %v", b.ID, err)
		}
		gotRe, err := reDB.GetBead(b.ID)
		if err != nil {
			t.Fatalf("reDB.GetBead(%s): %v", b.ID, err)
		}
		if wantRef != gotRe {
			t.Errorf("GetBead(%s) mismatch:\n ref     = %+v\n reindex = %+v", b.ID, wantRef, gotRe)
		}

		wantEdges, err := refDB.GetEdges(b.ID)
		if err != nil {
			t.Fatalf("refDB.GetEdges(%s): %v", b.ID, err)
		}
		gotEdges, err := reDB.GetEdges(b.ID)
		if err != nil {
			t.Fatalf("reDB.GetEdges(%s): %v", b.ID, err)
		}
		if !equalStringSlices(wantEdges, gotEdges) {
			t.Errorf("GetEdges(%s) mismatch: ref=%v reindex=%v", b.ID, wantEdges, gotEdges)
		}

		wantAntigens, err := refDB.GetAntigens(b.ID)
		if err != nil {
			t.Fatalf("refDB.GetAntigens(%s): %v", b.ID, err)
		}
		gotAntigens, err := reDB.GetAntigens(b.ID)
		if err != nil {
			t.Fatalf("reDB.GetAntigens(%s): %v", b.ID, err)
		}
		if !equalStringSlices(wantAntigens, gotAntigens) {
			t.Errorf("GetAntigens(%s) mismatch: ref=%v reindex=%v", b.ID, wantAntigens, gotAntigens)
		}
	}

	// FTS must be populated identically: the trigram search that finds the
	// medication bead in one DB must find it in the other.
	refHits, err := refDB.Search("ロペネ", 0)
	if err != nil {
		t.Fatalf("refDB.Search: %v", err)
	}
	reHits, err := reDB.Search("ロペネ", 0)
	if err != nil {
		t.Fatalf("reDB.Search: %v", err)
	}
	if len(refHits) != 1 || len(reHits) != 1 || refHits[0].BeadID != reHits[0].BeadID {
		t.Errorf("FTS hits mismatch: ref=%+v reindex=%+v", refHits, reHits)
	}

	// Row counts across every table must match exactly (no extra/missing
	// rows in either direction).
	for _, table := range []string{"beads", "bead_edges", "bead_antigens", "beads_fts"} {
		wantN := countRows(t, refDB.sqlDB, "SELECT COUNT(*) FROM "+table)
		gotN := countRows(t, reDB.sqlDB, "SELECT COUNT(*) FROM "+table)
		if wantN != gotN {
			t.Errorf("table %s row count mismatch: ref=%d reindex=%d", table, wantN, gotN)
		}
	}

	// Watermark must land at the Pod's full size in both.
	info, err := pod.Scan(podPath, false)
	if err != nil {
		t.Fatalf("pod.Scan (size check): %v", err)
	}
	gotWatermark, err := reDB.PodWatermark(relPodPath)
	if err != nil {
		t.Fatalf("reDB.PodWatermark: %v", err)
	}
	if gotWatermark != info.ValidUpto {
		t.Errorf("reDB watermark = %d, want %d (pod size)", gotWatermark, info.ValidUpto)
	}
}

// TestReindex_StartsFromScratch checks that Reindex on a data directory
// containing an existing (differently-shaped) index.db discards it rather
// than merging — R1.4's "ゼロからの完全再構築" guarantee.
func TestReindex_StartsFromScratch(t *testing.T) {
	dataDir := t.TempDir()
	_, beads := writeRealPod(t, dataDir)
	dbPath := filepath.Join(dataDir, "index.db")

	// Pre-populate index.db with a bogus row that Reindex must not preserve.
	pre, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open (pre-existing): %v", err)
	}
	bogus := testBead(t, "bogus", "should not survive reindex", nil, nil, nil)
	indexBeadT(t, pre, bogus, BeadLocation{PodPath: "nonexistent.pod", PatientRoot: "", Offset: 0, Length: 1})
	if err := pre.Close(); err != nil {
		t.Fatalf("Close (pre-existing): %v", err)
	}

	db, err := Reindex(dataDir, dbPath, DefaultFlattener{})
	if err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	defer db.Close()

	if _, err := db.GetBead(bogus.ID); err == nil {
		t.Error("GetBead(bogus) succeeded after Reindex, want ErrNotFound (Reindex must start from scratch)")
	}
	for _, b := range beads {
		if _, err := db.GetBead(b.ID); err != nil {
			t.Errorf("GetBead(%s) after Reindex: %v", b.ID, err)
		}
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	aCopy := append([]string(nil), a...)
	bCopy := append([]string(nil), b...)
	sort.Strings(aCopy)
	sort.Strings(bCopy)
	for i := range aCopy {
		if aCopy[i] != bCopy[i] {
			return false
		}
	}
	return true
}
