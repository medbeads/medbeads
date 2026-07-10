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

	// sibling_link Beads carry a horizontal relationship (specs/
	// MEDBEADS_SIBLING_SPEC.md §5) that must be re-derivable from the Pod
	// alone, per specs/DESIGN_v3.md §1's "インデックスは正本から完全再構築可能"
	// invariant: unlike a 'parent' edge (derived above from every Bead's own
	// Parents field, regardless of type), the bidirectional 'sibling'
	// bead_edges rows and the sibling_pairs de-duplication record are
	// specific to this one Bead type and were previously written only by
	// package apc's Scanner at scan time — meaning a Reindex/CatchUp replay
	// of an already-ingested sibling_link Bead silently lost both, since
	// neither Ingest nor a bare replay re-runs Scan. Deriving them here
	// instead, unconditionally whenever a sibling_link Bead is (re-)indexed,
	// makes both reconstructable from the Bead's own content the same way
	// beads/bead_edges('parent')/bead_antigens/beads_fts already are, and
	// makes them idempotent under replay via the same INSERT OR IGNORE
	// pattern used above.
	if b.Type == "sibling_link" {
		if err := indexSiblingLink(tx, normalized); err != nil {
			return fmt.Errorf("index: index bead %s: %w", b.ID, err)
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
	}

	if err := advanceWatermark(tx, podID, loc.Offset+loc.Length); err != nil {
		return fmt.Errorf("index: index bead %s: %w", b.ID, err)
	}

	return nil
}

// indexSiblingLink derives and records the two pieces of index.db state a
// sibling_link Bead implies but does not itself carry as ordinary
// parent/antigen edges (see IndexBead's sibling_link doc comment):
//
//  1. Bidirectional edge_type='sibling' bead_edges rows between the Bead's
//     two parents (specs/MEDBEADS_SIBLING_SPEC.md §5.3: "(A, B, sibling)"
//     and "(B, A, sibling)"). package apc's Scanner independently records
//     the same rows at scan time (recordSiblingEdges) via INSERT OR IGNORE;
//     doing it again here is therefore a no-op on the normal ingest path and
//     only actually adds rows on a Reindex/CatchUp replay where they would
//     otherwise be missing.
//  2. sibling_pairs rows, one per antigen in the Bead's own
//     content.matched_antigens, for the same normalized (bead_a < bead_b)
//     pair — recorded the same way package apc's Scanner.recordPair does,
//     again idempotent via INSERT OR IGNORE against the table's
//     UNIQUE(bead_a, bead_b, matched_antigen) constraint.
//
// b must already be normalized (Parents deduplicated + sorted — see
// bead.Normalize) and must already have a beads row (IndexBead calls this
// after its own beads INSERT). A sibling_link Bead is expected to have
// exactly two Parents (specs/MEDBEADS_SIBLING_SPEC.md §4.1: "関連付けされる
// Beadのハッシュ ID（2つ以上）" — this scanner's own sibling_link Beads are
// always exactly pairwise, per apc/link.go's buildSiblingLinkBead); a
// malformed sibling_link with a different Parents count, or whose
// content.matched_antigens is missing/malformed, is skipped rather than
// erroring — a hand-crafted or future agent-authored sibling_link Bead that
// does not exactly match this scanner's own shape must not abort an entire
// Reindex run over one Bead's unusual content.
func indexSiblingLink(tx *sql.Tx, b bead.Bead) error {
	if len(b.Parents) != 2 {
		return nil
	}
	pairA, pairB := b.Parents[0], b.Parents[1]
	if pairB < pairA {
		pairA, pairB = pairB, pairA
	}

	for _, pair := range [][2]string{{pairA, pairB}, {pairB, pairA}} {
		if _, err := tx.Exec(
			`INSERT OR IGNORE INTO bead_edges (child_id, parent_id, edge_type) VALUES (?, ?, 'sibling')`,
			pair[0], pair[1],
		); err != nil {
			return fmt.Errorf("index sibling_link %s: sibling edge %s<->%s: %w", b.ID, pairA, pairB, err)
		}
	}

	antigens, ok := siblingLinkMatchedAntigens(b)
	if !ok {
		return nil
	}
	for _, antigen := range antigens {
		if _, err := tx.Exec(
			`INSERT OR IGNORE INTO sibling_pairs (bead_a, bead_b, matched_antigen, sibling_link_id, created_at)
			 VALUES (?, ?, ?, ?, ?)`,
			pairA, pairB, antigen, b.ID, b.Timestamp,
		); err != nil {
			return fmt.Errorf("index sibling_link %s: sibling pair %s/%s/%s: %w", b.ID, pairA, pairB, antigen, err)
		}
	}
	return nil
}

// siblingLinkMatchedAntigens extracts b.Content["matched_antigens"] as a
// []string, tolerating the shape JSON round-tripping produces: a freshly
// built bead.Bead (apc/link.go's buildSiblingLinkBead, before Ingest) has a
// real []string there, but a Bead decoded back from a Pod frame (Reindex/
// CatchUp's path — decodeRecordBead unmarshals into map[string]any content)
// has a []any of string elements instead. ok is false (not an error) for
// any shape that isn't one of those two — see indexSiblingLink's doc
// comment on why a malformed sibling_link Bead is skipped, not fatal.
func siblingLinkMatchedAntigens(b bead.Bead) (antigens []string, ok bool) {
	raw, exists := b.Content["matched_antigens"]
	if !exists {
		return nil, false
	}
	switch v := raw.(type) {
	case []string:
		return v, true
	case []any:
		out := make([]string, 0, len(v))
		for _, elem := range v {
			s, isString := elem.(string)
			if !isString {
				return nil, false
			}
			out = append(out, s)
		}
		return out, true
	default:
		return nil, false
	}
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
