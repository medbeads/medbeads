package index

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// TestOpen_NewDB_AppliesMigrations_UserVersion checks that opening a brand
// new index.db applies every embedded migration and leaves PRAGMA
// user_version at the number of migrations (currently 1: 0001_init.sql).
func TestOpen_NewDB_AppliesMigrations_UserVersion(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "index.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	wantVersion := 0
	for _, m := range migrations {
		if m.version > wantVersion {
			wantVersion = m.version
		}
	}
	if wantVersion == 0 {
		t.Fatal("no embedded migrations found — migrations/*.sql embed is broken")
	}

	got, err := SchemaVersion(db.sqlDB)
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if got != wantVersion {
		t.Errorf("SchemaVersion = %d, want %d", got, wantVersion)
	}

	// Every table specs/DESIGN_v3.md §5 lists as in-scope for R3 must exist.
	for _, table := range []string{"pods", "beads", "bead_edges", "bead_antigens", "beads_fts"} {
		var name string
		err := db.sqlDB.QueryRow(
			`SELECT name FROM sqlite_master WHERE type IN ('table','view') AND name = ?`, table,
		).Scan(&name)
		if err != nil {
			t.Errorf("table/view %q missing after migration: %v", table, err)
		}
	}
}

// TestOpen_ReOpen_IsIdempotent verifies re-opening an already-migrated
// index.db does not error and does not change its schema_version.
func TestOpen_ReOpen_IsIdempotent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "index.db")

	db1, err := Open(dbPath)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	v1, err := SchemaVersion(db1.sqlDB)
	if err != nil {
		t.Fatalf("SchemaVersion (first): %v", err)
	}
	if err := db1.Close(); err != nil {
		t.Fatalf("Close (first): %v", err)
	}

	db2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer db2.Close()
	v2, err := SchemaVersion(db2.sqlDB)
	if err != nil {
		t.Fatalf("SchemaVersion (second): %v", err)
	}

	if v1 != v2 {
		t.Errorf("schema version changed across re-Open: first=%d second=%d", v1, v2)
	}
}

// TestOpen_Pragmas checks the pragmas Open is responsible for setting:
// WAL journal mode and foreign_keys enforcement.
func TestOpen_Pragmas(t *testing.T) {
	db := openT(t)

	var journalMode string
	if err := db.sqlDB.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Errorf("journal_mode = %q, want %q", journalMode, "wal")
	}

	var foreignKeys int
	if err := db.sqlDB.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatalf("PRAGMA foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Errorf("foreign_keys = %d, want 1 (enabled)", foreignKeys)
	}
}

// TestOpen_IncrementalApply_0005AddsPatientAntigenIndex checks the
// migrations/0005_bead_antigens_patient_idx.sql incremental-apply path a
// real, already-running store hits: a DB previously opened only through
// migration 0003 (i.e. before 0004/0005 existed) must, on the next Open,
// apply 0004 and then 0005 in order and end up with
// idx_bead_antigens_patient present, without losing or altering any
// pre-existing bead_antigens data. This mirrors the real production store
// this migration was written for (measured directly: schema at
// user_version=3, 1,576,172 pre-existing bead_antigens rows, 1,461
// bead_apc_scan rows from a partial APC scan already in progress — see
// TestReindex... siblings for the equivalent Reindex-side round-trip
// concern, and see cmd/medbeadsd's own migration-driven Open path, which
// this test exercises via the same index.Open every real invocation uses).
func TestOpen_IncrementalApply_0005AddsPatientAntigenIndex(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "index.db")

	// Build a DB stuck at user_version=3 (migrations 0001-0003 applied,
	// 0004/0005 not yet — the exact state Open must recover from on a real
	// store that predates this migration) by applying only those three
	// embedded migrations directly, bypassing applyMigrations' "apply
	// everything newer than current" loop.
	sqlDB, err := sql.Open(driverName, fmt.Sprintf("file:%s?_foreign_keys=1", dbPath))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	for _, m := range migrations {
		if m.version > 3 {
			continue
		}
		if err := applyOne(sqlDB, m); err != nil {
			t.Fatalf("apply migration %d: %v", m.version, err)
		}
	}
	if v, err := userVersion(sqlDB); err != nil || v != 3 {
		t.Fatalf("setup: user_version = %d, %v, want 3, nil", v, err)
	}

	// Seed a little pre-existing bead_antigens data at the pre-0005 schema,
	// the way a real store would have accumulated it over normal ingest —
	// so the test can assert this data survives 0004+0005 untouched.
	if _, err := sqlDB.Exec(
		`INSERT INTO pods (path, patient_root, size, indexed_upto) VALUES ('p', 'root1', 0, 0)`,
	); err != nil {
		t.Fatalf("seed pods: %v", err)
	}
	if _, err := sqlDB.Exec(
		`INSERT INTO beads (id, patient_root, type, timestamp, pod_id, offset, length) VALUES ('bead1', 'root1', 'fhir_observation', '2026-01-01T00:00:00Z', 1, 0, 10)`,
	); err != nil {
		t.Fatalf("seed beads: %v", err)
	}
	if _, err := sqlDB.Exec(
		`INSERT INTO bead_antigens (antigen, bead_id, patient_root) VALUES ('loinc:1234-5', 'bead1', 'root1')`,
	); err != nil {
		t.Fatalf("seed bead_antigens: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close setup db: %v", err)
	}

	// The real Open path: must apply 0004 then 0005 without error or data
	// loss.
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open (incremental apply from v3): %v", err)
	}
	defer db.Close()

	wantVersion := 0
	for _, m := range migrations {
		if m.version > wantVersion {
			wantVersion = m.version
		}
	}
	got, err := SchemaVersion(db.sqlDB)
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if got != wantVersion {
		t.Errorf("SchemaVersion after incremental apply = %d, want %d", got, wantVersion)
	}

	var indexName string
	if err := db.sqlDB.QueryRow(
		`SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?`,
		"idx_bead_antigens_patient",
	).Scan(&indexName); err != nil {
		t.Errorf("idx_bead_antigens_patient missing after incremental apply: %v", err)
	}

	// Pre-existing data must be untouched.
	var antigen, beadID, patientRoot string
	if err := db.sqlDB.QueryRow(
		`SELECT antigen, bead_id, patient_root FROM bead_antigens WHERE bead_id = 'bead1'`,
	).Scan(&antigen, &beadID, &patientRoot); err != nil {
		t.Fatalf("pre-existing bead_antigens row lost after incremental apply: %v", err)
	}
	if antigen != "loinc:1234-5" || beadID != "bead1" || patientRoot != "root1" {
		t.Errorf("pre-existing bead_antigens row corrupted: got (%q, %q, %q)", antigen, beadID, patientRoot)
	}

	// EXPLAIN QUERY PLAN must actually use the new index for the
	// patient-scoped query shape frequentAntigens issues (the whole point
	// of this migration) — a smoke check that the index is not just present
	// in sqlite_master but actually selected by the query planner.
	rows, err := db.sqlDB.Query(
		`EXPLAIN QUERY PLAN SELECT COUNT(DISTINCT bead_id) FROM bead_antigens WHERE patient_root = 'root1'`)
	if err != nil {
		t.Fatalf("EXPLAIN QUERY PLAN: %v", err)
	}
	defer rows.Close()
	var usesNewIndex bool
	cols, err := rows.Columns()
	if err != nil {
		t.Fatalf("columns: %v", err)
	}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatalf("scan explain row: %v", err)
		}
		for _, v := range vals {
			if s, ok := v.(string); ok && strings.Contains(s, "idx_bead_antigens_patient") {
				usesNewIndex = true
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if !usesNewIndex {
		t.Error("EXPLAIN QUERY PLAN for the patient-scoped bead_antigens query does not mention idx_bead_antigens_patient — index not selected by planner")
	}
}

// TestParseMigrationFilename covers the NNNN_name.sql parsing rules
// applyMigrations relies on to order and validate migrations.
func TestParseMigrationFilename(t *testing.T) {
	tests := []struct {
		filename    string
		wantVersion int
		wantName    string
		wantErr     bool
	}{
		{filename: "0001_init.sql", wantVersion: 1, wantName: "init"},
		{filename: "0042_add_widgets.sql", wantVersion: 42, wantName: "add_widgets"},
		{filename: "init.sql", wantErr: true},
		{filename: "abcd_init.sql", wantErr: true},
		{filename: "0000_zero.sql", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			version, name, err := parseMigrationFilename(tt.filename)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseMigrationFilename(%q) = nil error, want error", tt.filename)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseMigrationFilename(%q): %v", tt.filename, err)
			}
			if version != tt.wantVersion || name != tt.wantName {
				t.Errorf("parseMigrationFilename(%q) = (%d, %q), want (%d, %q)",
					tt.filename, version, name, tt.wantVersion, tt.wantName)
			}
		})
	}
}
