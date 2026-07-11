package projector

import (
	"database/sql"
	"fmt"

	"github.com/medbeads/medbeads/internal/engine/bead"
	"github.com/medbeads/medbeads/internal/engine/index"
)

// statusBeadReader is the narrow subset of *engine.Engine StatusReproject
// needs: enumerating one patient's full Beads, fully decoded (Type/Content/
// Parents/Amends/Retracts) — the "Pod デコード方式" specs/
// U4_state_derivation.md's "合意点" #2 calls for, distinct from Reproject's
// beadReader (which only ever needs a single Bead's Content, since it reads
// bead_tags/beads directly rather than replaying Pod content). §2's
// resolution algorithm cannot run off of index.DB's own BeadRef rows alone —
// it needs Amends/Retracts/Parents and attestation Content, none of which
// index.db projects into its own columns — so this projector, unlike
// Reproject, DOES read Pod content per patient (via ListPatientBeads), per
// this file's own doc comment on StatusReproject.
type statusBeadReader interface {
	ListPatientBeads(patientRoot string) ([]bead.Bead, error)
}

// StatusProjectionName is the fixed projection_manifest.projection_name the
// record_state projector (this file) writes and flips — a lineage separate
// from Reproject's ProjectionName ("clinical_links_v31"): bead_status/
// active_conditions/active_medications consult no knowledge Beads
// (knowledge_bead_ids is always empty for this projector), so mixing the two
// under one projection_name would conflate two unrelated input sets under
// migrations/0006's "at most one active row per projection_name" constraint
// (specs/U4_state_derivation.md's "合意点" #5: "別 projection_name =
// 'record_state_v31'…clinical_links_v31 とは別 lineage").
const StatusProjectionName = "record_state_v31"

// fhirConditionType / fhirMedicationRequestType are the two Bead types
// active_conditions/active_medications are populated from (specs/
// U4_state_derivation.md's active_views section: "条件/処方は type IN
// ('fhir_condition','fhir_medicationrequest')").
const (
	fhirConditionType         = "fhir_condition"
	fhirMedicationRequestType = "fhir_medicationrequest"
)

// StatusResult summarizes one StatusReproject call, mirroring Reproject's own
// Result shape for the analogous purpose (a caller wanting a count rather
// than re-querying bead_status/active_conditions/active_medications itself).
type StatusResult struct {
	RunID             string
	PatientsProjected int
	BeadStatusWritten int
	ActiveConditions  int
	ActiveMedications int
}

// StatusReproject is U4b's full-reprojection entry point (specs/
// U4_state_derivation.md's "projector 構造" section): for every patient, it
// decodes that patient's Beads (via reader's ListPatientBeads-shaped
// callback — see statusBeadReader below), joins each Bead's own recorded_at
// from beads (idx.SQLDB() directly — the §2 axis fix, "穴2"), runs
// resolvePatientState (the pure §2 algorithm, resolve.go) to get a per-Bead
// beadState, and derives active_conditions/active_medications rows from
// whichever Beads resolve to a currently-active fact of the right type. All
// three tables are DELETE+INSERT (patient_root-scoped) inside a single
// per-patient transaction, then projection_manifest's single active row for
// StatusProjectionName is flipped in one final transaction — mirroring
// Reproject's own patient_root-batched-not-single-global-transaction design
// (see Reproject's doc comment for why: SQLite's single writer lock,
// index.go's SetMaxOpenConns(1)).
//
// # This projector consults no knowledge Beads
//
// Unlike Reproject (which resolves a link_rule Bead), StatusReproject's
// knowledge_bead_ids is always the empty slice: §2's resolution rules are
// fixed Go logic, not configurable-by-knowledge-Bead the way the
// cooccurrence link_rule is (specs/U4_state_derivation.md's "bead_status は
// knowledge Bead を参照しない → knowledge_bead_ids=[]"). codeVersion still
// varies (an algorithm/build version), so re-running with a changed
// codeVersion still yields a distinct run_id.
//
// # Read-only with respect to bead_tags/clinical_links
//
// StatusReproject never reads or writes bead_tags/clinical_links (Reproject's
// own tables) — the two projectors are lineage-independent write sets, per
// this file's StatusProjectionName doc comment.
func StatusReproject(idx *index.DB, reader statusBeadReader, codeVersion string, builtAt string) (StatusResult, error) {
	// knowledgeBeadIDs is always empty for this projector (see doc comment
	// above); computeConfigHash/computeRunID/insertBuildingManifest all
	// accept it as a parameter purely for symmetry with Reproject's
	// signature — passing nil here is honest, not a placeholder for a future
	// wiring.
	var knowledgeBeadIDs []string

	configHash, err := computeConfigHash(knowledgeBeadIDs, codeVersion)
	if err != nil {
		return StatusResult{}, fmt.Errorf("projector: status reproject: %w", err)
	}
	watermarks, err := queryInputWatermarks(idx.SQLDB())
	if err != nil {
		return StatusResult{}, fmt.Errorf("projector: status reproject: %w", err)
	}
	runID, err := computeRunID(StatusProjectionName, knowledgeBeadIDs, configHash, codeVersion, builtAt)
	if err != nil {
		return StatusResult{}, fmt.Errorf("projector: status reproject: %w", err)
	}

	if err := insertBuildingManifest(idx.SQLDB(), StatusProjectionName, runID, codeVersion, knowledgeBeadIDs, configHash, watermarks, builtAt); err != nil {
		return StatusResult{}, fmt.Errorf("projector: status reproject: %w", err)
	}

	patients, err := idx.ListPatients()
	if err != nil {
		return StatusResult{}, fmt.Errorf("projector: status reproject: list patients: %w", err)
	}

	var res StatusResult
	res.RunID = runID
	for _, p := range patients {
		beads, err := reader.ListPatientBeads(p.ID)
		if err != nil {
			return res, fmt.Errorf("projector: status reproject: patient %s: list beads: %w", p.ID, err)
		}
		recordedAt, err := queryPatientRecordedAt(idx.SQLDB(), p.ID)
		if err != nil {
			return res, fmt.Errorf("projector: status reproject: patient %s: %w", p.ID, err)
		}

		resolveInput := make([]resolveBead, 0, len(beads))
		for _, b := range beads {
			ra, valid := recordedAt[b.ID]
			resolveInput = append(resolveInput, resolveBead{Bead: b, RecordedAt: ra, RecordedAtValid: valid})
		}
		states := resolvePatientState(resolveInput)

		written, conditions, medications, err := writePatientState(idx.SQLDB(), p.ID, runID, beads, states)
		if err != nil {
			return res, fmt.Errorf("projector: status reproject: patient %s: %w", p.ID, err)
		}
		res.PatientsProjected++
		res.BeadStatusWritten += written
		res.ActiveConditions += conditions
		res.ActiveMedications += medications
	}

	if err := flipManifestActive(idx.SQLDB(), StatusProjectionName, runID); err != nil {
		return res, fmt.Errorf("projector: status reproject: %w", err)
	}

	return res, nil
}

// queryPatientRecordedAt returns beadID -> (recorded_at, true) for every Bead
// indexed under patientRoot whose recorded_at is non-NULL, mirroring
// project.go's queryPatientTags direct-SQL convention: this package's own
// read API (index.DB) does not expose a bare beads.recorded_at lookup, so
// StatusReproject reads it directly, exactly the "beads を JOIN して
// recorded_at を取り、デコード content と ID で marry する" the spec calls
// for. A beadID absent from the returned map means its recorded_at is NULL
// (see beadOrderLess's RecordedAtValid handling for why NULL is NOT the same
// as "" — an empty string recorded_at is not a value this schema ever
// produces, but the map-presence check keeps that distinction explicit
// regardless).
func queryPatientRecordedAt(sqlDB *sql.DB, patientRoot string) (map[string]string, error) {
	rows, err := sqlDB.Query(
		`SELECT id, recorded_at FROM beads WHERE patient_root = ? AND recorded_at IS NOT NULL`,
		patientRoot)
	if err != nil {
		return nil, fmt.Errorf("query patient recorded_at %s: %w", patientRoot, err)
	}
	defer rows.Close()

	out := make(map[string]string)
	for rows.Next() {
		var id, recordedAt string
		if err := rows.Scan(&id, &recordedAt); err != nil {
			return nil, fmt.Errorf("query patient recorded_at %s: scan: %w", patientRoot, err)
		}
		out[id] = recordedAt
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query patient recorded_at %s: %w", patientRoot, err)
	}
	return out, nil
}

// writePatientState replaces patientRoot's bead_status/active_conditions/
// active_medications rows (any row not already stamped with runID) with the
// newly-resolved set, in a single transaction — the same per-patient atomic-
// replace pattern reprojectPatient (reproject.go) uses for clinical_links.
// Returns (bead_status rows written, active_conditions rows written,
// active_medications rows written).
func writePatientState(sqlDB *sql.DB, patientRoot, runID string, beads []bead.Bead, states map[string]beadState) (int, int, int, error) {
	tx, err := sqlDB.Begin()
	if err != nil {
		return 0, 0, 0, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after commit is a no-op

	if _, err := tx.Exec(
		`DELETE FROM bead_status WHERE patient_root = ? AND (projection_run_id IS NULL OR projection_run_id <> ?)`,
		patientRoot, runID,
	); err != nil {
		return 0, 0, 0, fmt.Errorf("delete stale bead_status for %s: %w", patientRoot, err)
	}
	if _, err := tx.Exec(
		`DELETE FROM active_conditions WHERE patient_root = ? AND (projection_run_id IS NULL OR projection_run_id <> ?)`,
		patientRoot, runID,
	); err != nil {
		return 0, 0, 0, fmt.Errorf("delete stale active_conditions for %s: %w", patientRoot, err)
	}
	if _, err := tx.Exec(
		`DELETE FROM active_medications WHERE patient_root = ? AND (projection_run_id IS NULL OR projection_run_id <> ?)`,
		patientRoot, runID,
	); err != nil {
		return 0, 0, 0, fmt.Errorf("delete stale active_medications for %s: %w", patientRoot, err)
	}

	var statusWritten, conditionsWritten, medicationsWritten int
	for _, b := range beads {
		st, ok := states[b.ID]
		if !ok {
			// Every Bead passed in was also passed to resolvePatientState, so
			// every ID must have a resolved state; unreachable in practice,
			// but fail loudly rather than silently skipping a bead_status row.
			return 0, 0, 0, fmt.Errorf("bead %s has no resolved state (internal invariant violated)", b.ID)
		}

		if err := insertBeadStatusRow(tx, b.ID, patientRoot, runID, st); err != nil {
			return 0, 0, 0, fmt.Errorf("insert bead_status %s: %w", b.ID, err)
		}
		statusWritten++

		if st.Status != "active" {
			continue
		}
		switch b.Type {
		case fhirConditionType:
			wrote, err := insertActiveConditionRow(tx, b, patientRoot, runID, st.CurrentBeadID)
			if err != nil {
				return 0, 0, 0, fmt.Errorf("insert active_conditions %s: %w", b.ID, err)
			}
			if wrote {
				conditionsWritten++
			}
		case fhirMedicationRequestType:
			wrote, err := insertActiveMedicationRow(tx, b, patientRoot, runID, st.CurrentBeadID)
			if err != nil {
				return 0, 0, 0, fmt.Errorf("insert active_medications %s: %w", b.ID, err)
			}
			if wrote {
				medicationsWritten++
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, 0, fmt.Errorf("commit: %w", err)
	}
	return statusWritten, conditionsWritten, medicationsWritten, nil
}

// insertBeadStatusRow writes one bead_status row for beadID, translating
// beadState's ""-means-NULL string fields into actual SQL NULLs via
// sql.NullString (bead_status.current_bead_id/superseded_by/
// attestation_bead_id/retraction_bead_id/reason are all nullable columns —
// migrations/0006_projection_v31.sql).
func insertBeadStatusRow(tx *sql.Tx, beadID, patientRoot, runID string, st beadState) error {
	_, err := tx.Exec(`
		INSERT INTO bead_status
			(bead_id, status, current_bead_id, superseded_by, attestation_bead_id,
			 retraction_bead_id, reason, projection_run_id, patient_root)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		beadID, st.Status,
		nullIfEmpty(st.CurrentBeadID), nullIfEmpty(st.SupersededBy),
		nullIfEmpty(st.AttestationBeadID), nullIfEmpty(st.RetractionBeadID),
		nil, // reason: not populated by U4b (no content field maps to it yet); left NULL
		runID, patientRoot,
	)
	return err
}

// insertActiveConditionRow writes conditionBead's active_conditions row if
// (and only if) its content.clinicalStatus == "active" — the FHIR axis half
// of the "両軸を AND" rule (specs/U4_state_derivation.md's active_views
// section: "active 判定 = FHIR content.clinicalStatus=='active' かつ
// bead_status.status='active'"; the record_status half was already checked
// by writePatientState's caller before this function is ever invoked).
//
// content.clinicalStatus/verificationStatus are read via fhirCodeString,
// which handles BOTH shapes this codebase's real Synthea corpus mixes: a
// plain string, and a FHIR CodeableConcept
// ({"coding":[{"code":"active"}]}) — the shape Synthea's own Condition
// resources actually use for both fields, which a bare ".(string)" type
// assertion silently misses (it never panics; it just always yields "",
// which never equals "active"). Returns whether a row was actually written.
func insertActiveConditionRow(tx *sql.Tx, conditionBead bead.Bead, patientRoot, runID, currentBeadID string) (bool, error) {
	clinicalStatus := fhirCodeString(conditionBead.Content, "clinicalStatus")
	if clinicalStatus != "active" {
		return false, nil
	}
	verificationStatus := fhirCodeString(conditionBead.Content, "verificationStatus")

	if _, err := tx.Exec(`
		INSERT INTO active_conditions
			(patient_root, bead_id, current_bead_id, clinical_status, verification_status, projection_run_id)
		VALUES (?, ?, ?, ?, ?, ?)`,
		patientRoot, conditionBead.ID, currentBeadID, clinicalStatus, nullIfEmpty(verificationStatus), runID,
	); err != nil {
		return false, err
	}
	return true, nil
}

// insertActiveMedicationRow writes medicationBead's active_medications row
// if (and only if) its content.status == "active" — Synthea's
// fhir_medicationrequest content carries the FHIR MedicationRequest
// resource's own top-level "status" field (not "medicationStatus" — verified
// against this store's demo_data corpus), analogous to
// insertActiveConditionRow's clinicalStatus check. status/intent are plain
// strings in this corpus (unlike fhir_condition's clinicalStatus/
// verificationStatus), but this also routes through fhirCodeString: it
// returns a string field unchanged, so behavior for the string case is
// identical to the previous direct ".(string)" assertion. Returns whether a
// row was actually written.
func insertActiveMedicationRow(tx *sql.Tx, medicationBead bead.Bead, patientRoot, runID, currentBeadID string) (bool, error) {
	medicationStatus := fhirCodeString(medicationBead.Content, "status")
	if medicationStatus != "active" {
		return false, nil
	}
	intent := fhirCodeString(medicationBead.Content, "intent")

	if _, err := tx.Exec(`
		INSERT INTO active_medications
			(patient_root, bead_id, current_bead_id, medication_status, intent, projection_run_id)
		VALUES (?, ?, ?, ?, ?, ?)`,
		patientRoot, medicationBead.ID, currentBeadID, medicationStatus, nullIfEmpty(intent), runID,
	); err != nil {
		return false, err
	}
	return true, nil
}

// fhirCodeString reads a FHIR status-code-shaped field out of content[key],
// tolerating the two shapes this codebase's real data mixes for such fields:
//
//   - a plain string (e.g. fhir_medicationrequest's "status"/"intent" in
//     this store's Synthea corpus) — returned as-is.
//   - a FHIR CodeableConcept (e.g. fhir_condition's "clinicalStatus"/
//     "verificationStatus" in the same corpus):
//     {"coding": [{"system": "...", "code": "active"}], ...}
//     — decoded from JSON via a Pod-frame round-trip, so "coding" comes back
//     as []any (not []map[string]any) and each element as map[string]any;
//     this reads the first coding entry's "code" and returns it.
//
// Any other shape (missing key, wrong type, empty coding, missing/non-string
// code) returns "" — the same "absent" signal a failed type assertion would
// have produced, so callers comparing against a specific status string need
// no other changes.
func fhirCodeString(content map[string]any, key string) string {
	switch v := content[key].(type) {
	case string:
		return v
	case map[string]any:
		coding, ok := v["coding"].([]any)
		if !ok || len(coding) == 0 {
			return ""
		}
		first, ok := coding[0].(map[string]any)
		if !ok {
			return ""
		}
		code, _ := first["code"].(string)
		return code
	default:
		return ""
	}
}

// nullIfEmpty converts ""'s beadState/content convention (empty string means
// "this column is NULL") into an actual sql.NullString, so INSERT statements
// never write a literal empty string into a nullable bead_status/
// active_conditions/active_medications column that should instead be NULL.
func nullIfEmpty(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}
