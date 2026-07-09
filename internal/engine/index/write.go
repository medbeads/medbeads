package index

import (
	"database/sql"
	"fmt"

	"github.com/medbeads/medbeads/internal/engine/bead"
)

// BeadLocation is where one Bead's frame lives within its Pod file: the
// (offset, length) pair pod.Writer.Append returns, plus the patient_root it
// was written under. IndexBead's caller (store/ingest layer, or Reindex in
// this package) supplies this after appending to the Pod — index.db never
// computes offsets itself.
type BeadLocation struct {
	PodPath     string
	PatientRoot string // "" for the shared Pod
	Offset      int64
	Length      int64
}

// RegisterPod ensures a pods row exists for podPath, returning its pod_id.
// It is idempotent: calling it again for the same path returns the existing
// row's pod_id rather than erroring or duplicating it (Reindex and CatchUp
// both need to "get or create" a Pod's row on every run).
func RegisterPod(tx *sql.Tx, podPath string, patientRoot string) (int64, error) {
	var podID int64
	err := tx.QueryRow(`SELECT pod_id FROM pods WHERE path = ?`, podPath).Scan(&podID)
	switch {
	case err == nil:
		return podID, nil
	case err != sql.ErrNoRows:
		return 0, fmt.Errorf("index: register pod %s: %w", podPath, err)
	}

	var root any
	if patientRoot != "" {
		root = patientRoot
	}
	res, err := tx.Exec(
		`INSERT INTO pods (path, patient_root, size, indexed_upto) VALUES (?, ?, 0, 0)`,
		podPath, root,
	)
	if err != nil {
		return 0, fmt.Errorf("index: register pod %s: %w", podPath, err)
	}
	podID, err = res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("index: register pod %s: last insert id: %w", podPath, err)
	}
	return podID, nil
}

// IndexBead records one Bead into index.db within a single transaction:
// beads (id, patient_root, type, timestamp, pod_id, offset, length,
// summary), bead_edges (one row per parent), bead_antigens (one row per
// antigen), and beads_fts (search_text) — per specs/DESIGN_v3.md §5 / R3.1.
// It also advances the owning pod's indexed_upto watermark to loc.Offset +
// loc.Length (R1.3), so a caller that indexes every Append in order keeps
// the watermark correct without a separate step.
//
// f flattens b into search_text/summary (see Flattener); pass
// DefaultFlattener{} if the caller has no type-specific flattening yet.
//
// IndexBead does not itself open a transaction — callers (store/ingest,
// Reindex, CatchUp) own transaction boundaries, since a single logical write
// may span multiple Beads (e.g. Reindex batches per Pod).
//
// # Duplicate-frame idempotency
//
// A Pod can legitimately contain two frames for the same Bead ID: a crash
// between "Pod append + fsync" and "IndexBead commit" (R1.3) leaves the Pod
// ahead of the index; if the caller retries the same Ingest, Ingest's own
// duplicate check (querying the index) sees nothing yet indexed and appends
// a *second* frame for the identical content-addressed Bead before the
// crashed attempt's frame is ever indexed. CatchUp/Reindex must still be
// able to index that Pod: beads.id is a stable, content-derived primary
// key, so the second frame is not new information, only a redundant copy of
// bytes already accounted for. The beads INSERT below is therefore
// `ON CONFLICT (id) DO NOTHING`, not a plain INSERT: on conflict, the
// *first*-written frame's row (whichever one is already indexed) wins, and
// this call resolves its existing rowid instead of failing. This is a
// deliberate, append-only-respecting choice — the surviving offset/length
// always point at the earliest frame in file order, and any subsequent
// duplicate frame becomes permanently unreachable dead bytes in the Pod
// (never reclaimed, per this format's no-compaction design), not an error.
func IndexBead(tx *sql.Tx, b bead.Bead, loc BeadLocation, f Flattener) error {
	if b.ID == "" {
		return fmt.Errorf("index: index bead: bead has no ID")
	}
	podID, err := RegisterPod(tx, loc.PodPath, loc.PatientRoot)
	if err != nil {
		return fmt.Errorf("index: index bead %s: %w", b.ID, err)
	}

	var root any
	if loc.PatientRoot != "" {
		root = loc.PatientRoot
	}

	searchText, summary := f.Flatten(b)

	res, err := tx.Exec(
		`INSERT INTO beads (id, patient_root, type, timestamp, pod_id, offset, length, summary)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (id) DO NOTHING`,
		b.ID, root, b.Type, b.Timestamp, podID, loc.Offset, loc.Length, summary,
	)
	if err != nil {
		return fmt.Errorf("index: index bead %s: insert beads: %w", b.ID, err)
	}
	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("index: index bead %s: rows affected: %w", b.ID, err)
	}

	// beadRowID is beads' own implicit SQLite rowid (beads.id is a TEXT
	// PRIMARY KEY, so it is not a rowid alias) — used below to key
	// beads_fts, a contentless FTS5 table that can only be joined back to
	// beads via rowid (see migrations/0001_init.sql).
	var beadRowID int64
	if rowsAffected == 1 {
		// The common case: this call actually inserted the row, so
		// LastInsertId reflects it directly.
		beadRowID, err = res.LastInsertId()
		if err != nil {
			return fmt.Errorf("index: index bead %s: last insert id: %w", b.ID, err)
		}
	} else {
		// ON CONFLICT DO NOTHING: rowsAffected is 0 and LastInsertId is
		// meaningless here (SQLite does not report it for a no-op upsert
		// arm) — this is a duplicate frame for an already-indexed Bead ID.
		// Look up the surviving row's rowid (belonging to whichever frame
		// was indexed first, per the doc comment above) instead of
		// treating this as new data.
		if err := tx.QueryRow(`SELECT rowid FROM beads WHERE id = ?`, b.ID).Scan(&beadRowID); err != nil {
			return fmt.Errorf("index: index bead %s: resolve existing rowid: %w", b.ID, err)
		}
	}

	normalized := bead.Normalize(b)

	for _, parent := range normalized.Parents {
		if _, err := tx.Exec(
			`INSERT OR IGNORE INTO bead_edges (child_id, parent_id, edge_type) VALUES (?, ?, 'parent')`,
			b.ID, parent,
		); err != nil {
			return fmt.Errorf("index: index bead %s: insert edge to parent %s: %w", b.ID, parent, err)
		}
	}

	for _, antigen := range normalized.Antigens {
		if _, err := tx.Exec(
			`INSERT OR IGNORE INTO bead_antigens (antigen, bead_id, patient_root) VALUES (?, ?, ?)`,
			antigen, b.ID, root,
		); err != nil {
			return fmt.Errorf("index: index bead %s: insert antigen %s: %w", b.ID, antigen, err)
		}
	}

	// beads_fts is contentless and keyed on beads.rowid (see migrations/
	// 0001_init.sql): only insert an FTS row the first time this Bead ID is
	// actually indexed. On a duplicate-frame replay (rowsAffected == 0
	// above), a row already exists at beadRowID — inserting again would
	// violate FTS5's own implicit rowid uniqueness and double-count the
	// Bead in search results.
	if rowsAffected == 1 {
		if _, err := tx.Exec(
			`INSERT INTO beads_fts (rowid, search_text) VALUES (?, ?)`,
			beadRowID, searchText,
		); err != nil {
			return fmt.Errorf("index: index bead %s: insert fts: %w", b.ID, err)
		}
	}

	if err := advanceWatermark(tx, podID, loc.Offset+loc.Length); err != nil {
		return fmt.Errorf("index: index bead %s: %w", b.ID, err)
	}

	return nil
}

// advanceWatermark sets pods.indexed_upto for podID to upto, but only if
// upto is greater than the current value — a no-op guard so CatchUp
// replaying from an old watermark can never move indexed_upto backwards.
func advanceWatermark(tx *sql.Tx, podID int64, upto int64) error {
	if _, err := tx.Exec(
		`UPDATE pods SET indexed_upto = ? WHERE pod_id = ? AND indexed_upto < ?`,
		upto, podID, upto,
	); err != nil {
		return fmt.Errorf("advance watermark: %w", err)
	}
	return nil
}
