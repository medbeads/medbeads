package engine

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/medbeads/medbeads/internal/engine/bead"
	"github.com/medbeads/medbeads/internal/engine/index"
	"github.com/medbeads/medbeads/internal/engine/pod"
	"github.com/medbeads/medbeads/internal/engine/projector"
)

func openAutoT(t *testing.T, dir string) *Engine {
	t.Helper()
	e, err := OpenWithOptions(dir, OpenOptions{
		AutoProject:           true,
		ProjectionCodeVersion: DefaultProjectionCodeVersion(),
	})
	if err != nil {
		t.Fatalf("OpenWithOptions(auto): %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	return e
}

func fhirCoding(system, code string) map[string]any {
	return map[string]any{
		"code": map[string]any{
			"coding": []any{map[string]any{"system": system, "code": code}},
		},
	}
}

func TestRollingLinkReprojection_PrioritizesRecentThenInactiveThenDeceased(t *testing.T) {
	e := openAutoT(t, t.TempDir())

	seed := func(name, encounterAt string, deceased bool) bead.Bead {
		content := map[string]any{"name": name}
		if deceased {
			content["deceasedBoolean"] = true
		}
		root, err := e.Ingest(unsavedBead("patient_registration", nil, content))
		if err != nil {
			t.Fatalf("Ingest root %s: %v", name, err)
		}
		encounter := unsavedBead("fhir_encounter", []string{root.ID}, map[string]any{"name": name + " encounter"})
		encounter.Timestamp = encounterAt
		if _, err := e.Ingest(encounter); err != nil {
			t.Fatalf("Ingest encounter %s: %v", name, err)
		}
		return root
	}

	recent := seed("recent", "2026-07-01T00:00:00Z", false)
	inactive := seed("inactive", "2020-01-01T00:00:00Z", false)
	deceased := seed("deceased", "2026-07-10T00:00:00Z", true)

	gen, found, err := e.activeGeneration(projector.ProjectionName)
	if err != nil || !found {
		t.Fatalf("active link generation: found=%t err=%v", found, err)
	}
	curated, err := e.Ingest(projector.BuildCuratedPairRuleBead(
		"test-priority-rule", "test_relation", "warning",
		[][2]string{{"atc:test-a", "atc:test-b"}}, "2026-07-14T00:00:00Z",
	))
	if err != nil {
		t.Fatalf("Ingest curated rule: %v", err)
	}
	knowledge := append(append([]string(nil), gen.KnowledgeBeadIDs...), curated.ID)
	activation, err := e.ActivateLinkKnowledge(knowledge, gen.CodeVersion, "2026-07-14T01:00:00Z")
	if err != nil {
		t.Fatalf("ActivateLinkKnowledge: %v", err)
	}
	if activation.QueuedPatients != 3 {
		t.Fatalf("queued patients = %d, want 3", activation.QueuedPatients)
	}

	now := time.Date(2026, 7, 14, 2, 0, 0, 0, time.UTC)
	wantOrder := []struct {
		root bead.Bead
		tier string
	}{
		{recent, "recent"},
		{inactive, "inactive"},
		{deceased, "deceased"},
	}
	for i, want := range wantOrder {
		batch, err := e.ProcessLinkReprojectionQueue(1, now, DefaultReprojectionInactiveAfter)
		if err != nil {
			t.Fatalf("batch %d: %v", i, err)
		}
		if batch.Projected != 1 {
			t.Fatalf("batch %d projected=%d, want 1", i, batch.Projected)
		}
		var runID string
		if err := e.idx.SQLDB().QueryRow(`
			SELECT clinical_links_run_id FROM patient_projection_state
			WHERE patient_root=?`, want.root.ID).Scan(&runID); err != nil {
			t.Fatalf("checkpoint %s: %v", want.tier, err)
		}
		if runID != activation.RunID {
			t.Fatalf("batch %d updated %s out of order: run=%s want=%s", i, want.tier, runID, activation.RunID)
		}
	}
}

func TestRollingLinkReprojection_NewPatientDataBypassesQueue(t *testing.T) {
	e := openAutoT(t, t.TempDir())
	root, err := e.Ingest(unsavedBead("patient_registration", nil, map[string]any{"name": "queued"}))
	if err != nil {
		t.Fatalf("Ingest root: %v", err)
	}
	gen, _, err := e.activeGeneration(projector.ProjectionName)
	if err != nil {
		t.Fatalf("active generation: %v", err)
	}
	curated, err := e.Ingest(projector.BuildCuratedPairRuleBead(
		"test-bypass-rule", "test_relation", "warning",
		[][2]string{{"atc:test-a", "atc:test-b"}}, "2026-07-14T00:00:00Z",
	))
	if err != nil {
		t.Fatalf("Ingest curated rule: %v", err)
	}
	activation, err := e.ActivateLinkKnowledge(
		append(append([]string(nil), gen.KnowledgeBeadIDs...), curated.ID),
		gen.CodeVersion, "2026-07-14T01:00:00Z",
	)
	if err != nil {
		t.Fatalf("ActivateLinkKnowledge: %v", err)
	}

	if _, err := e.Ingest(unsavedBead("fhir_observation", []string{root.ID}, map[string]any{"new": true})); err != nil {
		t.Fatalf("Ingest new patient data: %v", err)
	}
	var runID string
	if err := e.idx.SQLDB().QueryRow(`
		SELECT clinical_links_run_id FROM patient_projection_state
		WHERE patient_root=?`, root.ID).Scan(&runID); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if runID != activation.RunID {
		t.Fatalf("patient run=%s, want new target %s", runID, activation.RunID)
	}
	var queued int
	if err := e.idx.SQLDB().QueryRow(`
		SELECT COUNT(*) FROM patient_reprojection_queue WHERE patient_root=?`, root.ID).Scan(&queued); err != nil {
		t.Fatalf("queue count: %v", err)
	}
	if queued != 0 {
		t.Fatalf("newly updated patient remains queued: count=%d", queued)
	}
}

func TestAutoProjection_LinkCodeChangeDoesNotRebuildRecordState(t *testing.T) {
	dir := t.TempDir()
	e, err := OpenWithOptions(dir, OpenOptions{
		AutoProject:                  true,
		ProjectionCodeVersion:        "links-v1",
		RecordStateProjectionVersion: "record-state-v1",
	})
	if err != nil {
		t.Fatalf("open v1: %v", err)
	}
	root, err := e.Ingest(unsavedBead("patient_registration", nil, map[string]any{"name": "version split"}))
	if err != nil {
		t.Fatalf("Ingest root: %v", err)
	}
	var statusRunBefore string
	if err := e.idx.SQLDB().QueryRow(`
		SELECT record_state_run_id FROM patient_projection_state
		WHERE patient_root=?`, root.ID).Scan(&statusRunBefore); err != nil {
		t.Fatalf("status checkpoint before: %v", err)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("close v1: %v", err)
	}

	reopened, err := OpenWithOptions(dir, OpenOptions{
		AutoProject:                  true,
		ProjectionCodeVersion:        "links-v2",
		RecordStateProjectionVersion: "record-state-v1",
	})
	if err != nil {
		t.Fatalf("open v2: %v", err)
	}
	defer reopened.Close()
	var statusRunAfter string
	if err := reopened.idx.SQLDB().QueryRow(`
		SELECT record_state_run_id FROM patient_projection_state
		WHERE patient_root=?`, root.ID).Scan(&statusRunAfter); err != nil {
		t.Fatalf("status checkpoint after: %v", err)
	}
	if statusRunAfter != statusRunBefore {
		t.Fatalf("link-only code change rebuilt record_state: before=%s after=%s", statusRunBefore, statusRunAfter)
	}
	batch, err := reopened.ProcessLinkReprojectionQueue(0, time.Time{}, DefaultReprojectionInactiveAfter)
	if err != nil {
		t.Fatalf("virtual queue status: %v", err)
	}
	if batch.Remaining != 1 {
		t.Fatalf("link-only code change queued=%d patients, want 1", batch.Remaining)
	}
}

func TestRollingLinkReprojection_FailureIsRetriedDurably(t *testing.T) {
	e := openAutoT(t, t.TempDir())
	root, err := e.Ingest(unsavedBead("patient_registration", nil, map[string]any{"name": "retry"}))
	if err != nil {
		t.Fatalf("Ingest root: %v", err)
	}
	gen, _, err := e.activeGeneration(projector.ProjectionName)
	if err != nil {
		t.Fatalf("active generation: %v", err)
	}
	curated, err := e.Ingest(projector.BuildCuratedPairRuleBead(
		"test-retry-rule", "test_relation", "warning",
		[][2]string{{"atc:test-a", "atc:test-b"}}, "2026-07-14T00:00:00Z",
	))
	if err != nil {
		t.Fatalf("Ingest curated rule: %v", err)
	}
	activation, err := e.ActivateLinkKnowledge(
		append(append([]string(nil), gen.KnowledgeBeadIDs...), curated.ID),
		gen.CodeVersion, "2026-07-14T01:00:00Z",
	)
	if err != nil {
		t.Fatalf("ActivateLinkKnowledge: %v", err)
	}
	if _, err := e.idx.SQLDB().Exec(`
		CREATE TRIGGER test_fail_rolling_checkpoint
		BEFORE UPDATE OF clinical_links_run_id ON patient_projection_state
		WHEN NEW.patient_root='` + root.ID + `' AND NEW.clinical_links_run_id='` + activation.RunID + `'
		BEGIN SELECT RAISE(ABORT, 'injected rolling failure'); END`); err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}

	now := time.Date(2026, 7, 14, 2, 0, 0, 0, time.UTC)
	failed, err := e.ProcessLinkReprojectionQueue(1, now, DefaultReprojectionInactiveAfter)
	if err != nil {
		t.Fatalf("Process failure batch: %v", err)
	}
	if failed.Failed != 1 || failed.Projected != 0 || failed.Remaining != 1 {
		t.Fatalf("failed batch = %+v, want failed=1 projected=0 remaining=1", failed)
	}
	var attempts int
	if err := e.idx.SQLDB().QueryRow(`
		SELECT attempts FROM patient_reprojection_queue WHERE patient_root=?`, root.ID).Scan(&attempts); err != nil {
		t.Fatalf("retry row: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("retry attempts=%d, want 1", attempts)
	}
	if _, err := e.idx.SQLDB().Exec(`DROP TRIGGER test_fail_rolling_checkpoint`); err != nil {
		t.Fatalf("drop failure trigger: %v", err)
	}

	retried, err := e.ProcessLinkReprojectionQueue(1, now.Add(time.Minute), DefaultReprojectionInactiveAfter)
	if err != nil {
		t.Fatalf("Process retry batch: %v", err)
	}
	if retried.Projected != 1 || retried.Remaining != 0 {
		t.Fatalf("retry batch = %+v, want projected=1 remaining=0", retried)
	}
	var retryRows int
	if err := e.idx.SQLDB().QueryRow(`
		SELECT COUNT(*) FROM patient_reprojection_queue WHERE patient_root=?`, root.ID).Scan(&retryRows); err != nil {
		t.Fatalf("retry row count: %v", err)
	}
	if retryRows != 0 {
		t.Fatalf("successful retry left %d retry rows", retryRows)
	}
}

func TestRollingLinkReprojection_LargeBacklogIsVirtualNotMaterialized(t *testing.T) {
	e, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer e.Close()

	tx, err := e.idx.SQLDB().Begin()
	if err != nil {
		t.Fatalf("begin seed: %v", err)
	}
	const patients = 10000
	for i := 0; i < patients; i++ {
		root := fmt.Sprintf("patient-%05d", i)
		if _, err := tx.Exec(`
			INSERT INTO patient_projection_state
				(patient_root, pod_path, indexed_upto, clinical_links_run_id,
				 record_state_run_id, projected_at)
			VALUES (?, ?, 1, 'old-links', 'status', '2026-07-14T00:00:00Z')`,
			root, "pods/"+root+".pod"); err != nil {
			_ = tx.Rollback()
			t.Fatalf("seed checkpoint %d: %v", i, err)
		}
		if _, err := tx.Exec(`
			INSERT INTO patient_activity
				(patient_root, last_clinical_at, deceased_hint, updated_at, last_visit_at)
			VALUES (?, '2026-07-01T00:00:00Z', 0,
			        '2026-07-14T00:00:00Z', '2026-07-01T00:00:00Z')`, root); err != nil {
			_ = tx.Rollback()
			t.Fatalf("seed activity %d: %v", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	stale, err := e.prepareRollingLinkPatients("new-links")
	if err != nil {
		t.Fatalf("prepareRollingLinkPatients: %v", err)
	}
	if stale != patients {
		t.Fatalf("stale patients=%d, want %d", stale, patients)
	}
	var physicalRows int
	if err := e.idx.SQLDB().QueryRow(`SELECT COUNT(*) FROM patient_reprojection_queue`).Scan(&physicalRows); err != nil {
		t.Fatalf("physical queue count: %v", err)
	}
	if physicalRows != 0 {
		t.Fatalf("%d stale patients materialized %d physical queue rows; backlog must remain virtual", patients, physicalRows)
	}
}

func TestAutoProjection_IngestUpdatesOnlyOnePatientAndRecomputesClinicalLinks(t *testing.T) {
	e := openAutoT(t, t.TempDir())

	rootA, err := e.Ingest(unsavedBead("patient_registration", nil, map[string]any{"name": "A"}))
	if err != nil {
		t.Fatalf("Ingest root A: %v", err)
	}
	rootB, err := e.Ingest(unsavedBead("patient_registration", nil, map[string]any{"name": "B"}))
	if err != nil {
		t.Fatalf("Ingest root B: %v", err)
	}

	var bUptoBefore int64
	if err := e.idx.SQLDB().QueryRow(
		`SELECT indexed_upto FROM patient_projection_state WHERE patient_root = ?`, rootB.ID,
	).Scan(&bUptoBefore); err != nil {
		t.Fatalf("patient B checkpoint before: %v", err)
	}

	const rxNorm = "http://www.nlm.nih.gov/research/umls/rxnorm"
	for i := 0; i < 2; i++ {
		content := fhirCoding(rxNorm, "309362") // clopidogrel -> shared atc/risk/rxnorm tags
		content["instance"] = i
		if _, err := e.Ingest(unsavedBead("fhir_medication", []string{rootA.ID}, content)); err != nil {
			t.Fatalf("Ingest medication %d: %v", i, err)
		}
	}

	// The shared medication tags start above the 30% patient-local frequency
	// threshold. Five distinct SNOMED-bearing facts grow the denominator to 7,
	// making the two medication Beads eligible. This proves each append
	// recomputes the complete patient-local link set, not only pairs involving
	// the newest Bead.
	const snomed = "http://snomed.info/sct"
	for i := 0; i < 5; i++ {
		content := fhirCoding(snomed, "noise-"+string(rune('a'+i)))
		if _, err := e.Ingest(unsavedBead("fhir_observation", []string{rootA.ID}, content)); err != nil {
			t.Fatalf("Ingest noise %d: %v", i, err)
		}
	}

	var linksA, linksB int
	if err := e.idx.SQLDB().QueryRow(
		`SELECT COUNT(*) FROM clinical_links WHERE patient_root = ?`, rootA.ID,
	).Scan(&linksA); err != nil {
		t.Fatalf("count patient A links: %v", err)
	}
	if linksA < 3 {
		t.Fatalf("patient A clinical_links = %d, want at least 3 shared atc/risk/rxnorm links", linksA)
	}
	if err := e.idx.SQLDB().QueryRow(
		`SELECT COUNT(*) FROM clinical_links WHERE patient_root = ?`, rootB.ID,
	).Scan(&linksB); err != nil {
		t.Fatalf("count patient B links: %v", err)
	}
	if linksB != 0 {
		t.Fatalf("patient B clinical_links = %d, want 0", linksB)
	}

	var bUptoAfter int64
	if err := e.idx.SQLDB().QueryRow(
		`SELECT indexed_upto FROM patient_projection_state WHERE patient_root = ?`, rootB.ID,
	).Scan(&bUptoAfter); err != nil {
		t.Fatalf("patient B checkpoint after: %v", err)
	}
	if bUptoAfter != bUptoBefore {
		t.Errorf("patient B projection watermark changed during patient A ingest: before=%d after=%d", bUptoBefore, bUptoAfter)
	}

	var podUpto, projectedUpto int64
	if err := e.idx.SQLDB().QueryRow(
		`SELECT p.indexed_upto, s.indexed_upto
		 FROM pods p JOIN patient_projection_state s ON s.patient_root = p.patient_root
		 WHERE p.patient_root = ?`, rootA.ID,
	).Scan(&podUpto, &projectedUpto); err != nil {
		t.Fatalf("patient A watermarks: %v", err)
	}
	if projectedUpto != podUpto {
		t.Errorf("patient A projected_upto=%d, pod indexed_upto=%d", projectedUpto, podUpto)
	}
}

func TestAutoProjection_CorrectionStateIsCurrentWhenIngestReturns(t *testing.T) {
	e := openAutoT(t, t.TempDir())

	root, err := e.Ingest(unsavedBead("patient_registration", nil, map[string]any{"name": "correction"}))
	if err != nil {
		t.Fatalf("Ingest root: %v", err)
	}
	condition, err := e.Ingest(unsavedBead("fhir_condition", []string{root.ID}, map[string]any{
		"clinicalStatus":     "active",
		"verificationStatus": "confirmed",
		"label":              "original",
	}))
	if err != nil {
		t.Fatalf("Ingest condition: %v", err)
	}

	amendmentInput := unsavedBead("fhir_condition", []string{condition.ID}, map[string]any{
		"clinicalStatus":     "active",
		"verificationStatus": "confirmed",
		"label":              "corrected",
	})
	amendmentInput.Amends = []string{condition.ID}
	amendment, err := e.Ingest(amendmentInput)
	if err != nil {
		t.Fatalf("Ingest amendment: %v", err)
	}

	var originalStatus, originalCurrent, amendmentStatus string
	if err := e.idx.SQLDB().QueryRow(
		`SELECT status, current_bead_id FROM bead_status WHERE bead_id = ?`, condition.ID,
	).Scan(&originalStatus, &originalCurrent); err != nil {
		t.Fatalf("original status after unattested amendment: %v", err)
	}
	if originalStatus != "active" || originalCurrent != condition.ID {
		t.Fatalf("original after unattested amendment = (%s,%s), want (active,%s)", originalStatus, originalCurrent, condition.ID)
	}
	if err := e.idx.SQLDB().QueryRow(
		`SELECT status FROM bead_status WHERE bead_id = ?`, amendment.ID,
	).Scan(&amendmentStatus); err != nil {
		t.Fatalf("amendment status: %v", err)
	}
	if amendmentStatus != "unattested" {
		t.Fatalf("amendment status = %s, want unattested", amendmentStatus)
	}

	attestationInput := unsavedBead("attestation", []string{amendment.ID}, map[string]any{"verdict": "approved"})
	if _, err := e.Ingest(attestationInput); err != nil {
		t.Fatalf("Ingest attestation: %v", err)
	}

	if err := e.idx.SQLDB().QueryRow(
		`SELECT status, current_bead_id FROM bead_status WHERE bead_id = ?`, condition.ID,
	).Scan(&originalStatus, &originalCurrent); err != nil {
		t.Fatalf("original status after attestation: %v", err)
	}
	if originalStatus != "amended" || originalCurrent != amendment.ID {
		t.Fatalf("original after attestation = (%s,%s), want (amended,%s)", originalStatus, originalCurrent, amendment.ID)
	}

	var activeConditionID string
	if err := e.idx.SQLDB().QueryRow(
		`SELECT bead_id FROM active_conditions WHERE patient_root = ?`, root.ID,
	).Scan(&activeConditionID); err != nil {
		t.Fatalf("active condition after attestation: %v", err)
	}
	if activeConditionID != amendment.ID {
		t.Errorf("active condition = %s, want amendment %s", activeConditionID, amendment.ID)
	}
}

func TestAutoProjection_OpenRepairsPodAheadOfProjection(t *testing.T) {
	dir := t.TempDir()
	e := openAutoT(t, dir)
	root, err := e.Ingest(unsavedBead("patient_registration", nil, map[string]any{"name": "recovery"}))
	if err != nil {
		t.Fatalf("Ingest root: %v", err)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("Close before crash simulation: %v", err)
	}

	child, err := bead.WithID(unsavedBead("fhir_observation", []string{root.ID}, map[string]any{"note": "pod only"}))
	if err != nil {
		t.Fatalf("WithID child: %v", err)
	}
	store := pod.NewStore(dir)
	podPath, err := store.PatientPodPath(root.ID)
	if err != nil {
		t.Fatalf("PatientPodPath: %v", err)
	}
	w, err := pod.OpenWriter(podPath)
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	if _, err := w.Append(bead.Normalize(child), pod.CodecZstd, pod.NewMeta(root.ID)); err != nil {
		_ = w.Close()
		t.Fatalf("append crash-only frame: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close crash-only writer: %v", err)
	}

	// Disable the cleanup registered by openAutoT from being the only owner of
	// the close lifecycle: Close is idempotency-unsafe, but its later cleanup
	// error is deliberately ignored. Re-open now simulates the next process.
	reopened, err := OpenWithOptions(dir, OpenOptions{
		AutoProject:           true,
		ProjectionCodeVersion: DefaultProjectionCodeVersion(),
	})
	if err != nil {
		t.Fatalf("reopen with automatic recovery: %v", err)
	}
	defer reopened.Close()

	if _, err := reopened.GetBead(child.ID); err != nil {
		t.Fatalf("CatchUp did not index crash-only child: %v", err)
	}
	var status string
	if err := reopened.idx.SQLDB().QueryRow(
		`SELECT status FROM bead_status WHERE bead_id = ?`, child.ID,
	).Scan(&status); err != nil {
		t.Fatalf("recovery did not project child status: %v", err)
	}
	if status != "active" {
		t.Errorf("recovered child status = %s, want active", status)
	}

	var podUpto, projectedUpto int64
	if err := reopened.idx.SQLDB().QueryRow(
		`SELECT p.indexed_upto, s.indexed_upto
		 FROM pods p JOIN patient_projection_state s ON s.patient_root = p.patient_root
		 WHERE p.patient_root = ?`, root.ID,
	).Scan(&podUpto, &projectedUpto); err != nil {
		t.Fatalf("recovered watermarks: %v", err)
	}
	if projectedUpto != podUpto {
		t.Errorf("after recovery projected_upto=%d, pod indexed_upto=%d", projectedUpto, podUpto)
	}
}

func TestAutoProjection_FailureRollsBackIndexAndRecoversFromPod(t *testing.T) {
	dir := t.TempDir()
	e := openAutoT(t, dir)
	root, err := e.Ingest(unsavedBead("patient_registration", nil, map[string]any{"name": "rollback"}))
	if err != nil {
		t.Fatalf("Ingest root: %v", err)
	}

	child, err := bead.WithID(unsavedBead("fhir_observation", []string{root.ID}, map[string]any{"note": "must roll back"}))
	if err != nil {
		t.Fatalf("WithID child: %v", err)
	}
	if _, err := e.idx.SQLDB().Exec(`
		CREATE TRIGGER test_fail_auto_status
		BEFORE INSERT ON bead_status
		WHEN NEW.bead_id = '` + child.ID + `'
		BEGIN
			SELECT RAISE(ABORT, 'injected projection failure');
		END`); err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}

	if _, err := e.Ingest(child); err == nil {
		t.Fatal("Ingest succeeded despite injected projection failure")
	}
	if _, err := e.idx.GetBead(child.ID); !errors.Is(err, index.ErrNotFound) {
		t.Fatalf("child index row survived failed projection: err=%v, want ErrNotFound", err)
	}

	var podUpto, projectedUpto int64
	if err := e.idx.SQLDB().QueryRow(`
		SELECT p.indexed_upto, s.indexed_upto
		FROM pods p JOIN patient_projection_state s ON s.patient_root=p.patient_root
		WHERE p.patient_root=?`, root.ID,
	).Scan(&podUpto, &projectedUpto); err != nil {
		t.Fatalf("watermarks after failed projection: %v", err)
	}
	if podUpto != projectedUpto {
		t.Fatalf("committed watermarks diverged after rollback: pod=%d projected=%d", podUpto, projectedUpto)
	}

	if _, err := e.idx.SQLDB().Exec(`DROP TRIGGER test_fail_auto_status`); err != nil {
		t.Fatalf("drop failure trigger: %v", err)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("Close before recovery: %v", err)
	}
	reopened, err := OpenWithOptions(dir, OpenOptions{
		AutoProject:           true,
		ProjectionCodeVersion: DefaultProjectionCodeVersion(),
	})
	if err != nil {
		t.Fatalf("reopen after failed projection: %v", err)
	}
	defer reopened.Close()

	if _, err := reopened.idx.GetBead(child.ID); err != nil {
		t.Fatalf("CatchUp did not recover rolled-back child: %v", err)
	}
	var status string
	if err := reopened.idx.SQLDB().QueryRow(
		`SELECT status FROM bead_status WHERE bead_id=?`, child.ID,
	).Scan(&status); err != nil {
		t.Fatalf("recovered child status: %v", err)
	}
	if status != "active" {
		t.Errorf("recovered child status=%s, want active", status)
	}
}
