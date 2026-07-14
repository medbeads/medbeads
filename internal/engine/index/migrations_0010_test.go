package index

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestOpen_0010_ActivityPriorityIndexExists(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	version, err := SchemaVersion(db.sqlDB)
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if version < 10 {
		t.Fatalf("SchemaVersion = %d, want >= 10", version)
	}
	var name string
	if err := db.sqlDB.QueryRow(`
		SELECT name FROM sqlite_master
		WHERE type='index' AND name='idx_patient_activity_priority'`).Scan(&name); err != nil {
		t.Fatalf("priority index missing: %v", err)
	}

	rows, err := db.sqlDB.Query(`
		EXPLAIN QUERY PLAN
		SELECT s.patient_root
		FROM patient_activity a INDEXED BY idx_patient_activity_priority
		JOIN patient_projection_state s ON s.patient_root=a.patient_root
		WHERE s.clinical_links_run_id<>?
		ORDER BY a.deceased_hint, a.last_visit_at DESC, a.patient_root
		LIMIT ?`, "target", 25)
	if err != nil {
		t.Fatalf("EXPLAIN priority selection: %v", err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatalf("scan query plan: %v", err)
		}
		details = append(details, detail)
	}
	plan := strings.Join(details, "\n")
	if !strings.Contains(plan, "idx_patient_activity_priority") {
		t.Fatalf("priority query does not use scheduling index:\n%s", plan)
	}
	if strings.Contains(plan, "USE TEMP B-TREE") {
		t.Fatalf("priority query performs a full temporary sort:\n%s", plan)
	}
}
