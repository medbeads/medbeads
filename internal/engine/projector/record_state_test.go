package projector_test

import (
	"testing"

	"github.com/medbeads/medbeads/internal/engine/bead"
	"github.com/medbeads/medbeads/internal/engine/index"
	"github.com/medbeads/medbeads/internal/engine/projector"
)

// beadStatusRow / queryBeadStatus / activeConditionRow / queryActiveConditions
// / activeMedicationRow / queryActiveMedications are this file's own read-back
// helpers, mirroring reproject_test.go's queryClinicalLinks convention.

type beadStatusRow struct {
	BeadID            string
	Status            string
	CurrentBeadID     string
	SupersededBy      string
	AttestationBeadID string
	RetractionBeadID  string
	ProjectionRunID   string
}

func queryBeadStatus(t *testing.T, db *index.DB, beadID string) beadStatusRow {
	t.Helper()
	var r beadStatusRow
	r.BeadID = beadID
	err := db.SQLDB().QueryRow(`
		SELECT status, COALESCE(current_bead_id, ''), COALESCE(superseded_by, ''),
		       COALESCE(attestation_bead_id, ''), COALESCE(retraction_bead_id, ''),
		       COALESCE(projection_run_id, '')
		FROM bead_status WHERE bead_id = ?`, beadID,
	).Scan(&r.Status, &r.CurrentBeadID, &r.SupersededBy, &r.AttestationBeadID, &r.RetractionBeadID, &r.ProjectionRunID)
	if err != nil {
		t.Fatalf("queryBeadStatus(%s): %v", beadID, err)
	}
	return r
}

func queryBeadStatusCount(t *testing.T, db *index.DB, patientRoot string) int {
	t.Helper()
	return countRows(t, db, `SELECT COUNT(*) FROM bead_status WHERE patient_root = ?`, patientRoot)
}

type activeConditionRow struct {
	BeadID             string
	CurrentBeadID      string
	ClinicalStatus     string
	VerificationStatus string
}

func queryActiveConditions(t *testing.T, db *index.DB, patientRoot string) []activeConditionRow {
	t.Helper()
	rows, err := db.SQLDB().Query(`
		SELECT bead_id, current_bead_id, COALESCE(clinical_status, ''), COALESCE(verification_status, '')
		FROM active_conditions WHERE patient_root = ? ORDER BY bead_id`, patientRoot)
	if err != nil {
		t.Fatalf("queryActiveConditions: %v", err)
	}
	defer rows.Close()
	var out []activeConditionRow
	for rows.Next() {
		var r activeConditionRow
		if err := rows.Scan(&r.BeadID, &r.CurrentBeadID, &r.ClinicalStatus, &r.VerificationStatus); err != nil {
			t.Fatalf("queryActiveConditions: scan: %v", err)
		}
		out = append(out, r)
	}
	return out
}

type activeMedicationRow struct {
	BeadID           string
	CurrentBeadID    string
	MedicationStatus string
	Intent           string
}

func queryActiveMedications(t *testing.T, db *index.DB, patientRoot string) []activeMedicationRow {
	t.Helper()
	rows, err := db.SQLDB().Query(`
		SELECT bead_id, current_bead_id, COALESCE(medication_status, ''), COALESCE(intent, '')
		FROM active_medications WHERE patient_root = ? ORDER BY bead_id`, patientRoot)
	if err != nil {
		t.Fatalf("queryActiveMedications: %v", err)
	}
	defer rows.Close()
	var out []activeMedicationRow
	for rows.Next() {
		var r activeMedicationRow
		if err := rows.Scan(&r.BeadID, &r.CurrentBeadID, &r.MedicationStatus, &r.Intent); err != nil {
			t.Fatalf("queryActiveMedications: scan: %v", err)
		}
		out = append(out, r)
	}
	return out
}

// --- basic: a plain fhir_condition Bead with no correction resolves active,
// and (since content.clinicalStatus=="active") gets an active_conditions row.

func TestStatusReproject_PlainActiveCondition_WritesActiveConditionRow(t *testing.T) {
	e := openT(t)
	root := seedPatient(t, e, "patient A")
	cond := ingestT(t, e, unsavedBead("fhir_condition", []string{root.ID}, map[string]any{
		"clinicalStatus": "active", "verificationStatus": "confirmed",
	}))

	res, err := projector.StatusReproject(e.Index(), e, "test-code-v1", "2026-07-11T00:00:00Z")
	if err != nil {
		t.Fatalf("StatusReproject: %v", err)
	}
	if res.PatientsProjected != 1 {
		t.Errorf("PatientsProjected = %d, want 1", res.PatientsProjected)
	}

	st := queryBeadStatus(t, e.Index(), cond.ID)
	if st.Status != "active" {
		t.Errorf("status = %q, want active", st.Status)
	}
	if st.CurrentBeadID != cond.ID {
		t.Errorf("current_bead_id = %q, want %q", st.CurrentBeadID, cond.ID)
	}

	conds := queryActiveConditions(t, e.Index(), root.ID)
	if len(conds) != 1 {
		t.Fatalf("active_conditions rows = %d, want 1: %+v", len(conds), conds)
	}
	if conds[0].BeadID != cond.ID || conds[0].CurrentBeadID != cond.ID {
		t.Errorf("active_conditions row = %+v, want bead_id/current_bead_id = %s", conds[0], cond.ID)
	}
	if conds[0].ClinicalStatus != "active" || conds[0].VerificationStatus != "confirmed" {
		t.Errorf("active_conditions row clinical/verification status = %q/%q, want active/confirmed",
			conds[0].ClinicalStatus, conds[0].VerificationStatus)
	}
}

// --- a fhir_condition Bead whose content.clinicalStatus is NOT "active" (a
// resolved/inactive condition) must NOT get an active_conditions row, even
// though its bead_status is "active" (the two axes are independent — the
// record itself is not retracted/amended, but the clinical fact it describes
// is not currently active).

func TestStatusReproject_ResolvedClinicalStatus_NoActiveConditionRow(t *testing.T) {
	e := openT(t)
	root := seedPatient(t, e, "patient A")
	cond := ingestT(t, e, unsavedBead("fhir_condition", []string{root.ID}, map[string]any{
		"clinicalStatus": "resolved", "verificationStatus": "confirmed",
	}))

	if _, err := projector.StatusReproject(e.Index(), e, "test-code-v1", "2026-07-11T00:00:00Z"); err != nil {
		t.Fatalf("StatusReproject: %v", err)
	}

	st := queryBeadStatus(t, e.Index(), cond.ID)
	if st.Status != "active" {
		t.Errorf("bead_status = %q, want active (record itself is not retracted/amended)", st.Status)
	}
	conds := queryActiveConditions(t, e.Index(), root.ID)
	if len(conds) != 0 {
		t.Fatalf("active_conditions rows = %d, want 0 (content.clinicalStatus=='resolved', not 'active')", len(conds))
	}
}

// --- fhir_medicationrequest with content.status=="active" gets an
// active_medications row.

func TestStatusReproject_PlainActiveMedication_WritesActiveMedicationRow(t *testing.T) {
	e := openT(t)
	root := seedPatient(t, e, "patient A")
	rx := ingestT(t, e, unsavedBead("fhir_medicationrequest", []string{root.ID}, map[string]any{
		"status": "active", "intent": "order",
	}))

	if _, err := projector.StatusReproject(e.Index(), e, "test-code-v1", "2026-07-11T00:00:00Z"); err != nil {
		t.Fatalf("StatusReproject: %v", err)
	}

	meds := queryActiveMedications(t, e.Index(), root.ID)
	if len(meds) != 1 {
		t.Fatalf("active_medications rows = %d, want 1: %+v", len(meds), meds)
	}
	if meds[0].BeadID != rx.ID || meds[0].MedicationStatus != "active" || meds[0].Intent != "order" {
		t.Errorf("active_medications row = %+v, want bead_id=%s status=active intent=order", meds[0], rx.ID)
	}
}

// --- must-fix (a): an UNATTESTED amender does not supersede its target -----

func TestStatusReproject_UnattestedAmendmentDoesNotSupersedeTarget(t *testing.T) {
	e := openT(t)
	root := seedPatient(t, e, "patient A")
	cond := ingestT(t, e, unsavedBead("fhir_condition", []string{root.ID}, map[string]any{"clinicalStatus": "active"}))
	amendment := ingestT(t, e, bead.Bead{
		Type: "fhir_condition", Timestamp: nextTimestamp(), Author: "did:medbeads:doctor:12345",
		Parents: []string{root.ID}, Amends: []string{cond.ID},
		Content: map[string]any{"clinicalStatus": "active"},
	})

	res, err := projector.StatusReproject(e.Index(), e, "test-code-v1", "2026-07-11T00:00:00Z")
	if err != nil {
		t.Fatalf("StatusReproject: %v", err)
	}

	condSt := queryBeadStatus(t, e.Index(), cond.ID)
	if condSt.Status != "active" {
		t.Errorf("target status = %q, want active (unattested amender must not supersede)", condSt.Status)
	}
	amendSt := queryBeadStatus(t, e.Index(), amendment.ID)
	if amendSt.Status != "unattested" {
		t.Errorf("amendment status = %q, want unattested", amendSt.Status)
	}

	// The original condition remains in active_conditions (it is still the
	// current, unsuperseded active fact); the unattested amendment itself
	// must NOT appear in active_conditions (its bead_status is not "active").
	conds := queryActiveConditions(t, e.Index(), root.ID)
	if len(conds) != 1 || conds[0].BeadID != cond.ID {
		t.Fatalf("active_conditions = %+v, want exactly [%s]", conds, cond.ID)
	}
	if res.BeadStatusWritten < 2 {
		t.Errorf("BeadStatusWritten = %d, want >= 2 (patient_registration + condition + amendment)", res.BeadStatusWritten)
	}
}

// --- must-fix (b): a RETRACTED leaf terminates an amends chain ------------

func TestStatusReproject_RetractedAmendmentTerminatesChain(t *testing.T) {
	e := openT(t)
	root := seedPatient(t, e, "patient A")
	cond := ingestT(t, e, unsavedBead("fhir_condition", []string{root.ID}, map[string]any{"clinicalStatus": "active"}))
	amendment := ingestT(t, e, bead.Bead{
		Type: "fhir_condition", Timestamp: nextTimestamp(), Author: "did:medbeads:doctor:12345",
		Parents: []string{root.ID}, Amends: []string{cond.ID},
		Content: map[string]any{"clinicalStatus": "active"},
	})
	// Attestation must name its subject in Parents too (U4a's "穴1" fix).
	ingestT(t, e, bead.Bead{
		Type: "attestation", Timestamp: nextTimestamp(), Author: "did:medbeads:doctor:99999",
		Parents: []string{amendment.ID, root.ID},
		Content: map[string]any{"verdict": "approved"},
	})
	// Retraction must also name its subject (amendment) in Parents.
	ingestT(t, e, bead.Bead{
		Type: "retraction", Timestamp: nextTimestamp(), Author: "did:medbeads:doctor:12345",
		Parents: []string{amendment.ID, root.ID}, Retracts: []string{amendment.ID},
		Content: map[string]any{"reason_code": "entered-in-error", "authorized_by": "did:medbeads:doctor:12345"},
	})

	if _, err := projector.StatusReproject(e.Index(), e, "test-code-v1", "2026-07-11T00:00:00Z"); err != nil {
		t.Fatalf("StatusReproject: %v", err)
	}

	amendSt := queryBeadStatus(t, e.Index(), amendment.ID)
	if amendSt.Status != "retracted" {
		t.Fatalf("amendment status = %q, want retracted", amendSt.Status)
	}
	if amendSt.CurrentBeadID != "" {
		t.Errorf("amendment current_bead_id = %q, want \"\" (NULL)", amendSt.CurrentBeadID)
	}

	condSt := queryBeadStatus(t, e.Index(), cond.ID)
	if condSt.Status != "amended" {
		t.Fatalf("target status = %q, want amended (it WAS validly superseded before the amendment was retracted)", condSt.Status)
	}
	if condSt.CurrentBeadID != "" {
		t.Errorf("target current_bead_id = %q, want \"\" (NULL — retracted leaf terminates the chain)", condSt.CurrentBeadID)
	}
	if condSt.SupersededBy != amendment.ID {
		t.Errorf("target superseded_by = %q, want %q", condSt.SupersededBy, amendment.ID)
	}

	// Neither the original condition (amended, current_bead_id=NULL) nor the
	// retracted amendment should appear in active_conditions.
	conds := queryActiveConditions(t, e.Index(), root.ID)
	if len(conds) != 0 {
		t.Errorf("active_conditions = %+v, want 0 rows (chain has no active current version)", conds)
	}
}

// --- must-fix (c): retraction beats a newer amends --------------------------

func TestStatusReproject_RetractionBeatsNewerAmends(t *testing.T) {
	e := openT(t)
	root := seedPatient(t, e, "patient A")
	cond := ingestT(t, e, unsavedBead("fhir_condition", []string{root.ID}, map[string]any{"clinicalStatus": "active"}))
	ingestT(t, e, bead.Bead{
		Type: "retraction", Timestamp: nextTimestamp(), Author: "did:medbeads:doctor:12345",
		Parents: []string{cond.ID, root.ID}, Retracts: []string{cond.ID},
		Content: map[string]any{"reason_code": "entered-in-error", "authorized_by": "did:medbeads:doctor:12345"},
	})
	// A LATER (both by ingest order and hence recorded_at) amendment still
	// naming the now-retracted condition — must not resurrect it.
	laterAmendment := ingestT(t, e, bead.Bead{
		Type: "fhir_condition", Timestamp: nextTimestamp(), Author: "did:medbeads:doctor:12345",
		Parents: []string{root.ID}, Amends: []string{cond.ID},
		Content: map[string]any{"clinicalStatus": "active"},
	})
	ingestT(t, e, bead.Bead{
		Type: "attestation", Timestamp: nextTimestamp(), Author: "did:medbeads:doctor:99999",
		Parents: []string{laterAmendment.ID, root.ID},
		Content: map[string]any{"verdict": "approved"},
	})

	if _, err := projector.StatusReproject(e.Index(), e, "test-code-v1", "2026-07-11T00:00:00Z"); err != nil {
		t.Fatalf("StatusReproject: %v", err)
	}

	condSt := queryBeadStatus(t, e.Index(), cond.ID)
	if condSt.Status != "retracted" {
		t.Errorf("target status = %q, want retracted (retraction must beat a later amends)", condSt.Status)
	}
	if condSt.CurrentBeadID != "" {
		t.Errorf("target current_bead_id = %q, want \"\" (NULL)", condSt.CurrentBeadID)
	}
}

// --- cross-Pod retraction fixture: a retraction Bead's Parents must include
// its subject (U4a's "穴1" fix) — if Ingest ever regressed and allowed a
// retraction with empty Parents to land in the shared Pod, the projector
// would silently miss it (ListPatientBeads(patientRoot) would never see it).
// This test proves Ingest itself rejects that shape, which is what keeps the
// projector's per-patient Bead enumeration complete.

func TestStatusReproject_RetractionWithoutSubjectInParents_RejectedAtIngest(t *testing.T) {
	e := openT(t)
	root := seedPatient(t, e, "patient A")
	cond := ingestT(t, e, unsavedBead("fhir_condition", []string{root.ID}, map[string]any{"clinicalStatus": "active"}))

	_, err := e.Ingest(bead.Bead{
		Type: "retraction", Timestamp: nextTimestamp(), Author: "did:medbeads:doctor:12345",
		// Parents deliberately empty/omitted — only Retracts names the subject.
		Retracts: []string{cond.ID},
		Content:  map[string]any{"reason_code": "entered-in-error", "authorized_by": "did:medbeads:doctor:12345"},
	})
	if err == nil {
		t.Fatal("Ingest of a retraction Bead with empty Parents succeeded, want rejection " +
			"(it would resolve to the shared Pod, escaping per-patient scoping — see requireSubjectInParents)")
	}
}

// --- determinism: two independent StatusReproject builds of the identical
// patient Bead set yield byte-identical bead_status (excluding
// projection_run_id) -------------------------------------------------------

func TestStatusReproject_Deterministic_SameInputsSameOutput(t *testing.T) {
	build := func(t *testing.T) (beadStatusRow, beadStatusRow, string) {
		e := openT(t)
		root := ingestT(t, e, bead.Bead{
			Type: "patient_registration", Timestamp: "2026-01-01T00:00:00Z",
			Author: "did:medbeads:doctor:12345", Content: map[string]any{"name": "patient A"},
		})
		cond := ingestT(t, e, bead.Bead{
			Type: "fhir_condition", Timestamp: "2026-01-05T00:00:00Z", Author: "did:medbeads:doctor:12345",
			Parents: []string{root.ID}, Content: map[string]any{"clinicalStatus": "active"},
		})
		amendment := ingestT(t, e, bead.Bead{
			Type: "fhir_condition", Timestamp: "2026-01-06T00:00:00Z", Author: "did:medbeads:doctor:12345",
			Parents: []string{root.ID}, Amends: []string{cond.ID},
			Content: map[string]any{"clinicalStatus": "active"},
		})
		ingestT(t, e, bead.Bead{
			Type: "attestation", Timestamp: "2026-01-07T00:00:00Z", Author: "did:medbeads:doctor:99999",
			Parents: []string{amendment.ID, root.ID}, Content: map[string]any{"verdict": "approved"},
		})

		if _, err := projector.StatusReproject(e.Index(), e, "test-code-v1", "2026-07-11T00:00:00Z"); err != nil {
			t.Fatalf("StatusReproject: %v", err)
		}
		return queryBeadStatus(t, e.Index(), cond.ID), queryBeadStatus(t, e.Index(), amendment.ID), amendment.ID
	}

	condA, amendA, amendIDA := build(t)
	condB, amendB, amendIDB := build(t)

	if amendIDA != amendIDB {
		t.Fatalf("amendment Bead IDs differ across two independent builds (%s vs %s) — content-addressing itself is non-deterministic, "+
			"which would invalidate this whole test's premise", amendIDA, amendIDB)
	}

	condA.ProjectionRunID, condB.ProjectionRunID = "", ""
	amendA.ProjectionRunID, amendB.ProjectionRunID = "", ""
	if condA != condB {
		t.Errorf("condition bead_status differs across runs:\n run1=%+v\n run2=%+v", condA, condB)
	}
	if amendA != amendB {
		t.Errorf("amendment bead_status differs across runs:\n run1=%+v\n run2=%+v", amendA, amendB)
	}
}

// --- manifest flip: exactly one 'active' record_state_v31 manifest row -----

func TestStatusReproject_ManifestFlip_OneActivePerProjectionName(t *testing.T) {
	e := openT(t)
	root := seedPatient(t, e, "patient A")
	ingestT(t, e, unsavedBead("fhir_condition", []string{root.ID}, map[string]any{"clinicalStatus": "active"}))

	res1, err := projector.StatusReproject(e.Index(), e, "test-code-v1", "2026-07-11T00:00:00Z")
	if err != nil {
		t.Fatalf("first StatusReproject: %v", err)
	}
	// patient_registration + fhir_condition = 2 bead_status rows for this patient.
	if n := queryBeadStatusCount(t, e.Index(), root.ID); n != 2 {
		t.Fatalf("bead_status rows for patient after first StatusReproject = %d, want 2", n)
	}
	if n := countRows(t, e.Index(),
		`SELECT COUNT(*) FROM projection_manifest WHERE status = 'active' AND projection_name = ?`,
		projector.StatusProjectionName); n != 1 {
		t.Fatalf("active manifests after first StatusReproject = %d, want 1", n)
	}

	res2, err := projector.StatusReproject(e.Index(), e, "test-code-v2", "2026-07-11T00:00:01Z")
	if err != nil {
		t.Fatalf("second StatusReproject: %v", err)
	}
	if res2.RunID == res1.RunID {
		t.Fatalf("second run reused run_id %s from first", res1.RunID)
	}
	if n := countRows(t, e.Index(),
		`SELECT COUNT(*) FROM projection_manifest WHERE status = 'active' AND projection_name = ?`,
		projector.StatusProjectionName); n != 1 {
		t.Fatalf("active manifests after second StatusReproject = %d, want 1", n)
	}

	// clinical_links_v31's own manifest lineage must be untouched by this
	// projector (they are separate lineages per StatusProjectionName's own
	// doc comment) — no clinical_links_v31 manifest row should exist at all
	// since Reproject was never called in this test.
	if n := countRows(t, e.Index(),
		`SELECT COUNT(*) FROM projection_manifest WHERE projection_name = ?`,
		projector.ProjectionName); n != 0 {
		t.Errorf("clinical_links_v31 manifest rows = %d, want 0 (StatusReproject must not touch Reproject's lineage)", n)
	}
}
