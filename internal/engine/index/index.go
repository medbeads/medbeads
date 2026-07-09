package index

import (
	"database/sql"
	"errors"
	"fmt"

	_ "github.com/mattn/go-sqlite3" // registers the "sqlite3" database/sql driver
)

// driverName is the database/sql driver name registered by
// github.com/mattn/go-sqlite3's init(). See specs/DESIGN_v3.md §5 and the
// lead decision to use mattn/go-sqlite3 (CGO) + the sqlite_fts5 build tag so
// FTS5 trigram (R3.3) and a future sqlite-vec load path (R4.2) are both
// available. This file itself carries no build tag: the tag only changes
// what the mattn/go-sqlite3 package compiles internally (see its
// sqlite3_opt_fts5.go), not what our code needs to reference.
const driverName = "sqlite3"

// Sentinel errors returned by this package. Callers should use errors.Is.
var (
	// ErrNotFound means a lookup (GetBead, etc.) found no matching row.
	ErrNotFound = errors.New("index: not found")
)

// DB wraps a database/sql handle to index.db, opened and migrated. All
// exported methods on DB are safe for concurrent use (database/sql pools
// connections internally); write methods additionally serialize via SQLite's
// own locking, matching the "index.db is a rebuildable cache" design (no
// bespoke in-process write mutex needed beyond what SQLite already does).
type DB struct {
	sqlDB *sql.DB
	path  string
}

// Open opens (creating if necessary) the SQLite index database at path,
// applies WAL mode / foreign_keys / busy_timeout pragmas, and brings the
// schema up to date via the embedded numbered migrations
// (migrations/NNNN_*.sql). Open is idempotent: calling it again against an
// already-current database is safe and a no-op past the pragma settings.
func Open(path string) (*DB, error) {
	// _foreign_keys=1 and _busy_timeout are mattn/go-sqlite3 DSN query
	// parameters applied at connection-open time; journal_mode is set via an
	// explicit PRAGMA below since WAL is a persistent, on-disk database
	// property (not per-connection) and is clearer to set explicitly.
	dsn := fmt.Sprintf("file:%s?_foreign_keys=1&_busy_timeout=5000", path)
	sqlDB, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("index: open %s: %w", path, err)
	}

	if _, err := sqlDB.Exec("PRAGMA journal_mode = WAL"); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("index: open %s: set journal_mode: %w", path, err)
	}
	if _, err := sqlDB.Exec("PRAGMA foreign_keys = ON"); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("index: open %s: set foreign_keys: %w", path, err)
	}

	if err := applyMigrations(sqlDB); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("index: open %s: %w", path, err)
	}

	return &DB{sqlDB: sqlDB, path: path}, nil
}

// Path returns the file path this DB was opened from.
func (d *DB) Path() string {
	return d.path
}

// Close closes the underlying database handle.
func (d *DB) Close() error {
	if err := d.sqlDB.Close(); err != nil {
		return fmt.Errorf("index: close %s: %w", d.path, err)
	}
	return nil
}
