package index

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/medbeads/medbeads/internal/engine/bead"
	"github.com/medbeads/medbeads/internal/engine/pod"
)

// batchSize bounds how many Beads Reindex/CatchUp commit per transaction.
// Committing per-Pod would work too (Pods are bounded per DESIGN §3, ~300-
// 500KB compressed), but batching within very large Pods (e.g. the shared
// Pod, which has no per-patient size ceiling) keeps memory and transaction
// size bounded rather than assuming every Pod is small.
const batchSize = 500

// Reindex rebuilds dbPath from scratch by scanning every Pod file under
// dataDir's pods/ directory (pod.Store.ListPodFiles) and re-running
// IndexBead for every record found — the R1.4 / R3 "index.db のゼロからの
// 完全再構築" guarantee that index.db is always reconstructable from the
// Pod files (the source of truth). Any existing file at dbPath (and its
// WAL/SHM siblings) is removed first so Reindex always starts from an empty
// schema, never merges with stale rows.
//
// patient_root for each Bead comes from the frame's own Meta.PatientRoot
// (pod.Record.Meta), per specs/DESIGN_v3.md §3 — Reindex does not
// recompute or guess it; the frame meta a prior write recorded is
// authoritative for reconstruction purposes.
//
// f flattens each Bead into search_text/summary; pass DefaultFlattener{} for
// the generic fallback (see flatten.go) until a type-specific Flattener
// exists.
func Reindex(dataDir, dbPath string, f Flattener) (*DB, error) {
	if err := removeDBFiles(dbPath); err != nil {
		return nil, fmt.Errorf("index: reindex: %w", err)
	}

	db, err := Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("index: reindex: open %s: %w", dbPath, err)
	}

	store := pod.NewStore(dataDir)
	paths, err := store.ListPodFiles()
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("index: reindex: list pod files: %w", err)
	}

	for _, path := range paths {
		if err := reindexPod(db, path); err != nil {
			db.Close()
			return nil, fmt.Errorf("index: reindex: %w", err)
		}
	}

	return db, nil
}

// removeDBFiles deletes dbPath and its WAL-mode siblings (-wal, -shm) if
// present, so a re-run of Reindex never merges with a half-deleted previous
// database's leftover WAL frames.
func removeDBFiles(dbPath string) error {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(dbPath + suffix); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s%s: %w", dbPath, suffix, err)
		}
	}
	return nil
}

// reindexPod scans one Pod file and indexes every record it contains,
// starting from watermark 0 (a full rebuild always starts at the
// beginning) — see catchUpPod for the incremental/resume variant this
// shares its core loop with.
func reindexPod(db *DB, path string) error {
	return indexPodFrom(db, path, 0)
}

// CatchUp advances the index for a single Pod from its current
// pods.indexed_upto watermark to the Pod's actual end, indexing every frame
// appended since the watermark was last advanced. This is the R1.3 crash-
// recovery path: a Pod append that fsynced but crashed before its
// IndexBead transaction committed leaves the Pod ahead of the index;
// CatchUp resumes cleanly at the byte offset indexed_upto marks, without
// re-indexing (and so without duplicating rows for) anything already
// covered — INSERT OR IGNORE on edges/antigens, plus IndexBead's beads
// INSERT ... ON CONFLICT (id) DO NOTHING (see write.go), make replaying an
// already-indexed record from a slightly-stale watermark safe. This also
// covers the sharper case of two *distinct frames* for the same Bead ID
// existing in one Pod (a retried Ingest can append a duplicate frame after
// a crash before the first frame's IndexBead ever committed, per Ingest's
// own doc comment): CatchUp/Reindex scan every frame in file order and feed
// each to IndexBead in turn, so the first frame's row wins the ON CONFLICT
// arm and the later duplicate frame is recognized as already-indexed rather
// than erroring the whole Pod's recovery. CatchUp still starts precisely at
// the watermark (rather than always rescanning from offset 0) to avoid
// needless duplicate-row conflict machinery on the common, non-crashed
// path, where it isn't needed.
//
// podPath must already have a pods row (i.e. it has been indexed at least
// once, even if only via RegisterPod) — CatchUp on a never-seen Pod path
// registers it fresh, equivalent to indexing from offset 0.
func CatchUp(db *DB, podPath string) error {
	watermark, err := db.PodWatermark(podPath)
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			return fmt.Errorf("index: catch up %s: %w", podPath, err)
		}
		watermark = 0
	}
	if err := indexPodFrom(db, podPath, watermark); err != nil {
		return fmt.Errorf("index: catch up %s: %w", podPath, err)
	}
	return nil
}

// indexPodFrom scans podPath in full, then indexes every record whose
// Offset is >= fromOffset, in batches of batchSize Beads per transaction.
// Scanning the whole Pod even when fromOffset > 0 costs an extra sequential
// read of the already-indexed prefix, but keeps this function's logic (and
// pod.Scan's frame-boundary handling) in one place rather than
// reimplementing "seek to a frame boundary" outside the pod package; per
// DESIGN §3 a single patient Pod is small (~300-500KB compressed), so a full
// re-scan is cheap even when only the tail is new.
func indexPodFrom(db *DB, path string, fromOffset int64) error {
	scan, err := pod.Scan(path, true)
	if err != nil {
		return fmt.Errorf("scan %s: %w", path, err)
	}
	if scan.Damaged {
		return fmt.Errorf("scan %s: damaged at offset %d: %w", path, scan.ValidUpto, scan.DamageErr)
	}

	var pending []pod.Record
	for _, rec := range scan.Records {
		if rec.Offset < fromOffset {
			continue
		}
		pending = append(pending, rec)
		if len(pending) >= batchSize {
			if err := indexBatch(db, path, pending); err != nil {
				return fmt.Errorf("index %s: %w", path, err)
			}
			pending = pending[:0]
		}
	}
	if len(pending) > 0 {
		if err := indexBatch(db, path, pending); err != nil {
			return fmt.Errorf("index %s: %w", path, err)
		}
	}
	return nil
}

// indexBatch indexes a batch of already-scanned Records from the same Pod
// file in one transaction: decode each frame's Bead JSON, run IndexBead, and
// commit once. A decode/index failure aborts and rolls back the whole batch
// (partial application of a batch would leave the watermark ambiguous).
func indexBatch(db *DB, podPath string, records []pod.Record) error {
	tx, err := db.sqlDB.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after commit is a no-op

	f := DefaultFlattener{}
	for _, rec := range records {
		b, err := decodeRecordBead(rec)
		if err != nil {
			return fmt.Errorf("decode record at offset %d: %w", rec.Offset, err)
		}
		loc := BeadLocation{
			PodPath:     podPath,
			PatientRoot: rec.Meta.PatientRoot,
			Offset:      rec.Offset,
			Length:      rec.Length,
		}
		if err := IndexBead(tx, b, loc, f); err != nil {
			return fmt.Errorf("index bead at offset %d: %w", rec.Offset, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// decodeRecordBead decompresses rec's core_bytes (the JCS canonical Bead
// JSON) and unmarshals it into a bead.Bead, setting ID from rec.BeadID
// (core_bytes itself has no "id" field — see bead.Canonicalize's
// hash-target payload, which excludes id).
func decodeRecordBead(rec pod.Record) (bead.Bead, error) {
	plain, err := rec.Decompress()
	if err != nil {
		return bead.Bead{}, fmt.Errorf("decompress: %w", err)
	}
	var b bead.Bead
	if err := json.Unmarshal(plain, &b); err != nil {
		return bead.Bead{}, fmt.Errorf("unmarshal bead JSON: %w", err)
	}
	b.ID = rec.BeadID
	return b, nil
}

// SQLDB exposes the underlying *sql.DB for callers (e.g. a future store
// layer) that need transaction control IndexBead's tx-scoped API requires
// but this package's higher-level methods (GetBead, Search, ...) do not
// expose. It is a narrow escape hatch, not an invitation to bypass this
// package's schema knowledge — prefer the typed methods above wherever they
// suffice.
func (d *DB) SQLDB() *sql.DB {
	return d.sqlDB
}
