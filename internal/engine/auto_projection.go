package engine

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/medbeads/medbeads/internal/engine/bead"
	"github.com/medbeads/medbeads/internal/engine/projector"
	"github.com/medbeads/medbeads/internal/engine/trust"
)

const builtInRuleTimestamp = "2026-01-01T00:00:00Z"

type autoProjection struct {
	linkRunID   string
	statusRunID string
	rule        projector.LinkRule
	curated     []projector.CuratedPairRule
}

type activeProjectionGeneration struct {
	RunID            string
	CodeVersion      string
	KnowledgeBeadIDs []string
}

type engineProjectionReader struct{ e *Engine }

func (r engineProjectionReader) GetBead(id string) (projector.BeadContent, error) {
	b, err := r.e.GetBead(id)
	if err != nil {
		return projector.BeadContent{}, err
	}
	return projector.BeadContent{Content: b.Content}, nil
}

// initializeAutoProjection establishes the two global interpretation targets,
// then repairs only patients whose Pod/index watermark is ahead of their last
// atomic projection. A link knowledge/code change starts a rolling generation;
// ordinary Bead growth projects only the affected patient immediately.
func (e *Engine) initializeAutoProjection(codeVersion, statusCodeVersion string, initialKnowledgeIDs []string) error {
	linkGen, linkFound, err := e.activeGeneration(projector.ProjectionName)
	if err != nil {
		return err
	}

	var desiredKnowledgeIDs []string
	if e.trustPolicy != nil && e.trustPolicy.RequireKnowledgeRelease {
		desiredKnowledgeIDs = append([]string(nil), initialKnowledgeIDs...)
		if len(desiredKnowledgeIDs) == 0 {
			if !linkFound {
				return fmt.Errorf("trusted knowledge release is required, but no active clinical_links generation exists")
			}
			desiredKnowledgeIDs = append(desiredKnowledgeIDs, linkGen.KnowledgeBeadIDs...)
		}
		sort.Strings(desiredKnowledgeIDs)
		desiredKnowledgeIDs = deduplicateStrings(desiredKnowledgeIDs)
		if _, err := trust.ValidateKnowledgeSet(e, desiredKnowledgeIDs, *e.trustPolicy, time.Now().UTC()); err != nil {
			return fmt.Errorf("validate signed link knowledge: %w", err)
		}
	} else {
		// Compatibility mode: publish this build's immutable built-in rule. A
		// configured trust policy with RequireKnowledgeRelease=true never takes
		// this unsigned bootstrap path.
		savedRule, err := e.Ingest(projector.BuildCooccurrenceRuleBead(builtInRuleTimestamp))
		if err != nil {
			return fmt.Errorf("seed built-in link rule: %w", err)
		}
		desiredKnowledgeIDs, err = e.knowledgeForCurrentBuild(linkGen, linkFound, savedRule.ID)
		if err != nil {
			return err
		}
	}

	linkRebuilt := !linkFound || linkGen.CodeVersion != codeVersion ||
		!equalStrings(linkGen.KnowledgeBeadIDs, desiredKnowledgeIDs)
	if linkRebuilt {
		res, err := projector.BeginRollingReproject(
			e.idx,
			engineProjectionReader{e},
			desiredKnowledgeIDs,
			codeVersion,
			time.Now().UTC().Format(time.RFC3339Nano),
		)
		if err != nil {
			return fmt.Errorf("begin rolling clinical_links generation: %w", err)
		}
		linkGen = activeProjectionGeneration{
			RunID:            res.RunID,
			CodeVersion:      codeVersion,
			KnowledgeBeadIDs: desiredKnowledgeIDs,
		}
	}

	statusGen, statusFound, err := e.activeGeneration(projector.StatusProjectionName)
	if err != nil {
		return err
	}
	statusRebuilt := !statusFound || statusGen.CodeVersion != statusCodeVersion
	if statusRebuilt {
		res, err := projector.StatusReproject(
			e.idx,
			e,
			statusCodeVersion,
			time.Now().UTC().Format(time.RFC3339Nano),
		)
		if err != nil {
			return fmt.Errorf("bootstrap record_state generation: %w", err)
		}
		statusGen = activeProjectionGeneration{RunID: res.RunID, CodeVersion: statusCodeVersion}
		// StatusReproject already processed every indexed patient. Preserve each
		// patient's independently rolling link generation while advancing only
		// the status checkpoint, avoiding a second all-patient pass below.
		if _, err := e.idx.SQLDB().Exec(`
			UPDATE patient_projection_state
			SET record_state_run_id=?, projected_at=?`,
			statusGen.RunID, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("advance record_state patient checkpoints: %w", err)
		}
	}

	rule, curated, err := projector.LoadLinkRules(
		e.idx,
		engineProjectionReader{e},
		linkGen.KnowledgeBeadIDs,
	)
	if err != nil {
		return fmt.Errorf("load active link knowledge: %w", err)
	}
	e.autoProjection = &autoProjection{
		linkRunID:   linkGen.RunID,
		statusRunID: statusGen.RunID,
		rule:        rule,
		curated:     curated,
	}

	if linkRebuilt {
		if _, err := e.prepareRollingLinkPatients(linkGen.RunID); err != nil {
			return err
		}
	}
	if err := e.reconcilePatientProjections(); err != nil {
		return err
	}
	return nil
}

func (e *Engine) activeGeneration(projectionName string) (activeProjectionGeneration, bool, error) {
	var out activeProjectionGeneration
	var knowledgeJSON string
	err := e.idx.SQLDB().QueryRow(`
		SELECT run_id, code_version, knowledge_bead_ids
		FROM projection_manifest
		WHERE projection_name = ? AND status = 'active'`,
		projectionName,
	).Scan(&out.RunID, &out.CodeVersion, &knowledgeJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return activeProjectionGeneration{}, false, nil
	}
	if err != nil {
		return activeProjectionGeneration{}, false, fmt.Errorf("query active %s generation: %w", projectionName, err)
	}
	if err := json.Unmarshal([]byte(knowledgeJSON), &out.KnowledgeBeadIDs); err != nil {
		return activeProjectionGeneration{}, false, fmt.Errorf("decode active %s knowledge_bead_ids: %w", projectionName, err)
	}
	return out, true, nil
}

// knowledgeForCurrentBuild replaces only an older revision of the built-in
// cooccurrence rule while preserving every explicitly activated curated or
// dictionary knowledge Bead. Unactivated shared Beads are not picked up
// implicitly: projection inputs remain a closed manifest-declared set.
func (e *Engine) knowledgeForCurrentBuild(active activeProjectionGeneration, found bool, currentRuleID string) ([]string, error) {
	ids := []string{currentRuleID}
	if found {
		for _, id := range active.KnowledgeBeadIDs {
			if id == currentRuleID {
				continue
			}
			b, err := e.GetBead(id)
			if err != nil {
				return nil, fmt.Errorf("read active knowledge Bead %s: %w", id, err)
			}
			family, _ := b.Content["rule_family"].(string)
			ruleID, _ := b.Content["rule_id"].(string)
			if family == "cooccurrence" && ruleID == projector.CooccurrenceRuleID {
				continue // old built-in revision; superseded by currentRuleID
			}
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return deduplicateStrings(ids), nil
}

func (e *Engine) seedPatientProjectionState(linkRunID, statusRunID string) error {
	_, err := e.idx.SQLDB().Exec(`
		INSERT INTO patient_projection_state
			(patient_root, pod_path, indexed_upto, clinical_links_run_id,
			 record_state_run_id, projected_at)
		SELECT patient_root, path, indexed_upto, ?, ?, ?
		FROM pods
		WHERE patient_root IS NOT NULL AND patient_root <> ''
		ON CONFLICT(patient_root) DO UPDATE SET
			pod_path = excluded.pod_path,
			indexed_upto = excluded.indexed_upto,
			clinical_links_run_id = excluded.clinical_links_run_id,
			record_state_run_id = excluded.record_state_run_id,
			projected_at = excluded.projected_at`,
		linkRunID, statusRunID, time.Now().UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("seed patient projection state: %w", err)
	}
	return nil
}

type patientProjectionCheckpoint struct {
	PatientRoot string
	PodPath     string
	IndexedUpto int64
}

func (e *Engine) reconcilePatientProjections() error {
	rows, err := e.idx.SQLDB().Query(`
		SELECT p.patient_root, p.path, p.indexed_upto
		FROM pods p
		LEFT JOIN patient_projection_state s ON s.patient_root = p.patient_root
		WHERE p.patient_root IS NOT NULL AND p.patient_root <> ''
		  AND (s.patient_root IS NULL
		       OR s.indexed_upto IS NULL OR s.indexed_upto <> p.indexed_upto
		       OR s.record_state_run_id IS NULL OR s.record_state_run_id <> ?)
		ORDER BY p.patient_root`, e.autoProjection.statusRunID)
	if err != nil {
		return fmt.Errorf("list patient projection checkpoints: %w", err)
	}
	var checkpoints []patientProjectionCheckpoint
	for rows.Next() {
		var cp patientProjectionCheckpoint
		if err := rows.Scan(&cp.PatientRoot, &cp.PodPath, &cp.IndexedUpto); err != nil {
			rows.Close()
			return fmt.Errorf("scan patient projection checkpoint: %w", err)
		}
		checkpoints = append(checkpoints, cp)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("list patient projection checkpoints: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close patient projection checkpoints: %w", err)
	}

	for _, cp := range checkpoints {
		// The SQL query deliberately selects only crash/data-watermark or
		// record-state mismatches. A links-only generation mismatch is the
		// virtual rolling queue and is not materialized or synchronously repaired.
		if err := e.reconcilePatientProjection(cp.PatientRoot, cp.PodPath, cp.IndexedUpto); err != nil {
			return fmt.Errorf("reconcile patient %s: %w", cp.PatientRoot, err)
		}
	}
	return nil
}

func (e *Engine) reconcilePatientProjection(patientRoot, podPath string, indexedUpto int64) error {
	tx, err := e.idx.SQLDB().Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	beads, err := e.listPatientBeadsTx(tx, patientRoot)
	if err != nil {
		return err
	}
	if _, err := projector.ProjectPatientLinksInTx(
		tx, e.autoProjection.rule, e.autoProjection.curated,
		patientRoot, e.autoProjection.linkRunID,
	); err != nil {
		return fmt.Errorf("clinical_links: %w", err)
	}
	if _, err := projector.ProjectPatientStateInTx(
		tx, patientRoot, e.autoProjection.statusRunID, beads,
	); err != nil {
		return fmt.Errorf("record_state: %w", err)
	}
	if err := upsertPatientProjectionState(
		tx, patientRoot, podPath, indexedUpto,
		e.autoProjection.linkRunID, e.autoProjection.statusRunID,
		time.Now().UTC().Format(time.RFC3339Nano),
	); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM patient_reprojection_queue WHERE patient_root=?`, patientRoot); err != nil {
		return fmt.Errorf("complete queued patient: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// projectAppendedPatientBead runs after IndexBead and before the same
// transaction commits. It never touches another patient.
func (e *Engine) projectAppendedPatientBead(
	tx *sql.Tx,
	b bead.Bead,
	patientRoot, podPath string,
	indexedUpto int64,
	projectedAt string,
) error {
	if e.autoProjection == nil || patientRoot == "" {
		return nil
	}

	if _, err := projector.ProjectPatientLinksInTx(
		tx, e.autoProjection.rule, e.autoProjection.curated,
		patientRoot, e.autoProjection.linkRunID,
	); err != nil {
		return fmt.Errorf("clinical_links: %w", err)
	}

	if projector.RequiresFullPatientState(b) {
		beads, err := e.listPatientBeadsTx(tx, patientRoot)
		if err != nil {
			return fmt.Errorf("record_state list beads: %w", err)
		}
		if _, err := projector.ProjectPatientStateInTx(
			tx, patientRoot, e.autoProjection.statusRunID, beads,
		); err != nil {
			return fmt.Errorf("record_state: %w", err)
		}
	} else {
		if _, err := projector.ProjectNewPatientBeadStateInTx(
			tx, patientRoot, e.autoProjection.statusRunID, b,
		); err != nil {
			return fmt.Errorf("record_state: %w", err)
		}
	}

	if err := upsertPatientProjectionState(
		tx, patientRoot, podPath, indexedUpto,
		e.autoProjection.linkRunID, e.autoProjection.statusRunID, projectedAt,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM patient_reprojection_queue WHERE patient_root=?`, patientRoot); err != nil {
		return fmt.Errorf("complete queued patient: %w", err)
	}
	return nil
}

func upsertPatientProjectionState(
	tx *sql.Tx,
	patientRoot, podPath string,
	indexedUpto int64,
	linkRunID, statusRunID, projectedAt string,
) error {
	if _, err := tx.Exec(`
		INSERT INTO patient_projection_state
			(patient_root, pod_path, indexed_upto, clinical_links_run_id,
			 record_state_run_id, projected_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(patient_root) DO UPDATE SET
			pod_path = excluded.pod_path,
			indexed_upto = excluded.indexed_upto,
			clinical_links_run_id = excluded.clinical_links_run_id,
			record_state_run_id = excluded.record_state_run_id,
			projected_at = excluded.projected_at`,
		patientRoot, podPath, indexedUpto, linkRunID, statusRunID, projectedAt,
	); err != nil {
		return fmt.Errorf("update patient projection state: %w", err)
	}
	return nil
}

func containsString(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func deduplicateStrings(ss []string) []string {
	if len(ss) == 0 {
		return nil
	}
	out := ss[:1]
	for _, s := range ss[1:] {
		if s != out[len(out)-1] {
			out = append(out, s)
		}
	}
	return out
}
