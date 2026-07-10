package apc

import "fmt"

// ScanPatient is Scan's single-patient-scoped sibling: it examines only the
// not-yet-watermarked ("新Bead", see Scan's doc comment) Beads belonging to
// root, matching each against already-scanned Beads within that same
// patient, exactly as Scan does for every patient in one call.
// docs/requirements.md R5 and specs/DESIGN_v3.md §7 describe Scan's
// "新Bead vs 患者内スキャン済みのみ照合" incremental rule at the
// per-patient level already (candidatesFor/candidateRows always filter by
// anchor.PatientRoot) — ScanPatient simply narrows which patients' new Beads
// are picked up as anchors in a single call, for a caller (cmd/medbeadsd's
// `apc -patient <root>`) that wants to bound one invocation's batch scope to
// one patient rather than the whole store, without waiting on every other
// patient's new Beads to also be scanned in the same call.
//
// ScanPatient is not RescanPatient: it does not clear any existing
// bead_apc_scan watermark, so a Bead of root's already scanned by a prior
// Scan/ScanPatient call is not re-examined as an anchor here either — the
// same idempotent, incremental semantics Scan documents. Use RescanPatient
// first (a separate, deliberate call) if root's Beads must be re-matched
// under a new Config/scoring rule.
//
// root must be a non-empty patient_root (see RescanPatient's identical
// requirement); ScanPatient returns an error for root == "".
func (s *Scanner) ScanPatient(root string) (Result, error) {
	if root == "" {
		return Result{}, fmt.Errorf("apc: scan patient: root must not be empty")
	}

	anchors, err := s.unscannedBeadsForPatient(root)
	if err != nil {
		return Result{}, fmt.Errorf("apc: scan patient %s: %w", root, err)
	}

	// Pre-mark every anchor in this batch scanned up front, before matching
	// any of them against candidates — identical reasoning to Scan's own
	// pre-marking pass (see Scan's doc comment): this is what makes
	// intra-batch matching symmetric within root's own new-Bead set.
	for _, anchor := range anchors {
		anchorGeneration, err := s.beadGeneration(anchor)
		if err != nil {
			return Result{}, fmt.Errorf("apc: scan patient %s: %w", root, err)
		}
		if err := s.markScanned(anchor.ID, 0, anchorGeneration); err != nil {
			return Result{}, fmt.Errorf("apc: scan patient %s: %w", root, err)
		}
	}

	var res Result
	linksThisPatientScan := make(map[string]int)

	for _, anchor := range anchors {
		res.BeadsScanned++

		created, err := s.scanOne(anchor, linksThisPatientScan)
		if err != nil {
			return res, fmt.Errorf("apc: scan patient %s: scan %s: %w", root, anchor.ID, err)
		}
		res.SiblingLinksCreated += created
	}

	return res, nil
}

// unscannedBeadsForPatient is unscannedBeads narrowed to root's own Beads —
// the same "no bead_apc_scan row yet" anchor definition, ordered by id for
// deterministic output, but scoped by b.patient_root = ? instead of every
// patient in the store.
func (s *Scanner) unscannedBeadsForPatient(root string) ([]scannedBeadRef, error) {
	rows, err := s.idx.SQLDB().Query(`
		SELECT b.id, COALESCE(b.patient_root, ''), b.type, b.timestamp
		FROM beads b
		LEFT JOIN bead_apc_scan s ON s.bead_id = b.id
		WHERE s.bead_id IS NULL AND b.patient_root = ?
		ORDER BY b.id`, root)
	if err != nil {
		return nil, fmt.Errorf("unscanned beads for patient %s: %w", root, err)
	}
	defer rows.Close()

	var out []scannedBeadRef
	for rows.Next() {
		var ref scannedBeadRef
		if err := rows.Scan(&ref.ID, &ref.PatientRoot, &ref.Type, &ref.Timestamp); err != nil {
			return nil, fmt.Errorf("unscanned beads for patient %s: scan: %w", root, err)
		}
		out = append(out, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("unscanned beads for patient %s: %w", root, err)
	}
	return out, nil
}
