package index

import (
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

// migrationsFS embeds the numbered schema migration SQL files
// (migrations/0001_init.sql, ...). Each file's leading numeric prefix is its
// migration number; PRAGMA user_version records how many have been applied,
// per the project's "numbered SQL migrations" convention.
//
//go:embed migrations/*.sql
var migrationsFS embed.FS

// migration is one parsed, ready-to-apply schema migration.
type migration struct {
	version int
	name    string
	sql     string
}

// loadMigrations reads every migrations/*.sql file, parses its leading
// "NNNN_" numeric prefix as the migration version, and returns them sorted
// ascending by version. It fails closed on any filename that does not match
// the NNNN_name.sql convention, or on a duplicate version, rather than
// silently skipping a malformed migration file.
func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("index: load migrations: %w", err)
	}

	out := make([]migration, 0, len(entries))
	seen := make(map[int]string, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		version, name, err := parseMigrationFilename(e.Name())
		if err != nil {
			return nil, fmt.Errorf("index: load migrations: %w", err)
		}
		if prior, ok := seen[version]; ok {
			return nil, fmt.Errorf("index: load migrations: duplicate migration version %d (%s and %s)", version, prior, e.Name())
		}
		seen[version] = e.Name()

		body, err := migrationsFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, fmt.Errorf("index: load migrations: read %s: %w", e.Name(), err)
		}
		out = append(out, migration{version: version, name: name, sql: string(body)})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

// parseMigrationFilename splits "0001_init.sql" into version=1, name="init".
func parseMigrationFilename(filename string) (version int, name string, err error) {
	base := strings.TrimSuffix(filename, ".sql")
	prefix, rest, ok := strings.Cut(base, "_")
	if !ok {
		return 0, "", fmt.Errorf("migration filename %q does not match NNNN_name.sql", filename)
	}
	n, err := strconv.Atoi(prefix)
	if err != nil {
		return 0, "", fmt.Errorf("migration filename %q: non-numeric prefix: %w", filename, err)
	}
	if n <= 0 {
		return 0, "", fmt.Errorf("migration filename %q: version must be positive, got %d", filename, n)
	}
	return n, rest, nil
}

// applyMigrations brings db's schema up to the latest embedded migration,
// tracked via PRAGMA user_version. It is idempotent: calling it again on an
// already-current database is a no-op (Open calls this every time, so
// re-opening the same index.db must not fail or re-run migrations).
func applyMigrations(db *sql.DB) error {
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}

	current, err := userVersion(db)
	if err != nil {
		return fmt.Errorf("index: apply migrations: %w", err)
	}

	for _, m := range migrations {
		if m.version <= current {
			continue
		}
		if err := applyOne(db, m); err != nil {
			return fmt.Errorf("index: apply migration %04d_%s: %w", m.version, m.name, err)
		}
	}
	return nil
}

// applyOne runs a single migration's SQL and advances user_version to its
// version, inside one transaction so a failing migration never leaves the
// schema half-applied with a stale user_version (or vice versa).
func applyOne(db *sql.DB, m migration) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after commit is a no-op

	if _, err := tx.Exec(m.sql); err != nil {
		return fmt.Errorf("exec: %w", err)
	}
	// PRAGMA user_version does not accept bound parameters; the value comes
	// from our own embedded, trusted migration list (not user input), so
	// interpolating the int is safe.
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", m.version)); err != nil {
		return fmt.Errorf("set user_version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// userVersion reads the database's current PRAGMA user_version.
func userVersion(db *sql.DB) (int, error) {
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return 0, fmt.Errorf("read user_version: %w", err)
	}
	return v, nil
}

// SchemaVersion returns db's current PRAGMA user_version (the number of
// migrations applied so far). Exported for tests and diagnostics (e.g. a
// future `medbeadsd verify` schema check).
func SchemaVersion(db *sql.DB) (int, error) {
	return userVersion(db)
}
