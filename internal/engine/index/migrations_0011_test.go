package index

import (
	"path/filepath"
	"testing"
)

func TestOpen_0011_PreservesAssertionsFromMultipleRules(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	insert := `INSERT INTO clinical_links
		(link_id, bead_a, bead_b, patient_root, relation, matched_tag,
		 severity, evidence_basis, evidence_bead_ids, rule_id, rule_version,
		 created_at)
		VALUES (?, 'a', 'b', 'patient', 'interaction', 'atc:a+atc:b',
		        'warning', 'guideline', ?, 'ddi-rule', ?,
		        '2026-07-14T00:00:00Z')`
	if _, err := db.sqlDB.Exec(insert, "link-rule-1", `["source-1"]`, "rule-version-1"); err != nil {
		t.Fatalf("insert rule 1 assertion: %v", err)
	}
	if _, err := db.sqlDB.Exec(insert, "link-rule-2", `["source-2"]`, "rule-version-2"); err != nil {
		t.Fatalf("insert rule 2 assertion: %v", err)
	}
	var count int
	if err := db.sqlDB.QueryRow(`
		SELECT COUNT(*) FROM clinical_links
		WHERE bead_a='a' AND bead_b='b' AND relation='interaction'`).Scan(&count); err != nil {
		t.Fatalf("count assertions: %v", err)
	}
	if count != 2 {
		t.Fatalf("assertions=%d, want 2 independent rule versions", count)
	}
}
