package engine

import (
	"errors"
	"fmt"

	"github.com/medbeads/medbeads/internal/engine/bead"
	"github.com/medbeads/medbeads/internal/engine/index"
	"github.com/medbeads/medbeads/internal/engine/pod"
)

// Ingest is the write protocol's single entry point (specs/DESIGN_v3.md §3,
// specs/DESIGN_v3.1_draft.md §2):
//
//  1. Verify/assign the Bead's ID: if b.ID is already set it must match its
//     recomputed content hash (bead.Verify); if unset, it is computed and
//     assigned (bead.WithID).
//
//  2. Reject unknown parents: every parent listed in b.Parents must already
//     be indexed. This is what makes the DAG structurally acyclic — a Bead
//     can only name parents that were durably written (and hence indexed)
//     strictly before it, so no chain of parent pointers can ever loop back
//     to a not-yet-written descendant. Unlike v2's per-write hasAncestor BFS
//     (walking up from the new bead's parents to check whether it is
//     already its own ancestor), this check is O(len(parents)) and needs no
//     graph walk at all: existence-in-index is sufficient because the
//     append-only write order already guarantees a parent-before-child
//     timeline. amends/retracts targets are subject to the identical
//     existence check (requireBeadsIndexed), for the identical structural
//     reason — see bead.Bead's doc comment on why an amends/retracts cycle
//     is impossible by construction, not merely rejected here.
//
//     Before the parent-existence check, a retraction, clinical attestation,
//     or signature_attestation Bead is additionally required to name its
//     subject in Parents (see
//     requireSubjectInParents, specs/U4_state_derivation.md's "穴1" fix):
//     resolvePatientRoot below falls back to the shared Pod ("") when
//     Parents is empty, so one of these Beads that pointed at its
//     subject only via Retracts/(a future attestation target field) — never
//     via Parents — would silently land in the shared Pod, escaping its
//     subject's per-patient Pod entirely. That would make it invisible to
//     ListPatientBeads(patientRoot) and therefore invisible to the U4
//     record_state projector, which walks a patient's Beads to resolve
//     corrections: a retraction the projector never sees is a retraction
//     that never takes effect, which is a clinical safety hole (an
//     entered-in-error record staying "active"). Requiring Parents to name
//     the subject keeps patient_root resolution uniform: subject-in-parents
//     always resolves to the subject's own patient Pod.
//
//  3. Pre-resolve patient_root (see resolvePatientRoot): patient_registration
//     Beads are their own root; other Beads inherit their parents'
//     patient_root (single IN query, no N+1), falling back to the shared
//     Pod ("") when there are no parents or the parents disagree.
//
//  4. Reject cross-patient amends/retracts (specs/DESIGN_v3.1_draft.md §2:
//     "cross-patient の amends/retracts は禁止(ingest 時拒否)"): every Bead
//     named in b.Amends/b.Retracts must resolve to the same patient_root as
//     this Bead itself (see requireSamePatientRoot). This mirrors the
//     existing "cross-patient parents は原則禁止" convention for Parents,
//     applied to amends/retracts instead of being silently absorbed into a
//     shared-Pod fallback the way a Parents mismatch is — an amends/retracts
//     reference crossing patients is always a caller error, never a
//     legitimate shared-Bead reference (those go through Evidence instead,
//     per DESIGN §2).
//
//  5. Append to the resolved Pod (fsync included) via this Engine's per-path
//     Writer, then IndexBead in one transaction — "正本が常に先、インデックス
//     は追いつける". When OpenWithOptions enabled automatic projection, that
//     same transaction also replaces this patient's clinical_links, updates
//     record_state, and advances patient_projection_state. If the process
//     crashes after the Pod append, the next automatic Open runs CatchUp and
//     reprojects only the patient whose watermark is behind (see open.go and
//     specs/R10_incremental_patient_projection.md).
//
//  6. Idempotent replay: if b.ID is already indexed, Ingest returns success
//     without writing anything a second time. A caller retrying a network
//     call or a batch importer resuming after a partial failure cannot tell,
//     from Ingest's return value alone, whether an identical Bead it is
//     re-submitting was already durably stored — treating "already indexed"
//     as success (rather than an error) is what makes retries safe, and is
//     sound specifically because Bead IDs are content hashes: two Ingest
//     calls with the same ID are, by construction, the same Bead content.
func (e *Engine) Ingest(b bead.Bead) (bead.Bead, error) {
	e.ingestMu.Lock()
	defer e.ingestMu.Unlock()

	b, err := verifyOrAssignID(b)
	if err != nil {
		return bead.Bead{}, fmt.Errorf("engine: ingest: %w", err)
	}

	// Idempotent replay: a Bead already indexed under this ID is a no-op
	// success (see doc comment above). Checked before the parent-existence
	// check so that re-ingesting a Bead whose parents have since been
	// pruned from consideration (not that pruning exists in this
	// append-only design, but the check-order itself should not assume it
	// doesn't) never fails a request that already succeeded once.
	if _, err := e.idx.GetBead(b.ID); err == nil {
		return b, nil
	} else if !errors.Is(err, index.ErrNotFound) {
		return bead.Bead{}, fmt.Errorf("engine: ingest %s: check existing: %w", b.ID, err)
	}

	normalized := bead.Normalize(b)

	if err := requireSubjectInParents(normalized); err != nil {
		return bead.Bead{}, fmt.Errorf("engine: ingest %s: %w", b.ID, err)
	}
	if err := e.requireParentsIndexed(normalized.Parents); err != nil {
		return bead.Bead{}, fmt.Errorf("engine: ingest %s: %w", b.ID, err)
	}
	if err := e.requireBeadsIndexed(normalized.Amends); err != nil {
		return bead.Bead{}, fmt.Errorf("engine: ingest %s: amends: %w", b.ID, err)
	}
	if err := e.requireBeadsIndexed(normalized.Retracts); err != nil {
		return bead.Bead{}, fmt.Errorf("engine: ingest %s: retracts: %w", b.ID, err)
	}

	patientRoot, err := e.resolvePatientRoot(normalized)
	if err != nil {
		return bead.Bead{}, fmt.Errorf("engine: ingest %s: %w", b.ID, err)
	}

	if err := e.requireSamePatientRoot(patientRoot, normalized.Amends, "amends"); err != nil {
		return bead.Bead{}, fmt.Errorf("engine: ingest %s: %w", b.ID, err)
	}
	if err := e.requireSamePatientRoot(patientRoot, normalized.Retracts, "retracts"); err != nil {
		return bead.Bead{}, fmt.Errorf("engine: ingest %s: %w", b.ID, err)
	}

	podPath, err := e.podPathFor(patientRoot)
	if err != nil {
		return bead.Bead{}, fmt.Errorf("engine: ingest %s: %w", b.ID, err)
	}

	w, err := e.writers.get(podPath)
	if err != nil {
		return bead.Bead{}, fmt.Errorf("engine: ingest %s: %w", b.ID, err)
	}

	// meta is captured before Append so its WrittenAt (the actual write
	// instant, per pod.Meta's doc comment) survives into loc below exactly as
	// pod.NewMeta set it: Append takes meta by value and only ever sets its
	// own local copy's Clearance/Signature fields (see Writer.Append's doc
	// comment) — it cannot mutate this variable — but reading WrittenAt from
	// the same variable we pass in, rather than re-deriving it after the
	// call, keeps this call site correct even if that ever changed.
	meta := pod.NewMeta(patientRoot)
	res, err := w.Append(normalized, pod.CodecZstd, meta)
	if err != nil {
		return bead.Bead{}, fmt.Errorf("engine: ingest %s: pod append: %w", b.ID, err)
	}

	tx, err := e.idx.SQLDB().Begin()
	if err != nil {
		return bead.Bead{}, fmt.Errorf("engine: ingest %s: begin index tx: %w", b.ID, err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after commit is a no-op

	// pods.path is stored dataDir-relative (Store.RelPath), not as podPath's
	// own (possibly relative-to-cwd, possibly absolute) form — see this
	// task's pods.path portability fix: a stored path must remain valid
	// regardless of which cwd or dataDir-absolute-location a later process
	// reopens this data directory from.
	relPodPath, err := e.podStore.RelPath(podPath)
	if err != nil {
		return bead.Bead{}, fmt.Errorf("engine: ingest %s: %w", b.ID, err)
	}
	loc := index.BeadLocation{
		PodPath:     relPodPath,
		PatientRoot: patientRoot,
		Offset:      res.Offset,
		Length:      res.Length,
		WrittenAt:   meta.WrittenAt,
	}
	if err := index.IndexBead(tx, normalized, loc, e.flattener); err != nil {
		return bead.Bead{}, fmt.Errorf("engine: ingest %s: index: %w", b.ID, err)
	}
	if err := e.projectAppendedPatientBead(
		tx,
		normalized,
		patientRoot,
		relPodPath,
		res.Offset+res.Length,
		meta.WrittenAt,
	); err != nil {
		return bead.Bead{}, fmt.Errorf("engine: ingest %s: project patient: %w", b.ID, err)
	}
	if err := tx.Commit(); err != nil {
		return bead.Bead{}, fmt.Errorf("engine: ingest %s: commit index and projection: %w", b.ID, err)
	}

	return normalized, nil
}

// verifyOrAssignID implements Ingest step 1: an already-ID'd Bead must
// verify against its own content hash; an ID-less Bead gets one computed and
// assigned.
func verifyOrAssignID(b bead.Bead) (bead.Bead, error) {
	if b.ID == "" {
		withID, err := bead.WithID(b)
		if err != nil {
			return bead.Bead{}, fmt.Errorf("compute ID: %w", err)
		}
		return withID, nil
	}
	if err := bead.Verify(b); err != nil {
		return bead.Bead{}, fmt.Errorf("verify: %w", err)
	}
	return b, nil
}

// requireSubjectInParents rejects a retraction, clinical attestation, or
// cryptographic signature_attestation whose Parents is empty
// (specs/U4_state_derivation.md's "穴1" fix — see
// Ingest's doc comment on step 2 for why): these types name their subject
// (the Bead being retracted/attested) so a caller can always supply it in
// Parents too, and doing so is what keeps resolvePatientRoot from falling
// back to the shared Pod for a Bead that structurally belongs to one
// patient. This is a shape check only (Parents non-empty); it does not by
// itself confirm the named parent is the same Bead as Retracts[0] or an
// attestation's signed/clinical target. The clinical state projector and
// trust verifier perform those type-specific equality checks; this generic
// ingest gate guarantees that the Bead is stored in its parent's patient Pod.
func requireSubjectInParents(b bead.Bead) error {
	if b.Type != "retraction" && b.Type != "attestation" && b.Type != "signature_attestation" {
		return nil
	}
	if len(b.Parents) == 0 {
		return fmt.Errorf("%s Bead must name its subject in parents (empty parents would resolve to the shared Pod, escaping per-patient scoping)", b.Type)
	}
	return nil
}

// requireParentsIndexed rejects a Bead whose parents are not all already
// indexed (Ingest step 2 / DAG-acyclicity guarantee — see Ingest's doc
// comment). parents should already be normalized (deduplicated) by the
// caller; this still works correctly on an unnormalized slice, just doing
// one redundant lookup per duplicate.
func (e *Engine) requireParentsIndexed(parents []string) error {
	return e.requireBeadsIndexed(parents)
}

// requireBeadsIndexed rejects a Bead that references any not-yet-indexed
// Bead ID in ids — the same existence check requireParentsIndexed applies to
// Parents, generalized so Ingest can also apply it to Amends/Retracts (see
// Ingest's doc comment, step 2). ids should already be normalized
// (deduplicated) by the caller; this still works correctly on an
// unnormalized slice, just doing one redundant lookup per duplicate.
func (e *Engine) requireBeadsIndexed(ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	roots, err := e.idx.PatientRootsFor(ids)
	if err != nil {
		return fmt.Errorf("check beads exist: %w", err)
	}
	for _, id := range ids {
		if _, ok := roots[id]; !ok {
			return fmt.Errorf("bead %s is not indexed (referenced beads must be written first)", id)
		}
	}
	return nil
}

// requireSamePatientRoot rejects a Bead whose amends/retracts targets do not
// all share patientRoot — Ingest step 4's "cross-patient の amends/retracts
// は禁止" (specs/DESIGN_v3.1_draft.md §2). fieldName ("amends" or "retracts")
// is included in the error only for diagnostics. ids must already have been
// confirmed to exist via requireBeadsIndexed (called earlier in Ingest), so
// every id here is guaranteed present in PatientRootsFor's result map.
func (e *Engine) requireSamePatientRoot(patientRoot string, ids []string, fieldName string) error {
	if len(ids) == 0 {
		return nil
	}
	roots, err := e.idx.PatientRootsFor(ids)
	if err != nil {
		return fmt.Errorf("check %s patient_root: %w", fieldName, err)
	}
	for _, id := range ids {
		root, ok := roots[id]
		if !ok {
			// requireBeadsIndexed already checked this; reaching here would
			// mean the target vanished from the index mid-ingest, which
			// cannot happen in this append-only, single-Engine-per-data-dir
			// design (see resolvePatientRoot's identical defensive check),
			// but fail loudly rather than silently skipping the
			// cross-patient check if it somehow did.
			return fmt.Errorf("%s target %s vanished from index mid-ingest", fieldName, id)
		}
		if root != patientRoot {
			return fmt.Errorf(
				"%s target %s belongs to patient_root %q, this Bead resolves to %q (cross-patient %s is rejected)",
				fieldName, id, root, patientRoot, fieldName,
			)
		}
	}
	return nil
}

// resolvePatientRoot implements Ingest step 3 (specs/DESIGN_v3.md §3):
//
//   - type == "patient_registration": the Bead is its own root.
//   - no parents (and not a registration): the shared Pod ("").
//   - parents present: inherit patient_root from the parents, resolved via a
//     single PatientRootsFor IN-query (no N+1 — one query regardless of how
//     many parents a Bead has). If every parent shares the same
//     patient_root, the child inherits it. If the parents disagree (a Bead
//     that merges lineages from more than one patient, or from a patient
//     Pod and the shared Pod), the child falls back to the shared Pod: no
//     single patient_root can honestly describe it.
//
// b.Parents must already be normalized (deduplicated); e.requireParentsIndexed
// must have already confirmed every parent is indexed, so PatientRootsFor's
// result map is guaranteed to have an entry for every parent here.
func (e *Engine) resolvePatientRoot(b bead.Bead) (string, error) {
	if b.Type == "patient_registration" {
		return b.ID, nil
	}
	if len(b.Parents) == 0 {
		return "", nil
	}

	roots, err := e.idx.PatientRootsFor(b.Parents)
	if err != nil {
		return "", fmt.Errorf("resolve patient_root: %w", err)
	}

	var resolved string
	first := true
	for _, p := range b.Parents {
		root, ok := roots[p]
		if !ok {
			// requireParentsIndexed already checked this; reaching here
			// would mean a parent was removed between the two calls, which
			// cannot happen in this append-only, single-Engine-per-data-dir
			// design, but fail loudly rather than silently treating it as
			// "shared" if it somehow did.
			return "", fmt.Errorf("resolve patient_root: parent %s vanished from index mid-ingest", p)
		}
		if first {
			resolved = root
			first = false
			continue
		}
		if root != resolved {
			// Parents span more than one root (or a mix of patient and
			// shared): no single patient_root applies.
			return "", nil
		}
	}
	return resolved, nil
}

// podPathFor returns the Pod file path a Bead with the given (already
// resolved) patient_root should be appended to: the patient's own Pod, or
// the shared Pod for "" (specs/DESIGN_v3.md §3). It ensures the destination
// directory exists (EnsurePatientPodDir for a patient Pod; the pods/
// directory itself was already ensured by Open for the shared Pod).
func (e *Engine) podPathFor(patientRoot string) (string, error) {
	if patientRoot == "" {
		return e.podStore.SharedPodPath(), nil
	}
	path, err := e.podStore.EnsurePatientPodDir(patientRoot)
	if err != nil {
		return "", fmt.Errorf("resolve pod path for patient_root %s: %w", patientRoot, err)
	}
	return path, nil
}
