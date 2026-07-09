package index

import (
	"path/filepath"
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
