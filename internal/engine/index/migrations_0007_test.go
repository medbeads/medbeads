package index

import (
	"path/filepath"
	"testing"
)

// TestOpen_0007_SchemaVersionAndTablesExist checks that a freshly opened
// index.db applies migration 0007 (specs/U4_state_derivation.md) and ends up
// at SchemaVersion 7, with bead_status.patient_root added and the new
// active_conditions/active_medications tables present and queryable (empty
// is expected -- U4a is schema-only, U4b's record_state projector is what
// populates them).
func TestOpen_0007_SchemaVersionAndTablesExist(t *testing.T) {
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
	if got != 7 {
		t.Errorf("SchemaVersion = %d, want 7", got)
	}

	// bead_status.patient_root column must exist.
	rows, err := db.sqlDB.Query("PRAGMA table_info(bead_status)")
	if err != nil {
		t.Fatalf("PRAGMA table_info(bead_status): %v", err)
	}
	var foundPatientRoot bool
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var dfltValue any
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err != nil {
			rows.Close()
			t.Fatalf("scan table_info row: %v", err)
		}
		if name == "patient_root" {
			foundPatientRoot = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatalf("rows: %v", err)
	}
	rows.Close()
	if !foundPatientRoot {
		t.Error("bead_status.patient_root column missing after migration 0007")
	}

	// Every new table must exist and be queryable (empty is fine -- U4a is
	// schema-only, no projector writes to these yet).
	for _, table := range []string{"active_conditions", "active_medications"} {
		var count int
		if err := db.sqlDB.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil {
			t.Errorf("SELECT count(*) FROM %s: %v", table, err)
			continue
		}
		if count != 0 {
			t.Errorf("table %s: got count=%d, want 0 (fresh DB)", table, count)
		}
	}

	// The three new indexes must exist.
	for _, indexName := range []string{
		"idx_bead_status_patient",
		"idx_active_conditions_patient",
		"idx_active_medications_patient",
	} {
		var name string
		if err := db.sqlDB.QueryRow(
			`SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?`, indexName,
		).Scan(&name); err != nil {
			t.Errorf("index %q missing after migration 0007: %v", indexName, err)
		}
	}

	// Insert/select round trip against active_conditions and
	// active_medications must actually work end to end.
	if _, err := db.sqlDB.Exec(
		`INSERT INTO active_conditions
			(patient_root, bead_id, current_bead_id, clinical_status, verification_status, projection_run_id)
		 VALUES ('root7', 'condBead7', 'condBead7', 'active', 'confirmed', 'run7')`,
	); err != nil {
		t.Fatalf("insert active_conditions row: %v", err)
	}
	var clinicalStatus string
	if err := db.sqlDB.QueryRow(
		`SELECT clinical_status FROM active_conditions WHERE patient_root = 'root7' AND bead_id = 'condBead7'`,
	).Scan(&clinicalStatus); err != nil {
		t.Fatalf("select active_conditions: %v", err)
	}
	if clinicalStatus != "active" {
		t.Errorf("active_conditions.clinical_status = %q, want %q", clinicalStatus, "active")
	}

	if _, err := db.sqlDB.Exec(
		`INSERT INTO active_medications
			(patient_root, bead_id, current_bead_id, medication_status, intent, projection_run_id)
		 VALUES ('root7', 'medBead7', 'medBead7', 'active', 'order', 'run7')`,
	); err != nil {
		t.Fatalf("insert active_medications row: %v", err)
	}
	var medicationStatus string
	if err := db.sqlDB.QueryRow(
		`SELECT medication_status FROM active_medications WHERE patient_root = 'root7' AND bead_id = 'medBead7'`,
	).Scan(&medicationStatus); err != nil {
		t.Fatalf("select active_medications: %v", err)
	}
	if medicationStatus != "active" {
		t.Errorf("active_medications.medication_status = %q, want %q", medicationStatus, "active")
	}

	// bead_status.patient_root must actually be writable/queryable too.
	if _, err := db.sqlDB.Exec(
		`INSERT INTO bead_status (bead_id, status, patient_root) VALUES ('statusBead7', 'active', 'root7')`,
	); err != nil {
		t.Fatalf("insert bead_status row with patient_root: %v", err)
	}
	var patientRoot string
	if err := db.sqlDB.QueryRow(
		`SELECT patient_root FROM bead_status WHERE bead_id = 'statusBead7'`,
	).Scan(&patientRoot); err != nil {
		t.Fatalf("select bead_status.patient_root: %v", err)
	}
	if patientRoot != "root7" {
		t.Errorf("bead_status.patient_root = %q, want %q", patientRoot, "root7")
	}
}

// TestOpen_0007_ReOpenIsIdempotent verifies re-opening an already-migrated
// (version 7) index.db a second time does not error and keeps SchemaVersion
// at 7, per applyMigrations' idempotency contract.
func TestOpen_0007_ReOpenIsIdempotent(t *testing.T) {
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

	if v1 != 7 || v2 != 7 {
		t.Errorf("schema version across re-Open: first=%d second=%d, want both 7", v1, v2)
	}
}
