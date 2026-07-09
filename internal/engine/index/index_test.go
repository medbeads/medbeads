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
// and the given parents/antigens, mirroring internal/engine/pod's testBead
// helper (kept package-local rather than shared, since exporting test-only
// helpers across packages is not warranted here).
func testBead(t *testing.T, typ, note string, parents, antigens []string, content map[string]any) bead.Bead {
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
		Antigens:  antigens,
		Content:   content,
	}
	withID, err := bead.WithID(b)
	if err != nil {
		t.Fatalf("bead.WithID: %v", err)
	}
	return withID
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
