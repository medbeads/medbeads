package engine

import (
	"errors"
	"fmt"

	"github.com/medbeads/medbeads/internal/engine/bead"
	"github.com/medbeads/medbeads/internal/engine/index"
	"github.com/medbeads/medbeads/internal/engine/pod"
)

// Ingest is the write protocol's single entry point (specs/DESIGN_v3.md §3):
//
//  1. Verify/assign the Bead's ID: if b.ID is already set it must match its
//     recomputed content hash (bead.Verify); if unset, it is computed and
//     assigned (bead.WithID).
//  2. Reject unknown parents: every parent listed in b.Parents must already
//     be indexed. This is what makes the DAG structurally acyclic — a Bead
//     can only name parents that were durably written (and hence indexed)
//     strictly before it, so no chain of parent pointers can ever loop back
//     to a not-yet-written descendant. Unlike v2's per-write hasAncestor BFS
//     (walking up from the new bead's parents to check whether it is
//     already its own ancestor), this check is O(len(parents)) and needs no
//     graph walk at all: existence-in-index is sufficient because the
//     append-only write order already guarantees a parent-before-child
//     timeline.
//  3. Pre-resolve patient_root (see resolvePatientRoot): patient_registration
//     Beads are their own root; other Beads inherit their parents'
//     patient_root (single IN query, no N+1), falling back to the shared
//     Pod ("") when there are no parents or the parents disagree.
//  4. Append to the resolved Pod (fsync included) via this Engine's per-path
//     Writer, then IndexBead in one transaction — "正本が常に先、インデックス
//     は追いつける": if the process crashes between these two steps, the next
//     Open's CatchUp recovers it (see open.go).
//  5. Idempotent replay: if b.ID is already indexed, Ingest returns success
//     without writing anything a second time. A caller retrying a network
//     call or a batch importer resuming after a partial failure cannot tell,
//     from Ingest's return value alone, whether an identical Bead it is
//     re-submitting was already durably stored — treating "already indexed"
//     as success (rather than an error) is what makes retries safe, and is
//     sound specifically because Bead IDs are content hashes: two Ingest
//     calls with the same ID are, by construction, the same Bead content.
func (e *Engine) Ingest(b bead.Bead) (bead.Bead, error) {
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

	if err := e.requireParentsIndexed(normalized.Parents); err != nil {
		return bead.Bead{}, fmt.Errorf("engine: ingest %s: %w", b.ID, err)
	}

	patientRoot, err := e.resolvePatientRoot(normalized)
	if err != nil {
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

	res, err := w.Append(normalized, pod.CodecZstd, pod.NewMeta(patientRoot))
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
	}
	if err := index.IndexBead(tx, normalized, loc, e.flattener); err != nil {
		return bead.Bead{}, fmt.Errorf("engine: ingest %s: index: %w", b.ID, err)
	}
	if err := tx.Commit(); err != nil {
		return bead.Bead{}, fmt.Errorf("engine: ingest %s: commit index: %w", b.ID, err)
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

// requireParentsIndexed rejects a Bead whose parents are not all already
// indexed (Ingest step 2 / DAG-acyclicity guarantee — see Ingest's doc
// comment). parents should already be normalized (deduplicated) by the
// caller; this still works correctly on an unnormalized slice, just doing
// one redundant lookup per duplicate.
func (e *Engine) requireParentsIndexed(parents []string) error {
	if len(parents) == 0 {
		return nil
	}
	roots, err := e.idx.PatientRootsFor(parents)
	if err != nil {
		return fmt.Errorf("check parents exist: %w", err)
	}
	for _, p := range parents {
		if _, ok := roots[p]; !ok {
			return fmt.Errorf("parent %s is not indexed (parents must be written before their children)", p)
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
