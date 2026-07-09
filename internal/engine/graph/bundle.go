package graph

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"

	"github.com/medbeads/medbeads/internal/engine/bead"
	"github.com/medbeads/medbeads/internal/engine/pod"
)

// ErrPatientNotFound is returned by LoadBundle when patientRoot has no Pod
// file on disk (i.e. no Bead has ever been ingested under that patient_root).
var ErrPatientNotFound = errors.New("graph: patient not found")

// Bundle is a patient's entire Bead sub-graph, expanded into memory once
// (LoadBundle) and then queried purely via map operations (Ancestors /
// Descendants / Siblings / BuildContext) — no further disk or SQL access.
// Per specs/DESIGN_v3.md §3, this is the "桁改善の本体": a patient's
// sub-graph (~900 Beads, ~300-500KB compressed) is read with one Pod open +
// one sequential scan rather than one random-access read per Bead.
type Bundle struct {
	// PatientRoot is the patient_registration Bead ID this Bundle was loaded
	// for.
	PatientRoot string

	// beads holds every Bead in the bundle, keyed by ID.
	beads map[string]bead.Bead

	// parents maps a Bead ID to the IDs of its direct parents (from
	// bead.Bead.Parents, i.e. edge_type='parent' in index terms). children is
	// the reverse adjacency: a Bead ID to the IDs of its direct children.
	parents  map[string][]string
	children map[string][]string

	// siblings maps a Bead ID to the IDs of its explicit siblings: Beads
	// connected via a bidirectional edge_type='sibling' bead_edges row (see
	// specs/MEDBEADS_SIBLING_SPEC.md §5.2 — sibling edges are registered in
	// both directions when a sibling_link Bead is created by the APC
	// daemon). APC is not implemented yet (docs/requirements.md R5), so this
	// is empty for any Bundle loaded from real ingest data today; it exists
	// so BuildContext's explicit-sibling priority tier has something to grow
	// into, and so tests can exercise it by injecting sibling edges by hand.
	siblings map[string][]string
}

// Beads returns the number of Beads in the bundle.
func (bd *Bundle) Beads() int {
	return len(bd.beads)
}

// Get returns the Bead with id and whether it was found in the bundle.
func (bd *Bundle) Get(id string) (bead.Bead, bool) {
	b, ok := bd.beads[id]
	return b, ok
}

// AddSiblingEdge registers an explicit (bidirectional) sibling edge between
// two Beads already present in the bundle. This is how a caller (or a test)
// injects edge_type='sibling' bead_edges rows once the APC daemon exists;
// LoadBundle itself does not yet read such rows since IndexBead never writes
// them today (docs/requirements.md R5 is unimplemented — see doc comment on
// Bundle.siblings). It is a no-op if either id is not in the bundle.
func (bd *Bundle) AddSiblingEdge(a, b string) {
	if _, ok := bd.beads[a]; !ok {
		return
	}
	if _, ok := bd.beads[b]; !ok {
		return
	}
	bd.siblings[a] = appendUnique(bd.siblings[a], b)
	bd.siblings[b] = appendUnique(bd.siblings[b], a)
}

// appendUnique appends v to ss unless it is already present.
func appendUnique(ss []string, v string) []string {
	for _, s := range ss {
		if s == v {
			return ss
		}
	}
	return append(ss, v)
}

// LoadBundle loads every Bead indexed under patientRoot by opening its Pod
// file once and scanning it sequentially from start to end (pod.Scan),
// decoding each frame in file order and building the in-memory adjacency
// lists (parents/children) from each Bead's own Parents field — the "Pod
// バンドル一括読み" specs/DESIGN_v3.md §3/§6 and docs/requirements.md R4.3
// call for. It deliberately does not consult the index (no per-Bead
// index.DB.GetBead + pod.Reader.ReadAt): the Pod file itself is the single
// source of truth and a full sequential scan of one patient's small
// (~300-500KB compressed) Pod is what makes patient bundle loads meet the
// <10ms performance target (docs/requirements.md §7), versus ~900 random
// per-Bead opens/reads.
//
// LoadBundle verifies CRC-32C for every frame (pod.Scan's verifyCRC=true):
// a patient bundle feeding an agent's context should never silently
// swallow bit-rot. It returns ErrPatientNotFound if store has no Pod file
// for patientRoot yet, and an error wrapping pod.ErrShortFrame /
// pod.ErrCRCMismatch (etc.) if the Pod is damaged — LoadBundle itself never
// truncates; recovery (pod.Truncate) is a separate, explicit operation
// (see pod.Scan's doc comment).
func LoadBundle(store *pod.Store, patientRoot string) (*Bundle, error) {
	if patientRoot == "" {
		return nil, fmt.Errorf("graph: load bundle: patientRoot must not be empty")
	}

	path, err := store.PatientPodPath(patientRoot)
	if err != nil {
		return nil, fmt.Errorf("graph: load bundle %s: %w", patientRoot, err)
	}

	scan, err := pod.Scan(path, true)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("graph: load bundle %s: %w", patientRoot, ErrPatientNotFound)
		}
		return nil, fmt.Errorf("graph: load bundle %s: %w", patientRoot, err)
	}
	if scan.Damaged {
		return nil, fmt.Errorf("graph: load bundle %s: pod damaged at offset %d: %w",
			patientRoot, scan.ValidUpto, scan.DamageErr)
	}

	bd := &Bundle{
		PatientRoot: patientRoot,
		beads:       make(map[string]bead.Bead, len(scan.Records)),
		parents:     make(map[string][]string, len(scan.Records)),
		children:    make(map[string][]string, len(scan.Records)),
		siblings:    make(map[string][]string, len(scan.Records)),
	}

	for _, rec := range scan.Records {
		b, err := decodeBundleRecord(rec)
		if err != nil {
			return nil, fmt.Errorf("graph: load bundle %s: decode %s: %w", patientRoot, rec.BeadID, err)
		}
		bd.beads[b.ID] = b
	}

	// Adjacency is built as a second pass over the fully-decoded map (rather
	// than incrementally during the scan loop above) so that a child whose
	// frame happens to be read before some other unrelated Bead in the same
	// file never matters: parents/children only ever reference Beads that
	// are already known to exist in this bundle by the time edges are
	// derived, regardless of on-disk frame order.
	for id, b := range bd.beads {
		for _, p := range b.Parents {
			bd.parents[id] = appendUnique(bd.parents[id], p)
			bd.children[p] = appendUnique(bd.children[p], id)
		}
	}

	return bd, nil
}

// decodeBundleRecord decompresses rec's core_bytes, unmarshals it into a
// bead.Bead, restores its ID (core_bytes' JCS payload has no "id" field —
// see bead.Canonicalize), and verifies the recomputed hash matches. This
// mirrors engine.decodeBeadRecord (internal/engine/read.go); graph does not
// import package engine (see doc.go), so the same small decode+verify step
// is duplicated here rather than shared.
func decodeBundleRecord(rec pod.Record) (bead.Bead, error) {
	plain, err := rec.Decompress()
	if err != nil {
		return bead.Bead{}, fmt.Errorf("decompress: %w", err)
	}
	var b bead.Bead
	if err := json.Unmarshal(plain, &b); err != nil {
		return bead.Bead{}, fmt.Errorf("unmarshal: %w", err)
	}
	b.ID = rec.BeadID
	if err := bead.Verify(b); err != nil {
		return bead.Bead{}, fmt.Errorf("verify: %w", err)
	}
	return b, nil
}
