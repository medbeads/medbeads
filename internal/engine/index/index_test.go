package index

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/medbeads/medbeads/internal/engine/bead"
)

// openT opens a fresh index.db under a t.TempDir() and registers a Close
// cleanup, for tests that only need a migrated, empty database.
func openT(t *testing.T) *DB {
	t.Helper()
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "index.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// testBead returns a small, deterministic Bead with a real content-hash ID
// and the given parents, mirroring internal/engine/pod's testBead helper
// (kept package-local rather than shared, since exporting test-only helpers
// across packages is not warranted here).
//
// No antigens parameter: v3.1 removed Bead.Antigens entirely (see
// bead.Bead's doc comment) — bead_tags rows are now produced only by
// IndexBead running antigen.Extract(b.Type, b.Content) at index time.
// Callers that need specific bead_tags rows for a test either (a) build
// real FHIR-coding-shaped content antigen.Extract actually recognizes (see
// TestIndexBead_RoundTrip), or (b) call seedAntigenRow below to inject a row
// directly, for tests whose subject is bead_tags plumbing itself
// (dedup/idempotency) rather than tag derivation.
func testBead(t *testing.T, typ, note string, parents []string, content map[string]any) bead.Bead {
	t.Helper()
	if content == nil {
		content = map[string]any{}
	}
	content["note"] = note
	b := bead.Bead{
		Type:      typ,
		Timestamp: "2026-03-01T10:00:00Z",
		Author:    "did:medbeads:doctor:12345",
		Parents:   parents,
		Content:   content,
	}
	withID, err := bead.WithID(b)
	if err != nil {
		t.Fatalf("bead.WithID: %v", err)
	}
	return withID
}

// seedAntigenRow inserts one bead_tags row directly, bypassing
// antigen.Extract entirely. It exists for tests whose subject is
// bead_tags' own storage/idempotency behavior (e.g. duplicate-insert
// handling), not tag derivation — antigen.Extract's derivation rules are
// covered by internal/engine/antigen's own fixture-based tests, and
// TestIndexBead_RoundTrip below covers the real IndexBead-drives-Extract
// path end to end.
func seedAntigenRow(t *testing.T, db *DB, tag, beadID, patientRoot string) {
	t.Helper()
	var root any
	if patientRoot != "" {
		root = patientRoot
	}
	if _, err := db.sqlDB.Exec(
		`INSERT OR IGNORE INTO bead_tags (tag, bead_id, patient_root) VALUES (?, ?, ?)`,
		tag, beadID, root,
	); err != nil {
		t.Fatalf("seedAntigenRow(%s, %s): %v", tag, beadID, err)
	}
}

// indexBeadT runs IndexBead inside its own transaction and commits,
// failing the test on any error — the common case for tests that just want
// a Bead durably indexed without exercising batching/transaction behavior
// directly.
func indexBeadT(t *testing.T, db *DB, b bead.Bead, loc BeadLocation) {
	t.Helper()
	tx, err := db.sqlDB.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := IndexBead(tx, b, loc, DefaultFlattener{}); err != nil {
		tx.Rollback()
		t.Fatalf("IndexBead: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
}

// countRows returns the row count of query (a SELECT COUNT(*) ... style
// query with zero or more args), failing the test on error.
func countRows(t *testing.T, sqlDB *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := sqlDB.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("countRows(%q): %v", query, err)
	}
	return n
}
