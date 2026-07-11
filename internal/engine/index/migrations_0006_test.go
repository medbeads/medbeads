package index

import (
	"path/filepath"
	"testing"
)

// TestOpen_0006_SchemaVersionAndTablesExist checks that a freshly opened
// index.db applies migration 0006 (specs/U2_projection_schema.md) and ends
// up at SchemaVersion 6, with every new projection table present and
// queryable, and the beads.recorded_at column added.
func TestOpen_0006_SchemaVersionAndTablesExist(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "index.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	got, err := SchemaVersion(db.sqlDB)
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if got != 6 {
		t.Errorf("SchemaVersion = %d, want 6", got)
	}

	// Every new table must exist and be queryable (empty is fine — U2 is
	// schema-only, no projector writes to these yet).
	for _, table := range []string{"bead_tags", "clinical_links", "bead_status", "projection_manifest"} {
		var count int
		if err := db.sqlDB.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil {
			t.Errorf("SELECT count(*) FROM %s: %v", table, err)
			continue
		}
		if count != 0 {
			t.Errorf("table %s: got count=%d, want 0 (fresh DB)", table, count)
		}
	}

	// beads.recorded_at column must exist (PRAGMA table_info lists it).
	rows, err := db.sqlDB.Query("PRAGMA table_info(beads)")
	if err != nil {
		t.Fatalf("PRAGMA table_info(beads): %v", err)
	}
	defer rows.Close()
	var found bool
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var dfltValue any
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err != nil {
			t.Fatalf("scan table_info row: %v", err)
		}
		if name == "recorded_at" {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if !found {
		t.Error("beads.recorded_at column missing after migration 0006")
	}

	// Insert/select touching recorded_at must actually work end to end.
	if _, err := db.sqlDB.Exec(
		`INSERT INTO pods (path, patient_root, size, indexed_upto) VALUES ('p6', 'root6', 0, 0)`,
	); err != nil {
		t.Fatalf("seed pods: %v", err)
	}
	if _, err := db.sqlDB.Exec(
		`INSERT INTO beads (id, patient_root, type, timestamp, recorded_at, pod_id, offset, length)
		 VALUES ('bead6', 'root6', 'fhir_observation', '2026-01-01T00:00:00Z', '2026-01-02T00:00:00.123Z', 1, 0, 10)`,
	); err != nil {
		t.Fatalf("insert bead with recorded_at: %v", err)
	}
	var recordedAt string
	if err := db.sqlDB.QueryRow(`SELECT recorded_at FROM beads WHERE id = 'bead6'`).Scan(&recordedAt); err != nil {
		t.Fatalf("select recorded_at: %v", err)
	}
	if recordedAt != "2026-01-02T00:00:00.123Z" {
		t.Errorf("recorded_at = %q, want %q", recordedAt, "2026-01-02T00:00:00.123Z")
	}

	// idx_beads_root_recorded must exist.
	var indexName string
	if err := db.sqlDB.QueryRow(
		`SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?`,
		"idx_beads_root_recorded",
	).Scan(&indexName); err != nil {
		t.Errorf("idx_beads_root_recorded missing after migration 0006: %v", err)
	}
}

// TestOpen_0006_ReOpenIsIdempotent verifies re-opening an already-migrated
// (version 6) index.db a second time does not error and keeps
// SchemaVersion at 6, per applyMigrations' idempotency contract.
func TestOpen_0006_ReOpenIsIdempotent(t *testing.T) {
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

	if v1 != 6 || v2 != 6 {
		t.Errorf("schema version across re-Open: first=%d second=%d, want both 6", v1, v2)
	}
}

// TestClinicalLinks_SeverityCheckConstraint asserts the runaway-prevention
// CHECK constraint in clinical_links: a co-occurrence-derived link may only
// ever be surfaced at severity='info'; anything at 'warning' or above must
// cite curated_knowledge/guideline evidence with a non-NULL rule_version and
// a non-empty evidence_bead_ids array.
func TestClinicalLinks_SeverityCheckConstraint(t *testing.T) {
	db := openT(t)

	// matched_tag is varied per attempt (not held constant like bead_a/
	// bead_b/relation) because (bead_a, bead_b, relation, matched_tag) is
	// the table's UNIQUE natural key — reusing the same matched_tag across
	// these inserts would trip the UNIQUE constraint instead of exercising
	// the severity CHECK this test targets.
	baseInsert := `INSERT INTO clinical_links
		(link_id, bead_a, bead_b, patient_root, relation, matched_tag, severity,
		 evidence_basis, evidence_bead_ids, rule_version, created_at)
		VALUES (?, 'beadA', 'beadB', 'root1', 'sibling', ?, ?, ?, ?, ?, '2026-01-01T00:00:00Z')`

	// severity='warning' + evidence_basis='cooccurrence' must FAIL: a bare
	// co-occurrence can never justify anything above 'info'.
	if _, err := db.sqlDB.Exec(baseInsert, "link1", "tag1", "warning", "cooccurrence", "[]", nil); err == nil {
		t.Error("INSERT with severity='warning', evidence_basis='cooccurrence' succeeded, want CHECK constraint failure")
	}

	// severity='warning' + curated_knowledge but rule_version NULL must FAIL.
	if _, err := db.sqlDB.Exec(baseInsert, "link2", "tag2", "warning", "curated_knowledge", `["knowledge-bead-1"]`, nil); err == nil {
		t.Error("INSERT with severity='warning', rule_version=NULL succeeded, want CHECK constraint failure")
	}

	// severity='warning' + curated_knowledge + rule_version set but
	// evidence_bead_ids='[]' (empty) must FAIL.
	if _, err := db.sqlDB.Exec(baseInsert, "link3", "tag3", "warning", "curated_knowledge", "[]", "rule-bead-1"); err == nil {
		t.Error("INSERT with severity='warning', evidence_bead_ids='[]' succeeded, want CHECK constraint failure")
	}

	// severity='info' + evidence_basis='cooccurrence' must SUCCEED: this is
	// exactly the unsupported-statistics case the CHECK is designed to allow
	// at 'info' only.
	if _, err := db.sqlDB.Exec(baseInsert, "link4", "tag4", "info", "cooccurrence", "[]", nil); err != nil {
		t.Errorf("INSERT with severity='info', evidence_basis='cooccurrence' failed, want success: %v", err)
	}

	// severity='warning' + curated_knowledge + rule_version set + non-empty
	// evidence_bead_ids must SUCCEED.
	if _, err := db.sqlDB.Exec(baseInsert, "link5", "tag5", "warning", "curated_knowledge", `["knowledge-bead-1"]`, "rule-bead-1"); err != nil {
		t.Errorf("INSERT with fully-cited curated_knowledge warning failed, want success: %v", err)
	}
}

// TestClinicalLinks_BeadOrderCheckConstraint asserts the CHECK (bead_a <
// bead_b) normalization constraint: an unordered pair must always be
// inserted with bead_a lexically before bead_b.
func TestClinicalLinks_BeadOrderCheckConstraint(t *testing.T) {
	db := openT(t)

	insert := `INSERT INTO clinical_links
		(link_id, bead_a, bead_b, patient_root, relation, matched_tag, severity,
		 evidence_basis, evidence_bead_ids, created_at)
		VALUES (?, ?, ?, 'root1', 'sibling', 'loinc:1234-5', 'info', 'cooccurrence', '[]', '2026-01-01T00:00:00Z')`

	if _, err := db.sqlDB.Exec(insert, "linkA", "beadZ", "beadA"); err == nil {
		t.Error("INSERT with bead_a > bead_b succeeded, want CHECK (bead_a < bead_b) failure")
	}
	if _, err := db.sqlDB.Exec(insert, "linkB", "beadA", "beadZ"); err != nil {
		t.Errorf("INSERT with bead_a < bead_b failed, want success: %v", err)
	}
}

// TestProjectionManifest_OneActivePerName asserts the partial-unique index
// idx_projection_manifest_one_active: at most one 'active' manifest row may
// exist per projection_name at a time, but any number of non-active
// (building/superseded/failed) rows are unconstrained.
func TestProjectionManifest_OneActivePerName(t *testing.T) {
	db := openT(t)

	insert := `INSERT INTO projection_manifest
		(run_id, projection_name, code_version, config_hash, input_watermarks, built_at, activated_at, status)
		VALUES (?, ?, 'v1', 'hash1', '{}', '2026-01-01T00:00:00Z', ?, ?)`

	// First active row for "clinical_v31" succeeds.
	if _, err := db.sqlDB.Exec(insert, "run1", "clinical_v31", "2026-01-01T00:00:00Z", "active"); err != nil {
		t.Fatalf("insert first active manifest row failed: %v", err)
	}

	// A second 'active' row for the SAME projection_name must FAIL the
	// partial unique index.
	if _, err := db.sqlDB.Exec(insert, "run2", "clinical_v31", "2026-01-01T00:00:00Z", "active"); err == nil {
		t.Error("INSERT of a second active manifest row for the same projection_name succeeded, want UNIQUE index failure")
	}

	// A second row for the same projection_name with status='superseded'
	// must SUCCEED (the partial index only constrains status='active' rows).
	if _, err := db.sqlDB.Exec(insert, "run3", "clinical_v31", nil, "superseded"); err != nil {
		t.Errorf("INSERT of a superseded manifest row alongside an active one failed, want success: %v", err)
	}

	// An 'active' row for a DIFFERENT projection_name must SUCCEED (the
	// unique constraint is scoped per projection_name).
	if _, err := db.sqlDB.Exec(insert, "run4", "other_projection", "2026-01-01T00:00:00Z", "active"); err != nil {
		t.Errorf("INSERT of an active manifest row for a different projection_name failed, want success: %v", err)
	}
}
