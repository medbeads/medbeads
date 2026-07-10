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
// pods.path is stored dataDir-relative (Store.RelPath), not as whatever
// literal path string dataDir happened to be passed in as, so a data
// directory reindexed from a relative -data flag (e.g. `-data ./foo`) still
// opens correctly from a process with a different working directory later
// (see this task's pods.path portability fix; engine.GetBead/ListPatientBeads
// resolve pods.path back via Store.AbsPath before opening a Pod file).
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
		if err := reindexPod(db, store, path); err != nil {
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
// beginning) — see CatchUp for the incremental/resume variant this shares
// its core loop with.
func reindexPod(db *DB, store *pod.Store, path string) error {
	return indexPodFrom(db, store, path, 0)
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
// registers it fresh, equivalent to indexing from offset 0. store must be
// rooted at the same data directory podPath lives under: CatchUp looks up
// and stores pods.path in store.RelPath's dataDir-relative form (see
// Reindex's doc comment on why), so PodWatermark's lookup and this call's
// eventual RegisterPod/IndexBead writes stay keyed on the same string form
// pods.path already holds, regardless of what literal path string podPath
// itself is (absolute, or relative to whatever cwd the caller resolved it
// from).
func CatchUp(db *DB, store *pod.Store, podPath string) error {
	relPath, err := store.RelPath(podPath)
	if err != nil {
		return fmt.Errorf("index: catch up %s: %w", podPath, err)
	}
	watermark, err := db.PodWatermark(relPath)
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			return fmt.Errorf("index: catch up %s: %w", podPath, err)
		}
		watermark = 0
	}
	if err := indexPodFrom(db, store, podPath, watermark); err != nil {
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
//
// store (rooted at podPath's data directory) is threaded through to
// indexBatch so every BeadLocation.PodPath it builds is stored dataDir-
// relative, not as podPath's own literal form — see Reindex's doc comment.
func indexPodFrom(db *DB, store *pod.Store, path string, fromOffset int64) error {
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
			if err := indexBatch(db, store, path, pending); err != nil {
				return fmt.Errorf("index %s: %w", path, err)
			}
			pending = pending[:0]
		}
	}
	if len(pending) > 0 {
		if err := indexBatch(db, store, path, pending); err != nil {
			return fmt.Errorf("index %s: %w", path, err)
		}
	}
	return nil
}

// indexBatch indexes a batch of already-scanned Records from the same Pod
// file in one transaction: decode each frame's Bead JSON, run IndexBead, and
// commit once. A decode/index failure aborts and rolls back the whole batch
// (partial application of a batch would leave the watermark ambiguous).
// podPath is converted to store.RelPath's dataDir-relative form before being
// recorded as BeadLocation.PodPath (see Reindex's doc comment).
func indexBatch(db *DB, store *pod.Store, podPath string, records []pod.Record) error {
	relPodPath, err := store.RelPath(podPath)
	if err != nil {
		return err
	}

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
			PodPath:     relPodPath,
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
// hash-target payload, which excludes id) and restoring Clearance/Signature
// from rec.Meta (their designed storage location, since both are excluded
// from core_bytes too — see pod.Meta's doc comment). IndexBead itself does
// not consult Clearance/Signature today (index.db stores no Bead content),
// but decodeRecordBead restores them anyway for consistency with this
// project's other two decode-a-Record-into-a-bead.Bead call sites
// (engine.decodeBeadRecord, graph.decodeBundleRecord) — a bead.Bead value
// handed to IndexBead's Flattener (or any future consumer added here)
// should never look less complete than one read via engine.GetBead purely
// because of which decode path produced it.
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
	b.Clearance = rec.Meta.Clearance
	b.Signature = rec.Meta.Signature
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
