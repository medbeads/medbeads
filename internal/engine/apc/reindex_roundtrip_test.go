package apc_test

import (
	"path/filepath"
	"testing"

	"github.com/medbeads/medbeads/internal/engine"
	"github.com/medbeads/medbeads/internal/engine/apc"
	"github.com/medbeads/medbeads/internal/engine/index"
)

// TestReindex_RebuildsSiblingEdgesAndPairsFromPodAlone is the formalized
// regression test for the reviewer-found bug: sibling_pairs rows and the
// bidirectional edge_type='sibling' bead_edges rows a Scan call produces
// must survive a full index.db Reindex (dataDir's Pod files are the only
// input Reindex reads — specs/DESIGN_v3.md §1's "インデックスは正本から完全再
// 構築可能" invariant), with no second Scan() call needed to restore them.
//
// Sequence: Scan (creates a sibling_link, its sibling edges, its
// sibling_pairs rows) -> close Engine -> index.Reindex the same data
// directory from scratch -> reopen an Engine on it -> assert the sibling
// edges/pairs are already present (not merely re-creatable by scanning
// again) -> Scan once more and assert it is a true no-op (no duplicate
// sibling_link, no duplicate sibling_pairs rows).
func TestReindex_RebuildsSiblingEdgesAndPairsFromPodAlone(t *testing.T) {
	dataDir := t.TempDir()

	e, err := engine.Open(dataDir)
	if err != nil {
		t.Fatalf("engine.Open: %v", err)
	}

	root := seedPatient(t, e, "patient A")
	padWithNoiseBeads(t, e, root, 10) // keep risk:nephrotoxic/organ:renal under the 30% IDF threshold
	rx := seedChildBead(t, e, root, "fhir_medicationrequest",
		[]string{"risk:nephrotoxic", "organ:renal"}, map[string]any{"drug": "meropenem"})
	lab := seedChildBead(t, e, root, "fhir_observation",
		[]string{"risk:nephrotoxic", "organ:renal"}, map[string]any{"test": "eGFR"})

	scanner := apc.New(e, e.Index(), apc.Default())
	first, err := scanner.Scan()
	if err != nil {
		t.Fatalf("first Scan: %v", err)
	}
	if first.SiblingLinksCreated != 1 {
		t.Fatalf("first Scan created %d links, want 1", first.SiblingLinksCreated)
	}

	var linkID string
	if err := e.Index().SQLDB().QueryRow(
		`SELECT id FROM beads WHERE type = 'sibling_link'`,
	).Scan(&linkID); err != nil {
		t.Fatalf("query sibling_link id: %v", err)
	}

	linksBefore := countRows(t, e.Index(), `SELECT COUNT(*) FROM beads WHERE type = 'sibling_link'`)
	pairsBefore := countRows(t, e.Index(), `SELECT COUNT(*) FROM sibling_pairs`)
	edgesBefore := countRows(t, e.Index(), `SELECT COUNT(*) FROM bead_edges WHERE edge_type = 'sibling'`)
	if pairsBefore == 0 {
		t.Fatal("sibling_pairs empty before Reindex — test setup did not actually create a link")
	}
	if edgesBefore == 0 {
		t.Fatal("sibling bead_edges empty before Reindex — test setup did not actually create a link")
	}

	// Close the Engine (releases the data-dir flock — see lock.go) before
	// Reindex opens its own *index.DB against the same dbPath: Engine and
	// index.Reindex must not hold two simultaneous handles onto the data
	// directory's lock file, though Reindex itself only needs the Pod files
	// and a fresh index.db path, not the lock.
	if err := e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	dbPath := filepath.Join(dataDir, "index.db")
	rebuilt, err := index.Reindex(dataDir, dbPath, index.DefaultFlattener{})
	if err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	defer rebuilt.Close()

	// --- assertions against the reindexed DB, before any Scan() call ---

	linksAfterReindex := countRows(t, rebuilt, `SELECT COUNT(*) FROM beads WHERE type = 'sibling_link'`)
	if linksAfterReindex != linksBefore {
		t.Errorf("sibling_link beads after Reindex = %d, want %d (unchanged — Reindex must not duplicate Beads)", linksAfterReindex, linksBefore)
	}

	pairsAfterReindex := countRows(t, rebuilt, `SELECT COUNT(*) FROM sibling_pairs`)
	if pairsAfterReindex != pairsBefore {
		t.Errorf("sibling_pairs after Reindex (no Scan yet) = %d, want %d — sibling_pairs must be re-derived from the sibling_link Bead's own content by IndexBead itself, not lost until the next Scan", pairsAfterReindex, pairsBefore)
	}

	edgesAfterReindex := countRows(t, rebuilt, `SELECT COUNT(*) FROM bead_edges WHERE edge_type = 'sibling'`)
	if edgesAfterReindex != edgesBefore {
		t.Errorf("sibling bead_edges after Reindex (no Scan yet) = %d, want %d — sibling edges must be re-derived from the sibling_link Bead's own Parents by IndexBead itself, not lost until the next Scan", edgesAfterReindex, edgesBefore)
	}

	// The specific rx<->lab sibling edge (not just a nonzero count) must be
	// present in both directions.
	if n := countRows(t, rebuilt,
		`SELECT COUNT(*) FROM bead_edges WHERE child_id = ? AND parent_id = ? AND edge_type = 'sibling'`,
		rx.ID, lab.ID); n != 1 {
		t.Errorf("sibling edge rx->lab after Reindex = %d, want 1", n)
	}
	if n := countRows(t, rebuilt,
		`SELECT COUNT(*) FROM bead_edges WHERE child_id = ? AND parent_id = ? AND edge_type = 'sibling'`,
		lab.ID, rx.ID); n != 1 {
		t.Errorf("sibling edge lab->rx after Reindex = %d, want 1", n)
	}

	// The specific sibling_pairs rows must reference the original
	// sibling_link_id (not a Reindex-invented placeholder).
	pairA, pairB := rx.ID, lab.ID
	if pairB < pairA {
		pairA, pairB = pairB, pairA
	}
	rows, err := rebuilt.SQLDB().Query(
		`SELECT matched_antigen, sibling_link_id FROM sibling_pairs WHERE bead_a = ? AND bead_b = ?`,
		pairA, pairB)
	if err != nil {
		t.Fatalf("query sibling_pairs: %v", err)
	}
	gotPairAntigens := map[string]bool{}
	for rows.Next() {
		var antigen, siblingLinkID string
		if err := rows.Scan(&antigen, &siblingLinkID); err != nil {
			rows.Close()
			t.Fatalf("scan: %v", err)
		}
		gotPairAntigens[antigen] = true
		if siblingLinkID != linkID {
			t.Errorf("sibling_pairs row for antigen %s has sibling_link_id=%s, want %s", antigen, siblingLinkID, linkID)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatalf("rows: %v", err)
	}
	rows.Close()
	for _, want := range []string{"risk:nephrotoxic", "organ:renal"} {
		if !gotPairAntigens[want] {
			t.Errorf("sibling_pairs missing matched_antigen %q after Reindex, got %v", want, gotPairAntigens)
		}
	}

	// --- re-Scan against the reindexed DB: the original rx<->lab pair must
	// --- not be re-linked or duplicated ---
	//
	// Reindex does not (and must not) populate bead_apc_scan — that table
	// is scan *history*, not reconstructable structural state, and is
	// deliberately out of Reindex's "rebuild from the Pod alone" scope (see
	// write.go's indexSiblingLink doc comment: only sibling_pairs/bead_edges
	// are re-derived, not bead_apc_scan). So every Bead in the patient,
	// including the pre-existing sibling_link Bead itself, is a legitimate
	// "new" anchor to the reopened Scanner — settleT below is expected to
	// examine all of them and MAY create new sibling_link Beads for
	// genuinely new pairs this history never recorded before (e.g.
	// rx<->sibling_link, lab<->sibling_link — the sibling_link Bead matching
	// its own parents on the antigens it copied from them, a legitimate
	// generation-1 pair distinct from the original rx<->lab pair). What must
	// NOT happen is the *original* rx<->lab pair being re-linked or
	// double-recorded: sibling_pairs' UNIQUE constraint plus
	// tryLink/unlinkedAntigens' pre-check (both already covered by
	// TestRunawayPrevention_A) must still hold across this Reindex + re-Scan
	// round trip, exactly as they hold across two ordinary Scan calls.
	//
	// This also exercises the IDF-self-contamination fix (frequentAntigens /
	// candidateRows excluding type='sibling_link' Beads): before that fix,
	// the sibling_link Bead's own copied antigens (risk:nephrotoxic,
	// organ:renal) would inflate their patient-local frequency once the
	// sibling_link Bead itself became indexed, pushing them over the 30%
	// default threshold and silently filtering out legitimate matches
	// (including the assertions above already having exercised the exact
	// scenario that regression required: a sibling_link Bead present in
	// bead_tags before any Scan of it has run).
	rescanEngine, err := engine.Open(dataDir)
	if err != nil {
		t.Fatalf("reopen engine.Open: %v", err)
	}
	defer rescanEngine.Close()

	rescanScanner := apc.New(rescanEngine, rescanEngine.Index(), apc.Default())
	settleT(t, rescanScanner)

	rxLabPairsAfter := countRows(t, rescanEngine.Index(),
		`SELECT COUNT(*) FROM sibling_pairs WHERE bead_a = ? AND bead_b = ?`, pairA, pairB)
	if rxLabPairsAfter != pairsBefore {
		t.Errorf("sibling_pairs rows for the original rx/lab pair after post-Reindex re-Scan = %d, want %d (unchanged — no re-link, no duplicate)", rxLabPairsAfter, pairsBefore)
	}
	rxLabPairsWithOriginalLink := countRows(t, rescanEngine.Index(),
		`SELECT COUNT(*) FROM sibling_pairs WHERE bead_a = ? AND bead_b = ? AND sibling_link_id = ?`,
		pairA, pairB, linkID)
	if rxLabPairsWithOriginalLink != pairsBefore {
		t.Errorf("original rx/lab sibling_pairs rows still pointing at the original sibling_link_id after post-Reindex re-Scan = %d, want %d (no replacement link)", rxLabPairsWithOriginalLink, pairsBefore)
	}
}
