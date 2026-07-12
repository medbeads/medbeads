package index

import (
	"strings"
	"testing"
)

// seedClinicalLinkRow inserts one clinical_links row directly (bypassing the
// projector, whose write path is not this test's subject), mirroring
// migrations_0006_test.go's own direct-INSERT convention for exercising this
// table. bead_a/bead_b must already satisfy bead_a < bead_b (the table's own
// CHECK constraint).
func seedClinicalLinkRow(t *testing.T, db *DB, linkID, beadA, beadB, patientRoot, relation, matchedTag, severity, evidenceBasis, ruleVersion string) {
	t.Helper()
	var ruleVersionArg any
	if ruleVersion != "" {
		ruleVersionArg = ruleVersion
	}
	evidenceIDs := "[]"
	if ruleVersion != "" {
		evidenceIDs = `["some-evidence-bead"]`
	}
	if _, err := db.sqlDB.Exec(`
		INSERT INTO clinical_links
			(link_id, bead_a, bead_b, patient_root, relation, matched_tag, severity,
			 evidence_basis, evidence_bead_ids, rule_version, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '2026-01-01T00:00:00Z')`,
		linkID, beadA, beadB, patientRoot, relation, matchedTag, severity, evidenceBasis, evidenceIDs, ruleVersionArg,
	); err != nil {
		t.Fatalf("seedClinicalLinkRow(%s): %v", linkID, err)
	}
}

// seedBeadStatusRow inserts one bead_status row directly, mirroring
// bead_status_for_test.go's own direct-INSERT convention.
func seedBeadStatusRow(t *testing.T, db *DB, beadID, status, currentBeadID string) {
	t.Helper()
	var current any
	if currentBeadID != "" {
		current = currentBeadID
	}
	if _, err := db.sqlDB.Exec(
		`INSERT INTO bead_status (bead_id, status, current_bead_id) VALUES (?, ?, ?)`,
		beadID, status, current,
	); err != nil {
		t.Fatalf("seedBeadStatusRow(%s): %v", beadID, err)
	}
}

// TestGetClinicalLinksForPatient_ScopesByPatientRoot confirms the query is
// WHERE patient_root = ? (not, say, an unfiltered scan later narrowed in
// Go), and that a link belonging to a different patient is excluded.
func TestGetClinicalLinksForPatient_ScopesByPatientRoot(t *testing.T) {
	db := openT(t)

	seedClinicalLinkRow(t, db, "link1", "beadA1", "beadB1", "patientX", "cooccurrence", "loinc:1234", "info", "cooccurrence", "")
	seedClinicalLinkRow(t, db, "link2", "beadA2", "beadB2", "patientY", "cooccurrence", "loinc:5678", "info", "cooccurrence", "")

	rows, err := db.GetClinicalLinksForPatient("patientX")
	if err != nil {
		t.Fatalf("GetClinicalLinksForPatient: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1 (only patientX's link)", len(rows))
	}
	if rows[0].LinkID != "link1" {
		t.Errorf("LinkID = %q, want link1", rows[0].LinkID)
	}
	if rows[0].BeadA != "beadA1" || rows[0].BeadB != "beadB1" {
		t.Errorf("BeadA/BeadB = %s/%s, want beadA1/beadB1", rows[0].BeadA, rows[0].BeadB)
	}
}

// TestGetClinicalLinksForPatient_NoResultsIsEmptyNotError confirms a
// patient_root with zero links returns an empty (nil) slice, not an error —
// a real, reachable case for a patient with no derived clinical_links rows
// yet.
func TestGetClinicalLinksForPatient_NoResultsIsEmptyNotError(t *testing.T) {
	db := openT(t)
	rows, err := db.GetClinicalLinksForPatient("nonexistent-patient")
	if err != nil {
		t.Fatalf("GetClinicalLinksForPatient: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("rows = %v, want empty", rows)
	}
}

// TestGetClinicalLinksForPatient_UsesPatientSevIndex runs EXPLAIN QUERY PLAN
// against the exact query GetClinicalLinksForPatient issues and asserts it
// reports using idx_clinical_links_patient_sev, not a full table scan — the
// R7a task's explicit "confirm via EXPLAIN QUERY PLAN" requirement
// (specs/R7_graph_view.md).
func TestGetClinicalLinksForPatient_UsesPatientSevIndex(t *testing.T) {
	db := openT(t)
	seedClinicalLinkRow(t, db, "link1", "beadA1", "beadB1", "patientX", "cooccurrence", "loinc:1234", "info", "cooccurrence", "")

	rows, err := db.sqlDB.Query(`EXPLAIN QUERY PLAN
		SELECT link_id, bead_a, bead_b, relation, matched_tag,
		       severity, evidence_basis, evidence_bead_ids,
		       COALESCE(rule_id, ''), COALESCE(rule_version, ''), created_at
		FROM clinical_links
		WHERE patient_root = ?
		ORDER BY created_at, matched_tag`, "patientX")
	if err != nil {
		t.Fatalf("EXPLAIN QUERY PLAN: %v", err)
	}
	defer rows.Close()

	var plan []string
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatalf("scan EXPLAIN QUERY PLAN row: %v", err)
		}
		plan = append(plan, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("EXPLAIN QUERY PLAN: %v", err)
	}

	full := strings.Join(plan, " | ")
	t.Logf("EXPLAIN QUERY PLAN: %s", full)
	if !strings.Contains(full, "idx_clinical_links_patient_sev") {
		t.Errorf("query plan does not mention idx_clinical_links_patient_sev, want index use: %s", full)
	}
	if strings.Contains(full, "SCAN clinical_links") {
		t.Errorf("query plan is a full table SCAN, want an index SEARCH: %s", full)
	}
}

// TestListPatientBeadsForGraph_JoinsRecordedAtAndStatus confirms
// recorded_at + bead_status (status/current_bead_id) are joined in for each
// row, and that a Bead with no bead_status row at all gets the empty-string
// zero value (the "absent" case the caller must treat as active) rather
// than an error.
func TestListPatientBeadsForGraph_JoinsRecordedAtAndStatus(t *testing.T) {
	db := openT(t)

	p1 := testBead(t, "patient_registration", "Patient One", nil, nil)
	indexBeadT(t, db, p1, BeadLocation{PodPath: "pods/p1.pod", PatientRoot: p1.ID, Offset: 0, Length: 100, WrittenAt: "2026-03-01T10:00:01Z"})

	obsAmended := testBead(t, "fhir_observation", "old BP reading", []string{p1.ID}, nil)
	indexBeadT(t, db, obsAmended, BeadLocation{PodPath: "pods/p1.pod", PatientRoot: p1.ID, Offset: 100, Length: 100, WrittenAt: "2026-03-01T10:00:02Z"})
	seedBeadStatusRow(t, db, obsAmended.ID, "amended", "current-bead-id-xyz")

	rows, err := db.ListPatientBeadsForGraph(p1.ID)
	if err != nil {
		t.Fatalf("ListPatientBeadsForGraph: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}

	byID := make(map[string]GraphBeadRow, len(rows))
	for _, r := range rows {
		byID[r.ID] = r
	}

	// p1 has no bead_status row: absent = active fallback, zero-value fields.
	if got := byID[p1.ID]; got.Status != "" || got.CurrentBeadID != "" {
		t.Errorf("p1 status/current_bead_id = %q/%q, want empty (absent = active)", got.Status, got.CurrentBeadID)
	}
	if byID[p1.ID].RecordedAt != "2026-03-01T10:00:01Z" {
		t.Errorf("p1 RecordedAt = %q, want the seeded WrittenAt", byID[p1.ID].RecordedAt)
	}

	// obsAmended's bead_status row must round-trip.
	if got := byID[obsAmended.ID]; got.Status != "amended" || got.CurrentBeadID != "current-bead-id-xyz" {
		t.Errorf("obsAmended status/current_bead_id = %q/%q, want amended/current-bead-id-xyz", got.Status, got.CurrentBeadID)
	}
}

// TestListPatientBeadsForGraph_OrdersByTimestamp confirms rows come back
// timestamp-ascending, matching ListPatientBeads' own order (R7a's contract:
// beads[] "timestamp 昇順").
func TestListPatientBeadsForGraph_OrdersByTimestamp(t *testing.T) {
	db := openT(t)

	p1 := testBead(t, "patient_registration", "Patient", nil, nil)
	indexBeadT(t, db, p1, BeadLocation{PodPath: "pods/p1.pod", PatientRoot: p1.ID, Offset: 0, Length: 100})

	// testBead always stamps the same fixed Timestamp; seed distinct
	// timestamps directly via a second bead built with different content to
	// get a different content-hash ID, then override its beads.timestamp row
	// post-insert so ordering is actually exercised.
	later := testBead(t, "fhir_observation", "later", []string{p1.ID}, nil)
	indexBeadT(t, db, later, BeadLocation{PodPath: "pods/p1.pod", PatientRoot: p1.ID, Offset: 100, Length: 100})
	if _, err := db.sqlDB.Exec(`UPDATE beads SET timestamp = '2026-06-01T00:00:00Z' WHERE id = ?`, later.ID); err != nil {
		t.Fatalf("update timestamp: %v", err)
	}

	rows, err := db.ListPatientBeadsForGraph(p1.ID)
	if err != nil {
		t.Fatalf("ListPatientBeadsForGraph: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if rows[0].ID != p1.ID || rows[1].ID != later.ID {
		t.Errorf("order = [%s, %s], want [p1, later] (timestamp ascending)", rows[0].ID, rows[1].ID)
	}
}

// TestGetParentEdgesForPatient_ScopesByPatientAndParentOnly confirms only
// 'parent' edges whose CHILD is under patientRoot are returned, and that a
// hypothetical 'sibling'-typed edge (dead code path, never written by
// current IndexBead, but the column still accepts it) would be excluded —
// specs/R7_graph_view.md's "sibling は死文化なので出さない".
func TestGetParentEdgesForPatient_ScopesByPatientAndParentOnly(t *testing.T) {
	db := openT(t)

	p1 := testBead(t, "patient_registration", "Patient One", nil, nil)
	indexBeadT(t, db, p1, BeadLocation{PodPath: "pods/p1.pod", PatientRoot: p1.ID, Offset: 0, Length: 100})
	child := testBead(t, "fhir_observation", "obs", []string{p1.ID}, nil)
	indexBeadT(t, db, child, BeadLocation{PodPath: "pods/p1.pod", PatientRoot: p1.ID, Offset: 100, Length: 100})

	p2 := testBead(t, "patient_registration", "Patient Two", nil, nil)
	indexBeadT(t, db, p2, BeadLocation{PodPath: "pods/p2.pod", PatientRoot: p2.ID, Offset: 0, Length: 100})
	child2 := testBead(t, "fhir_observation", "obs2", []string{p2.ID}, nil)
	indexBeadT(t, db, child2, BeadLocation{PodPath: "pods/p2.pod", PatientRoot: p2.ID, Offset: 100, Length: 100})

	// A dead-code 'sibling' edge row, inserted directly (IndexBead itself
	// never writes edge_type='sibling' — see write.go), to confirm the query
	// filters it out even though the column would technically accept it.
	if _, err := db.sqlDB.Exec(
		`INSERT INTO bead_edges (child_id, parent_id, edge_type) VALUES (?, ?, 'sibling')`,
		child.ID, child2.ID,
	); err != nil {
		t.Fatalf("insert sibling edge: %v", err)
	}

	edges, err := db.GetParentEdgesForPatient(p1.ID)
	if err != nil {
		t.Fatalf("GetParentEdgesForPatient: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("edges = %v, want exactly 1 (child->p1 parent edge, not the sibling row, not p2's edge)", edges)
	}
	if edges[0].ChildID != child.ID || edges[0].ParentID != p1.ID {
		t.Errorf("edge = %+v, want {ChildID: %s, ParentID: %s}", edges[0], child.ID, p1.ID)
	}
}
