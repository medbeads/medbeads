package index

import (
	"database/sql"
	"errors"
	"fmt"
	"sync"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	_ "github.com/mattn/go-sqlite3" // registers the "sqlite3" database/sql driver
)

// driverName is the database/sql driver name registered by
// github.com/mattn/go-sqlite3's init(). See specs/DESIGN_v3.md §5 and the
// lead decision to use mattn/go-sqlite3 (CGO) + the sqlite_fts5 build tag so
// FTS5 trigram (R3.3) and, since R4.2, a sqlite-vec vec0 load path are both
// available. This file itself carries no build tag: the tag only changes
// what the mattn/go-sqlite3 package compiles internally (see its
// sqlite3_opt_fts5.go), not what our code needs to reference.
const driverName = "sqlite3"

// registerVecExtension calls sqlite_vec.Auto() exactly once per process
// (sync.Once), before any *sql.DB is ever opened by this package.
//
// sqlite_vec.Auto() wraps SQLite's own sqlite3_auto_extension() C API: it
// registers sqlite-vec's init function to run automatically on every future
// new SQLite connection opened in this process (mattn/go-sqlite3 connections
// included, since sqlite-vec-go-bindings/cgo's C sources are compiled with
// -DSQLITE_CORE — see its lib.go — statically linking against the same
// libsqlite3 mattn/go-sqlite3 itself compiles in, not a separately
// dlopen'd .so). This is a process-global registration, not a per-*sql.DB
// or per-connection call: calling it more than once is harmless (SQLite
// auto_extension dedupes by function pointer) but only needs to happen
// once, and must happen before Open's first sql.Open/db.Exec — a
// connection opened before Auto() runs would never see vec0 registered on
// it. sync.Once (rather than a package init()) keeps this colocated with
// Open itself, which is the one place in this package that ever calls
// sql.Open; it does not change ordering relative to init() since Open is
// always called after package initialization completes in Go.
var registerVecExtensionOnce sync.Once

func registerVecExtension() {
	registerVecExtensionOnce.Do(sqlite_vec.Auto)
}

// Sentinel errors returned by this package. Callers should use errors.Is.
var (
	// ErrNotFound means a lookup (GetBead, etc.) found no matching row.
	ErrNotFound = errors.New("index: not found")
)

// DB wraps a database/sql handle to index.db, opened and migrated. All
// exported methods on DB are safe for concurrent use: Open caps the
// underlying connection pool at one open connection (see Open), so
// database/sql's own connection-checkout queue serializes concurrent callers
// onto SQLite's single writer rather than relying solely on SQLite's
// busy-timeout retry to arbitrate between multiple concurrently-open
// connections (which does not reliably cover every lock-upgrade case — see
// Open's doc comment). No separate in-process write mutex is layered on top
// of that; the capped pool is the single serialization point.
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
	registerVecExtension()

	// _foreign_keys=1 and _busy_timeout are mattn/go-sqlite3 DSN query
	// parameters applied at connection-open time; journal_mode is set via an
	// explicit PRAGMA below since WAL is a persistent, on-disk database
	// property (not per-connection) and is clearer to set explicitly.
	dsn := fmt.Sprintf("file:%s?_foreign_keys=1&_busy_timeout=5000", path)
	sqlDB, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("index: open %s: %w", path, err)
	}

	// SQLite allows exactly one writer at a time; database/sql's default
	// pool otherwise opens one *SQLite* connection per concurrent Go
	// goroutine that needs one. A transaction that reads (e.g. IndexBead's
	// RegisterPod SELECT) before its first write then tries to upgrade to a
	// write lock on commit — and under concurrent load from multiple
	// connections, that upgrade can return SQLITE_BUSY as "database is
	// locked" in ways _busy_timeout's retry does not reliably absorb (this
	// was reproduced directly: concurrent IndexBead calls without this line
	// fail intermittently under -race with exactly that error). Capping the
	// pool at one open connection makes database/sql itself queue callers
	// for the single connection — turning "many SQLite connections
	// contending for one write lock" into "many goroutines waiting for one
	// Go-level resource", which is both correct and simpler than trying to
	// out-tune SQLite's own locking. This is a pure availability property of
	// database/sql's pool (FIFO queuing for a checked-out connection), not
	// dependent on any particular fairness order.
	sqlDB.SetMaxOpenConns(1)

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
