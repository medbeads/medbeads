package engine

import (
	"encoding/json"
	"fmt"

	"github.com/medbeads/medbeads/internal/engine/bead"
	"github.com/medbeads/medbeads/internal/engine/pod"
)

// GetBead resolves id's storage location via the index, reads its frame from
// the owning Pod, decompresses and unmarshals it back into a bead.Bead, and
// verifies its content hash before returning — a thin read-side mirror of
// Ingest's write-side guarantees (R3's "読み取り API" scope: no graph
// traversal here yet, see specs/DESIGN_v3.md §2's graph package for that).
func (e *Engine) GetBead(id string) (bead.Bead, error) {
	ref, err := e.idx.GetBead(id)
	if err != nil {
		return bead.Bead{}, fmt.Errorf("engine: get bead %s: %w", id, err)
	}
	return readBeadAt(ref.PodPath, ref.Offset)
}

// ListPatientBeads returns every Bead indexed under patientRoot (the
// patient_registration Bead's own ID), ordered by timestamp, with each
// Bead's full content read back from its Pod. patientRoot must be non-empty
// (per index.DB.ListPatientBeads).
//
// This reads one frame per Bead via pod.Reader.ReadAt rather than a single
// bulk sequential Pod scan; specs/DESIGN_v3.md §3 notes a full patient
// sub-graph is small (~900 Beads, ~300-500KB compressed) and expects the
// timeline/search use case to eventually read via one sequential Pod-bundle
// scan (package graph, a later unit) rather than per-Bead random access —
// this method is the interim thin delegation this unit's scope calls for
// (see task: "graph 的な API はまだ作らない").
func (e *Engine) ListPatientBeads(patientRoot string) ([]bead.Bead, error) {
	refs, err := e.idx.ListPatientBeads(patientRoot)
	if err != nil {
		return nil, fmt.Errorf("engine: list patient beads %s: %w", patientRoot, err)
	}

	out := make([]bead.Bead, 0, len(refs))
	// readers caches one pod.Reader per distinct Pod path within this call,
	// since every Bead in a single patient's timeline normally lives in that
	// one patient Pod (or occasionally the shared Pod) — avoiding one
	// os.Open per Bead when they all resolve to the same handful of paths.
	readers := make(map[string]*pod.Reader)
	defer func() {
		for _, r := range readers {
			r.Close() //nolint:errcheck // best-effort cleanup after a read-only pass
		}
	}()

	for _, ref := range refs {
		r, ok := readers[ref.PodPath]
		if !ok {
			opened, err := pod.OpenReader(ref.PodPath)
			if err != nil {
				return nil, fmt.Errorf("engine: list patient beads %s: open %s: %w", patientRoot, ref.PodPath, err)
			}
			readers[ref.PodPath] = opened
			r = opened
		}

		rec, err := r.ReadAt(ref.Offset)
		if err != nil {
			return nil, fmt.Errorf("engine: list patient beads %s: read %s at %d: %w", patientRoot, ref.PodPath, ref.Offset, err)
		}
		b, err := decodeBeadRecord(rec)
		if err != nil {
			return nil, fmt.Errorf("engine: list patient beads %s: decode %s: %w", patientRoot, ref.ID, err)
		}
		out = append(out, b)
	}
	return out, nil
}

// readBeadAt opens podPath, reads the single frame at offset, and decodes it
// into a verified bead.Bead. It is GetBead's implementation, factored out so
// it does not need a long-lived *pod.Reader for a single lookup.
func readBeadAt(podPath string, offset int64) (bead.Bead, error) {
	r, err := pod.OpenReader(podPath)
	if err != nil {
		return bead.Bead{}, fmt.Errorf("open %s: %w", podPath, err)
	}
	defer r.Close() //nolint:errcheck // read-only handle, nothing to flush

	rec, err := r.ReadAt(offset)
	if err != nil {
		return bead.Bead{}, fmt.Errorf("read %s at %d: %w", podPath, offset, err)
	}
	return decodeBeadRecord(rec)
}

// decodeBeadRecord decompresses rec's core_bytes, unmarshals it into a
// bead.Bead, restores its ID (core_bytes' JCS payload has no "id" field —
// see bead.Canonicalize), restores its Clearance/Signature from rec.Meta
// (the hash-excluded fields' designed storage location — see pod.Meta's doc
// comment), and verifies the recomputed hash matches — the read-side half
// of the tamper-evidence guarantee. Verify never looks at Clearance/
// Signature (bead.Verify's own doc comment), so restoring them here cannot
// affect the hash check either way.
func decodeBeadRecord(rec pod.Record) (bead.Bead, error) {
	plain, err := rec.Decompress()
	if err != nil {
		return bead.Bead{}, fmt.Errorf("decompress: %w", err)
	}
	var b bead.Bead
	if err := json.Unmarshal(plain, &b); err != nil {
		return bead.Bead{}, fmt.Errorf("unmarshal: %w", err)
	}
	b.ID = rec.BeadID
	b.Clearance = rec.Meta.Clearance
	b.Signature = rec.Meta.Signature
	if err := bead.Verify(b); err != nil {
		return bead.Bead{}, fmt.Errorf("verify: %w", err)
	}
	return b, nil
}
