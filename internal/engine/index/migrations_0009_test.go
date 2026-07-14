package index

import (
	"path/filepath"
	"testing"
)

func TestOpen_0009_PriorityReprojectionTablesExist(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	version, err := SchemaVersion(db.sqlDB)
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if version < 9 {
		t.Fatalf("SchemaVersion = %d, want >= 9", version)
	}

	if _, err := db.sqlDB.Exec(`
		INSERT INTO patient_activity
			(patient_root, last_recorded_at, last_clinical_at,
			 last_encounter_at, deceased_hint, updated_at)
		VALUES ('root9', '2026-07-14T00:00:00Z', '2026-07-01',
		        '2026-07-01', 0, '2026-07-14T00:00:00Z')`); err != nil {
		t.Fatalf("insert patient_activity: %v", err)
	}
	if _, err := db.sqlDB.Exec(`
		INSERT INTO patient_reprojection_queue
			(patient_root, target_links_run_id, reason, enqueued_at)
		VALUES ('root9', 'run9', 'knowledge_generation_changed',
		        '2026-07-14T00:00:00Z')`); err != nil {
		t.Fatalf("insert patient_reprojection_queue: %v", err)
	}

	var runID string
	if err := db.sqlDB.QueryRow(`
		SELECT target_links_run_id FROM patient_reprojection_queue
		WHERE patient_root='root9'`).Scan(&runID); err != nil {
		t.Fatalf("select queue: %v", err)
	}
	if runID != "run9" {
		t.Fatalf("target_links_run_id = %q, want run9", runID)
	}
}
