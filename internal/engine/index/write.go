package index

import (
	"database/sql"
	"fmt"

	"github.com/medbeads/medbeads/internal/engine/antigen"
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
	// WrittenAt is the Pod frame's own meta.WrittenAt (RFC3339Nano, the wall-
	// clock instant the frame was appended — see pod.Meta's doc comment), or
	// "" if the caller has none to supply. IndexBead projects it into
	// beads.recorded_at (specs/U2_projection_schema.md's crux 1 / U3a):
	// unlike beads.timestamp (the clinical event time), recorded_at is the
	// write instant, which is what the correction-chain resolution in
	// specs/DESIGN_v3.1_draft.md §2 needs ("most recently written wins").
	WrittenAt string
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
// summary, recorded_at), bead_edges (one row per parent), bead_tags (one row
// per tag), and beads_fts (search_text) — per specs/DESIGN_v3.md §5 / R3.1,
// specs/U2_projection_schema.md / U3a (bead_tags is bead_antigens' successor;
// recorded_at is Pod meta WrittenAt). It also advances the owning pod's
// indexed_upto watermark to loc.Offset + loc.Length (R1.3), so a caller that
// indexes every Append in order keeps the watermark correct without a
// separate step.
//
// # Tag (antigen) extraction happens here, not at ingest (v3.1)
//
// specs/DESIGN_v3.1_draft.md §0/§2 moves antigen/tag derivation entirely
// into the index projection: Bead no longer carries an Antigens field (it
// was removed from the hash-target payload — see bead.Bead's doc comment),
// so IndexBead is now the single place antigen.Extract(b.Type, b.Content)
// ever runs. This makes tag derivation "投影時に決定論実行": the same
// (Type, Content) always yields the same bead_tags rows regardless of
// when or how many times a Bead is (re-)indexed (Reindex/CatchUp included),
// and re-tagging (a future dictionary update) is purely a projection
// rebuild — it never touches a Bead's own content hash.
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
	var recordedAt any
	if loc.WrittenAt != "" {
		recordedAt = loc.WrittenAt
	}

	searchText, summary := f.Flatten(b)

	res, err := tx.Exec(
		`INSERT INTO beads (id, patient_root, type, timestamp, pod_id, offset, length, summary, recorded_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (id) DO NOTHING`,
		b.ID, root, b.Type, b.Timestamp, podID, loc.Offset, loc.Length, summary, recordedAt,
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

	for _, tag := range extractTags(normalized) {
		if _, err := tx.Exec(
			`INSERT OR IGNORE INTO bead_tags (tag, bead_id, patient_root) VALUES (?, ?, ?)`,
			tag, b.ID, root,
		); err != nil {
			return fmt.Errorf("index: index bead %s: insert tag %s: %w", b.ID, tag, err)
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

		// L2 semantic search (R4.2, specs/DESIGN_v3.md §6): every newly-
		// indexed Bead is enqueued for asynchronous embedding regardless of
		// whether an embedder is configured for this process at all — the
		// queue itself is cheap (an id/root/text row), and it is
		// StartEmbedIndexer's caller (cmd/medbeadsd's `serve -embedder ...`)
		// that decides whether anything ever drains it. This keeps Ingest's
		// write path fully decoupled from embedding-server availability: an
		// embedder being down, slow, or simply never configured never blocks
		// or fails an Ingest call, only leaves rows queued (see
		// EnqueueEmbed's own doc comment for the ON CONFLICT replace-on-
		// reindex behavior).
		if err := EnqueueEmbed(tx, b.ID, loc.PatientRoot, searchText); err != nil {
			return fmt.Errorf("index: index bead %s: %w", b.ID, err)
		}

		// Activity is a scheduling projection, not clinical state. Keeping it
		// beside the primary index write lets a future knowledge-generation
		// rollout prioritize recent patients without opening every Pod. A
		// duplicate frame is deliberately ignored: replaying identical content
		// is not new patient activity.
		if err := upsertPatientActivity(tx, normalized, loc); err != nil {
			return fmt.Errorf("index: index bead %s: patient activity: %w", b.ID, err)
		}
	}

	if err := advanceWatermark(tx, podID, loc.Offset+loc.Length); err != nil {
		return fmt.Errorf("index: index bead %s: %w", b.ID, err)
	}

	return nil
}

func upsertPatientActivity(tx *sql.Tx, b bead.Bead, loc BeadLocation) error {
	if loc.PatientRoot == "" {
		return nil
	}

	var clinicalAt any
	if b.Type != "patient_registration" && b.Timestamp != "" {
		clinicalAt = b.Timestamp
	}
	var encounterAt any
	if (b.Type == "fhir_encounter" || b.Type == "encounter") && b.Timestamp != "" {
		encounterAt = b.Timestamp
	}
	var visitAt any
	if b.Type != "patient_registration" && b.Timestamp != "" {
		visitAt = b.Timestamp
	}

	deceased := 0
	var deceasedAt any
	if b.Type == "patient_registration" {
		if value, ok := b.Content["deceasedBoolean"].(bool); ok && value {
			deceased = 1
		}
		if value, ok := b.Content["deceasedDateTime"].(string); ok && value != "" {
			deceased = 1
			deceasedAt = value
		}
	}

	var recordedAt any
	if loc.WrittenAt != "" {
		recordedAt = loc.WrittenAt
	}
	updatedAt := loc.WrittenAt
	if updatedAt == "" {
		updatedAt = b.Timestamp
	}
	if updatedAt == "" {
		updatedAt = "1970-01-01T00:00:00Z"
	}

	_, err := tx.Exec(`
		INSERT INTO patient_activity
			(patient_root, last_recorded_at, last_clinical_at,
			 last_encounter_at, deceased_hint, deceased_at, updated_at, last_visit_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(patient_root) DO UPDATE SET
			last_recorded_at = CASE
				WHEN excluded.last_recorded_at IS NULL THEN patient_activity.last_recorded_at
				WHEN patient_activity.last_recorded_at IS NULL OR excluded.last_recorded_at > patient_activity.last_recorded_at
				THEN excluded.last_recorded_at ELSE patient_activity.last_recorded_at END,
			last_clinical_at = CASE
				WHEN excluded.last_clinical_at IS NULL THEN patient_activity.last_clinical_at
				WHEN patient_activity.last_clinical_at IS NULL OR excluded.last_clinical_at > patient_activity.last_clinical_at
				THEN excluded.last_clinical_at ELSE patient_activity.last_clinical_at END,
			last_encounter_at = CASE
				WHEN excluded.last_encounter_at IS NULL THEN patient_activity.last_encounter_at
				WHEN patient_activity.last_encounter_at IS NULL OR excluded.last_encounter_at > patient_activity.last_encounter_at
				THEN excluded.last_encounter_at ELSE patient_activity.last_encounter_at END,
			deceased_hint = MAX(patient_activity.deceased_hint, excluded.deceased_hint),
			deceased_at = COALESCE(excluded.deceased_at, patient_activity.deceased_at),
			last_visit_at = CASE
				WHEN excluded.last_visit_at IS NULL THEN patient_activity.last_visit_at
				WHEN patient_activity.last_visit_at IS NULL OR excluded.last_visit_at > patient_activity.last_visit_at
				THEN excluded.last_visit_at ELSE patient_activity.last_visit_at END,
			updated_at = CASE WHEN excluded.updated_at > patient_activity.updated_at
				THEN excluded.updated_at ELSE patient_activity.updated_at END`,
		loc.PatientRoot, recordedAt, clinicalAt, encounterAt, deceased, deceasedAt, updatedAt, visitAt,
	)
	return err
}

// extractTags returns the bead_tags tags b's projection should carry: exactly
// antigen.Extract(b.Type, b.Content), the deterministic FHIR-coding +
// dictionary derivation (see IndexBead's "Tag extraction happens here" doc
// comment). Before U5a, this had a narrow special case for type=
// "sibling_link" Beads (the old package apc scanner's output) reading
// content.matched_antigens directly; U5a deleted package apc (the sole
// producer of that Bead type) along with write.go's sibling_link handling
// entirely, so extractTags is now this one deterministic call for every Bead
// type.
func extractTags(b bead.Bead) []string {
	return antigen.Extract(b.Type, b.Content)
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
