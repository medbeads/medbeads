package index

import "testing"

// TestBeadStatusFor_ResolvesRowsAndLeavesAbsentIDsOut pins BeadStatusFor's
// own contract (specs/U5_api_retrieve.md's U5b section): every id present in
// bead_status is returned with its status/current_bead_id, and an id with no
// bead_status row at all is simply absent from the map (not an error, not a
// zero-value entry) — the caller-side "absent = active" convention this
// function's own doc comment documents.
func TestBeadStatusFor_ResolvesRowsAndLeavesAbsentIDsOut(t *testing.T) {
	db := openT(t)

	active := testBead(t, "fhir_condition", "active fact", nil, nil)
	indexBeadT(t, db, active, BeadLocation{PodPath: "pods/_shared.pod", PatientRoot: "patientroot1", Offset: 0, Length: 100})

	amended := testBead(t, "fhir_condition", "amended fact", nil, nil)
	indexBeadT(t, db, amended, BeadLocation{PodPath: "pods/_shared.pod", PatientRoot: "patientroot1", Offset: 100, Length: 100})
	current := testBead(t, "fhir_condition", "current version", nil, nil)
	indexBeadT(t, db, current, BeadLocation{PodPath: "pods/_shared.pod", PatientRoot: "patientroot1", Offset: 200, Length: 100})

	retracted := testBead(t, "fhir_condition", "retracted fact", nil, nil)
	indexBeadT(t, db, retracted, BeadLocation{PodPath: "pods/_shared.pod", PatientRoot: "patientroot1", Offset: 300, Length: 100})

	// Only 3 of the 4 seeded Beads get a bead_status row (mirroring a store
	// where StatusReproject wrote some but not this test's 4th "absent" one);
	// the fourth (absentID) never gets a row.
	if _, err := db.sqlDB.Exec(
		`INSERT INTO bead_status (bead_id, status, current_bead_id, patient_root) VALUES (?, 'active', ?, ?)`,
		active.ID, active.ID, "patientroot1",
	); err != nil {
		t.Fatalf("seed bead_status active: %v", err)
	}
	if _, err := db.sqlDB.Exec(
		`INSERT INTO bead_status (bead_id, status, current_bead_id, patient_root) VALUES (?, 'amended', ?, ?)`,
		amended.ID, current.ID, "patientroot1",
	); err != nil {
		t.Fatalf("seed bead_status amended: %v", err)
	}
	if _, err := db.sqlDB.Exec(
		`INSERT INTO bead_status (bead_id, status, current_bead_id, patient_root) VALUES (?, 'retracted', NULL, ?)`,
		retracted.ID, "patientroot1",
	); err != nil {
		t.Fatalf("seed bead_status retracted: %v", err)
	}
	const absentID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" // 64 hex chars, never indexed

	statuses, err := db.BeadStatusFor([]string{active.ID, amended.ID, retracted.ID, absentID})
	if err != nil {
		t.Fatalf("BeadStatusFor: %v", err)
	}

	if len(statuses) != 3 {
		t.Fatalf("BeadStatusFor returned %d entries, want 3 (absent id must not appear): %+v", len(statuses), statuses)
	}
	if _, ok := statuses[absentID]; ok {
		t.Errorf("BeadStatusFor included the never-indexed id %s; want it simply absent from the map", absentID)
	}

	got, ok := statuses[active.ID]
	if !ok || got.Status != "active" || got.CurrentBeadID != active.ID {
		t.Errorf("statuses[active] = %+v (ok=%v), want status=active current_bead_id=%s", got, ok, active.ID)
	}
	got, ok = statuses[amended.ID]
	if !ok || got.Status != "amended" || got.CurrentBeadID != current.ID {
		t.Errorf("statuses[amended] = %+v (ok=%v), want status=amended current_bead_id=%s", got, ok, current.ID)
	}
	got, ok = statuses[retracted.ID]
	if !ok || got.Status != "retracted" || got.CurrentBeadID != "" {
		t.Errorf("statuses[retracted] = %+v (ok=%v), want status=retracted current_bead_id=\"\" (NULL)", got, ok)
	}
}

// TestBeadStatusFor_EmptyIDs_NoQuery checks the zero-ids short-circuit (no
// query issued at all — mirrors PatientRootsFor's identical convention).
func TestBeadStatusFor_EmptyIDs_NoQuery(t *testing.T) {
	db := openT(t)
	statuses, err := db.BeadStatusFor(nil)
	if err != nil {
		t.Fatalf("BeadStatusFor(nil): %v", err)
	}
	if len(statuses) != 0 {
		t.Errorf("BeadStatusFor(nil) = %+v, want empty map", statuses)
	}
}

// TestBeadStatusTableEmpty reports true on a fresh store and false once at
// least one row exists — the signal resolveBeadStatuses (mcpserver) uses to
// distinguish "StatusReproject never ran" from an individual absent id.
func TestBeadStatusTableEmpty(t *testing.T) {
	db := openT(t)

	empty, err := db.BeadStatusTableEmpty()
	if err != nil {
		t.Fatalf("BeadStatusTableEmpty (fresh store): %v", err)
	}
	if !empty {
		t.Errorf("BeadStatusTableEmpty = false on a fresh store, want true")
	}

	b := testBead(t, "fhir_condition", "some fact", nil, nil)
	indexBeadT(t, db, b, BeadLocation{PodPath: "pods/_shared.pod", PatientRoot: "patientroot1", Offset: 0, Length: 100})
	if _, err := db.sqlDB.Exec(
		`INSERT INTO bead_status (bead_id, status, current_bead_id, patient_root) VALUES (?, 'active', ?, ?)`,
		b.ID, b.ID, "patientroot1",
	); err != nil {
		t.Fatalf("seed bead_status: %v", err)
	}

	empty, err = db.BeadStatusTableEmpty()
	if err != nil {
		t.Fatalf("BeadStatusTableEmpty (populated store): %v", err)
	}
	if empty {
		t.Errorf("BeadStatusTableEmpty = true after inserting a row, want false")
	}
}
