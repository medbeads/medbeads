package index

import (
	"path/filepath"
	"testing"
)

func TestOpen_0008_PatientProjectionStateExists(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	version, err := SchemaVersion(db.sqlDB)
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if version < 8 {
		t.Fatalf("SchemaVersion = %d, want >= 8", version)
	}

	if _, err := db.sqlDB.Exec(`
		INSERT INTO patient_projection_state
			(patient_root, pod_path, indexed_upto, clinical_links_run_id,
			 record_state_run_id, projected_at)
		VALUES ('root8', 'pods/aa/root8.pod', 123, 'links8', 'status8',
		        '2026-07-14T00:00:00Z')`); err != nil {
		t.Fatalf("insert patient_projection_state: %v", err)
	}

	var upto int64
	var linksRun, statusRun string
	if err := db.sqlDB.QueryRow(`
		SELECT indexed_upto, clinical_links_run_id, record_state_run_id
		FROM patient_projection_state WHERE patient_root = 'root8'`,
	).Scan(&upto, &linksRun, &statusRun); err != nil {
		t.Fatalf("select patient_projection_state: %v", err)
	}
	if upto != 123 || linksRun != "links8" || statusRun != "status8" {
		t.Errorf("checkpoint = (%d,%s,%s), want (123,links8,status8)", upto, linksRun, statusRun)
	}

	for _, name := range []string{
		"idx_patient_projection_links_run",
		"idx_patient_projection_status_run",
	} {
		var got string
		if err := db.sqlDB.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='index' AND name=?`, name,
		).Scan(&got); err != nil {
			t.Errorf("index %s missing: %v", name, err)
		}
	}
}
