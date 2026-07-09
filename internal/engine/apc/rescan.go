package apc

import "fmt"

// RescanPatient resets root's bead_apc_scan watermark (every bead_apc_scan
// row belonging to a Bead indexed under patient_root=root is deleted) so
// the next Scan() call treats every one of that patient's Beads as an
// unscanned anchor again — the "辞書改訂時の患者単位再スキャン API"
// docs/requirements.md R5 and DESIGN §7 call for ("辞書改訂時のみ患者単位再ス
// キャン API").
//
// # What RescanPatient does and does not undo
//
// RescanPatient only clears the watermark (bead_apc_scan rows); it does not
// delete any previously-created sibling_link Beads, their bead_edges
// 'sibling' rows, or their sibling_pairs rows. This is deliberate, not an
// oversight: sibling_link Beads are themselves content-addressed, immutable,
// tamper-evident Beads (specs/MEDBEADS_SIBLING_SPEC.md §4.5's "CAS不変性の
// 保証") — an append-only design has no delete path for them, and deleting
// their bead_edges/sibling_pairs bookkeeping out from under a still-durable
// Pod frame would desynchronize index.db from the Pod files it is supposed
// to be reconstructable from (specs/DESIGN_v3.md §1's "インデックスは正本か
// ら完全再構築可能" invariant). Concretely, after RescanPatient + Scan:
//
//   - Every pair/antigen combination already recorded in sibling_pairs is
//     still there, so tryLink's unlinkedAntigens check still treats it as
//     already-linked and will not re-ingest a duplicate sibling_link Bead
//     for it (runaway prevention a holds across a rescan, exactly as it
//     does across two ordinary Scan calls).
//   - A dictionary revision that adds a *new* antigen to some existing
//     Bead's content would require a *new* Bead (antigens are hash-target
//     fields — re-tagging a Bead in place is impossible by design, see
//     bead.Canonicalize), which Scan already picks up as an ordinary new
//     anchor without needing RescanPatient at all.
//   - RescanPatient's real use case is a scoring-logic or Config change (a
//     materially different MinScoreThreshold, a new relation-detection rule
//     in a future scorer, etc.): re-running every existing Bead through
//     scanOne re-evaluates candidatesFor/tryLink under the new logic and can
//     discover matches the old logic missed, without needing to touch Beads
//     that never changed.
//
// root must be a patient_root (a patient_registration Bead's own ID), not
// "" (the shared Pod is not a rescan-able "patient" — RescanPatient returns
// an error for root == "").
func (s *Scanner) RescanPatient(root string) error {
	if root == "" {
		return fmt.Errorf("apc: rescan patient: root must not be empty")
	}

	res, err := s.idx.SQLDB().Exec(`
		DELETE FROM bead_apc_scan
		WHERE bead_id IN (SELECT id FROM beads WHERE patient_root = ?)`,
		root)
	if err != nil {
		return fmt.Errorf("apc: rescan patient %s: %w", root, err)
	}
	if _, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("apc: rescan patient %s: rows affected: %w", root, err)
	}
	return nil
}
