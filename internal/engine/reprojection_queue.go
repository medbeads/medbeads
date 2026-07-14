package engine

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/medbeads/medbeads/internal/engine/projector"
	"github.com/medbeads/medbeads/internal/engine/trust"
)

// DefaultReprojectionInactiveAfter is intentionally an operational policy,
// not medical knowledge. Patients without an encounter in this window are
// still updated, but after recently-seen patients. New data always bypasses
// the queue and is projected immediately by Ingest.
const DefaultReprojectionInactiveAfter = 3 * 365 * 24 * time.Hour

type RollingActivation struct {
	RunID          string
	QueuedPatients int64
	AlreadyActive  bool
}

type ReprojectionBatchResult struct {
	TargetRunID string
	Projected   int
	Recent      int
	Inactive    int
	Deceased    int
	Failed      int
	Remaining   int
}

// ActivateLinkKnowledge selects an immutable, closed knowledge set as the
// rolling target generation. It never synchronously reprojects every patient.
// Existing patients are durably represented by their checkpoint mismatch;
// ordinary Ingest moves its own patient directly to this generation.
func (e *Engine) ActivateLinkKnowledge(knowledgeBeadIDs []string, codeVersion, builtAt string) (RollingActivation, error) {
	// A caller cannot bypass a required release merely by choosing this
	// lower-level API instead of ActivateKnowledgeRelease.
	if e.trustPolicy != nil && e.trustPolicy.RequireKnowledgeRelease {
		if _, err := trust.ValidateKnowledgeSet(e, knowledgeBeadIDs, *e.trustPolicy, time.Now().UTC()); err != nil {
			return RollingActivation{}, fmt.Errorf("engine: activate link knowledge: signed release required: %w", err)
		}
	}
	e.ingestMu.Lock()
	defer e.ingestMu.Unlock()

	if e.autoProjection == nil {
		return RollingActivation{}, fmt.Errorf("engine: activate link knowledge: automatic projection is not enabled")
	}
	if codeVersion == "" {
		codeVersion = DefaultProjectionCodeVersion()
	}
	if builtAt == "" {
		builtAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	ids := append([]string(nil), knowledgeBeadIDs...)
	sort.Strings(ids)
	ids = deduplicateStrings(ids)

	active, found, err := e.activeGeneration(projector.ProjectionName)
	if err != nil {
		return RollingActivation{}, err
	}
	if found && active.CodeVersion == codeVersion && equalStrings(active.KnowledgeBeadIDs, ids) {
		queued, err := e.prepareRollingLinkPatients(active.RunID)
		if err != nil {
			return RollingActivation{}, err
		}
		return RollingActivation{RunID: active.RunID, QueuedPatients: queued, AlreadyActive: true}, nil
	}

	res, err := projector.BeginRollingReproject(
		e.idx, engineProjectionReader{e}, ids, codeVersion, builtAt,
	)
	if err != nil {
		return RollingActivation{}, err
	}
	rule, curated, err := projector.LoadLinkRules(e.idx, engineProjectionReader{e}, ids)
	if err != nil {
		return RollingActivation{}, err
	}
	e.autoProjection.linkRunID = res.RunID
	e.autoProjection.rule = rule
	e.autoProjection.curated = curated

	queued, err := e.prepareRollingLinkPatients(res.RunID)
	if err != nil {
		return RollingActivation{}, err
	}
	return RollingActivation{RunID: res.RunID, QueuedPatients: queued}, nil
}

// prepareRollingLinkPatients deliberately does not materialize one queue row per
// patient. patient_projection_state's generation mismatch IS the durable queue;
// writing a million redundant queue rows at activation would turn an O(1)
// generation flip into a large foreground transaction. The physical queue table
// is reserved for failures/retry diagnostics. A second release therefore
// supersedes unprocessed work simply by changing the active target run.
func (e *Engine) prepareRollingLinkPatients(targetRunID string) (int64, error) {
	if _, err := e.idx.SQLDB().Exec(`
		DELETE FROM patient_reprojection_queue
		WHERE target_links_run_id <> ?`, targetRunID); err != nil {
		return 0, fmt.Errorf("engine: discard superseded link retries: %w", err)
	}
	var count int64
	if err := e.idx.SQLDB().QueryRow(`
		SELECT COUNT(*)
		FROM patient_projection_state
		WHERE clinical_links_run_id <> ?`, targetRunID).Scan(&count); err != nil {
		return 0, fmt.Errorf("engine: count stale link patients: %w", err)
	}
	return count, nil
}

type queuedPatient struct {
	PatientRoot string
	Tier        int
}

// ProcessLinkReprojectionQueue processes at most limit patients. Each patient
// takes the ingest mutex separately, so a large maintenance batch never holds
// the write path hostage for the entire batch. Priority is recent encounter,
// inactive/no recent encounter, then deceased hint. The hint only affects
// order; every queued patient remains eligible for eventual processing.
func (e *Engine) ProcessLinkReprojectionQueue(limit int, now time.Time, inactiveAfter time.Duration) (ReprojectionBatchResult, error) {
	e.ingestMu.Lock()
	if e.autoProjection == nil {
		e.ingestMu.Unlock()
		return ReprojectionBatchResult{}, fmt.Errorf("engine: process link reprojection queue: automatic projection is not enabled")
	}
	targetRunID := e.autoProjection.linkRunID
	e.ingestMu.Unlock()
	if inactiveAfter <= 0 {
		inactiveAfter = DefaultReprojectionInactiveAfter
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	result := ReprojectionBatchResult{TargetRunID: targetRunID}
	if limit <= 0 {
		remaining, err := e.countQueuedPatients(targetRunID)
		result.Remaining = remaining
		return result, err
	}

	cutoff := now.Add(-inactiveAfter).UTC().Format(time.RFC3339Nano)
	rows, err := e.idx.SQLDB().Query(`
		SELECT s.patient_root,
		       CASE
		         WHEN COALESCE(a.deceased_hint, 0) = 1 THEN 2
		         WHEN COALESCE(a.last_visit_at, '') >= ? THEN 0
		         ELSE 1
		       END AS priority_tier
		FROM patient_activity a INDEXED BY idx_patient_activity_priority
		JOIN patient_projection_state s ON s.patient_root = a.patient_root
		WHERE s.clinical_links_run_id <> ?
		ORDER BY a.deceased_hint, a.last_visit_at DESC, a.patient_root
		LIMIT ?`, cutoff, targetRunID, limit)
	if err != nil {
		return result, fmt.Errorf("engine: select link reprojection queue: %w", err)
	}
	var patients []queuedPatient
	for rows.Next() {
		var p queuedPatient
		if err := rows.Scan(&p.PatientRoot, &p.Tier); err != nil {
			rows.Close()
			return result, fmt.Errorf("engine: scan link reprojection queue: %w", err)
		}
		patients = append(patients, p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return result, fmt.Errorf("engine: scan link reprojection queue: %w", err)
	}
	if err := rows.Close(); err != nil {
		return result, fmt.Errorf("engine: close link reprojection queue: %w", err)
	}
	if len(patients) < limit {
		// Fail-safe for a deleted/partially rebuilt scheduling projection: lack
		// of patient_activity must lower priority, never make a stale patient
		// permanently unreachable.
		missingRows, err := e.idx.SQLDB().Query(`
			SELECT s.patient_root
			FROM patient_projection_state s
			LEFT JOIN patient_activity a ON a.patient_root=s.patient_root
			WHERE s.clinical_links_run_id<>? AND a.patient_root IS NULL
			ORDER BY s.patient_root
			LIMIT ?`, targetRunID, limit-len(patients))
		if err != nil {
			return result, fmt.Errorf("engine: select stale patients without activity: %w", err)
		}
		for missingRows.Next() {
			var patientRoot string
			if err := missingRows.Scan(&patientRoot); err != nil {
				missingRows.Close()
				return result, fmt.Errorf("engine: scan stale patient without activity: %w", err)
			}
			patients = append(patients, queuedPatient{PatientRoot: patientRoot, Tier: 1})
		}
		if err := missingRows.Err(); err != nil {
			missingRows.Close()
			return result, fmt.Errorf("engine: scan stale patients without activity: %w", err)
		}
		if err := missingRows.Close(); err != nil {
			return result, fmt.Errorf("engine: close stale patients without activity: %w", err)
		}
	}

	for _, p := range patients {
		e.ingestMu.Lock()
		if e.autoProjection == nil || e.autoProjection.linkRunID != targetRunID {
			e.ingestMu.Unlock()
			break // a newer knowledge release superseded this selected batch
		}
		err := e.projectQueuedPatientLinks(p.PatientRoot, now.UTC().Format(time.RFC3339Nano))
		e.ingestMu.Unlock()
		if err != nil {
			result.Failed++
			_, _ = e.idx.SQLDB().Exec(`
				INSERT INTO patient_reprojection_queue
					(patient_root, target_links_run_id, reason, enqueued_at,
					 attempts, last_attempt_at, last_error)
				VALUES (?, ?, 'projection_failure', ?, 1, ?, ?)
				ON CONFLICT(patient_root) DO UPDATE SET
					target_links_run_id=excluded.target_links_run_id,
					reason=excluded.reason,
					attempts=CASE
						WHEN patient_reprojection_queue.target_links_run_id=excluded.target_links_run_id
						THEN patient_reprojection_queue.attempts+1 ELSE 1 END,
					last_attempt_at=excluded.last_attempt_at,
					last_error=excluded.last_error`,
				p.PatientRoot, targetRunID,
				now.UTC().Format(time.RFC3339Nano), now.UTC().Format(time.RFC3339Nano), err.Error())
			continue
		}
		result.Projected++
		switch p.Tier {
		case 0:
			result.Recent++
		case 1:
			result.Inactive++
		case 2:
			result.Deceased++
		}
	}
	remaining, countErr := e.countQueuedPatients(targetRunID)
	result.Remaining = remaining
	if countErr != nil {
		return result, countErr
	}
	return result, nil
}

func (e *Engine) projectQueuedPatientLinks(patientRoot, projectedAt string) error {
	tx, err := e.idx.SQLDB().Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	var currentRun string
	if err := tx.QueryRow(`
		SELECT clinical_links_run_id FROM patient_projection_state
		WHERE patient_root=?`, patientRoot).Scan(&currentRun); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("projection checkpoint is missing for patient %s", patientRoot)
		}
		return fmt.Errorf("read patient projection generation: %w", err)
	}
	if currentRun == e.autoProjection.linkRunID {
		return nil // a new ingest already moved this patient to the target
	}
	if _, err := projector.ProjectPatientLinksInTx(
		tx, e.autoProjection.rule, e.autoProjection.curated,
		patientRoot, e.autoProjection.linkRunID,
	); err != nil {
		return fmt.Errorf("clinical_links: %w", err)
	}
	res, err := tx.Exec(`
		UPDATE patient_projection_state
		SET clinical_links_run_id=?, projected_at=?
		WHERE patient_root=?`, e.autoProjection.linkRunID, projectedAt, patientRoot)
	if err != nil {
		return fmt.Errorf("update projection checkpoint: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("projection checkpoint rows affected: %w", err)
	} else if n == 0 {
		return fmt.Errorf("projection checkpoint is missing for patient %s", patientRoot)
	}
	if _, err := tx.Exec(`
		DELETE FROM patient_reprojection_queue
		WHERE patient_root=? AND target_links_run_id=?`, patientRoot, e.autoProjection.linkRunID); err != nil {
		return fmt.Errorf("complete queue row: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func (e *Engine) countQueuedPatients(targetRunID string) (int, error) {
	var count int
	if err := e.idx.SQLDB().QueryRow(`
		SELECT COUNT(*) FROM patient_projection_state
		WHERE clinical_links_run_id<>?`, targetRunID).Scan(&count); err != nil {
		return 0, fmt.Errorf("engine: count link reprojection queue: %w", err)
	}
	return count, nil
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	aa := append([]string(nil), a...)
	bb := append([]string(nil), b...)
	sort.Strings(aa)
	sort.Strings(bb)
	for i := range aa {
		if aa[i] != bb[i] {
			return false
		}
	}
	return true
}
