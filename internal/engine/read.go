package engine

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/medbeads/medbeads/internal/engine/bead"
	"github.com/medbeads/medbeads/internal/engine/index"
	"github.com/medbeads/medbeads/internal/engine/pod"
)

// GetBead resolves id's storage location via the index, reads its frame from
// the owning Pod (with CRC-32C verification — ReadAtVerified, so storage
// corruption is always caught), decompresses and unmarshals it back into a
// bead.Bead — a thin read-side mirror of Ingest's write-side guarantees (R3's
// "読み取り API" scope: no graph traversal here yet, see specs/DESIGN_v3.md
// §2's graph package for that).
//
// # Why this does not re-verify the content hash (bead.Verify) on every call
//
// GetBead does NOT recompute bead.Verify's JCS-canonicalize-then-SHA256
// check by default. That is a deliberate scope split, not an oversight:
//
//   - CRC-32C (always on here) detects storage corruption/bit-rot — accidental
//     damage to bytes already on disk — cheaply, and covers bead_id/core_bytes/
//     meta_bytes together (see pod's crcTarget), so a corrupted frame is
//     always caught before this function returns a Bead built from it.
//   - bead.Verify's JCS re-canonicalization is a tamper-evidence check: it
//     proves the returned Bead's content still matches the hash a client
//     trusted as its identity. That guarantee's designed home is
//     `medbeadsd verify` / the verify_integrity MCP tool (pod.VerifyAll),
//     which already runs a full per-frame self-verify pass
//     (sha256(decompress(core_bytes)) == bead_id — see pod.Record.SelfVerify)
//     over the entire store; paying that cost again on every single
//     GetBead call (JCS canonicalization is the expensive part — see
//     graph.LoadBundle's doc comment) is redundant with that separate,
//     already-fast, already-run verification path and was the dominant cost
//     in profiling patient-bundle reads (docs/requirements.md §7's <10ms
//     target).
//   - Use GetBeadVerified for the (much rarer) caller that wants the
//     stronger per-Bead guarantee inline, e.g. an integrity spot-check tool.
//
// Residual risk, stated explicitly: CRC-32C detects accidental corruption
// only. It is a linear code — an adversary who can rewrite raw Pod bytes can
// also recompute the frame's crc32c field, so deliberate tampering passes
// this check. Tamper-evidence (sha256 == bead ID) is the job of
// GetBeadVerified and `medbeadsd verify` / verify_integrity, not of the hot
// read path.
//
// See also graph.LoadBundle's own doc comment for the identical reasoning
// applied to whole-patient bundle loads.
func (e *Engine) GetBead(id string) (bead.Bead, error) {
	ref, err := e.idx.GetBead(id)
	if err != nil {
		return bead.Bead{}, fmt.Errorf("engine: get bead %s: %w", id, err)
	}
	// ref.PodPath is stored dataDir-relative (see index.Reindex/CatchUp's
	// doc comments on this task's pods.path portability fix); AbsPath joins
	// it against this Engine's own dataDir (also tolerating a pre-existing
	// store where pods.path still holds an absolute path from before this
	// normalization existed — AbsPath passes that through unchanged).
	b, err := readBeadAt(e.podStore.AbsPath(ref.PodPath), ref.Offset, false)
	if err != nil {
		return bead.Bead{}, fmt.Errorf("engine: get bead %s: %w", id, err)
	}
	return b, nil
}

// GetBeadVerified is GetBead plus pod.Record.SelfVerify: after the usual
// CRC-32C check, it also decompresses core_bytes and checks
// sha256(core_bytes) == the frame's bead_id, the same self-verification
// pod.VerifyAll performs (see GetBead's doc comment for why that is not the
// default). It does not use bead.Verify's JCS re-canonicalization either —
// SelfVerify is a cheaper, equally-conclusive way to ask the same question,
// since core_bytes on disk already *is* bead.Canonicalize's output (see
// pod.Record.SelfVerify's doc comment) — but it is strictly stronger than
// GetBead's CRC-only default, for callers (e.g. a future per-Bead integrity
// tool) that want that guarantee on a single read without running a full
// pod.VerifyAll pass.
func (e *Engine) GetBeadVerified(id string) (bead.Bead, error) {
	ref, err := e.idx.GetBead(id)
	if err != nil {
		return bead.Bead{}, fmt.Errorf("engine: get bead %s: %w", id, err)
	}
	b, err := readBeadAt(e.podStore.AbsPath(ref.PodPath), ref.Offset, true)
	if err != nil {
		return bead.Bead{}, fmt.Errorf("engine: get bead %s: %w", id, err)
	}
	return b, nil
}

// ListPatientBeads returns every Bead indexed under patientRoot (the
// patient_registration Bead's own ID), ordered by timestamp, with each
// Bead's full content read back from its Pod. patientRoot must be non-empty
// (per index.DB.ListPatientBeads). Every frame is read with CRC-32C
// verification (ReadAtVerified); see GetBead's doc comment for why the
// (much more expensive) JCS content-hash re-verify is not also run here by
// default.
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
	return e.readPatientBeadRefs(patientRoot, refs)
}

// listPatientBeadsTx is ListPatientBeads over tx's snapshot. Automatic
// projection calls it after IndexBead and before commit, so a correction-state
// rebuild includes the newly appended Bead without opening a second SQLite
// connection (index.DB intentionally caps its pool at one).
func (e *Engine) listPatientBeadsTx(tx *sql.Tx, patientRoot string) ([]bead.Bead, error) {
	rows, err := tx.Query(`
		SELECT b.id, COALESCE(b.patient_root, ''), b.type, b.timestamp,
		       p.path, b.offset, b.length, COALESCE(b.summary, '')
		FROM beads b
		JOIN pods p ON p.pod_id = b.pod_id
		WHERE b.patient_root = ?
		ORDER BY b.timestamp, b.id`, patientRoot)
	if err != nil {
		return nil, fmt.Errorf("engine: list patient beads %s in tx: %w", patientRoot, err)
	}

	var refs []index.BeadRef
	for rows.Next() {
		var ref index.BeadRef
		if err := rows.Scan(&ref.ID, &ref.PatientRoot, &ref.Type, &ref.Timestamp,
			&ref.PodPath, &ref.Offset, &ref.Length, &ref.Summary); err != nil {
			rows.Close()
			return nil, fmt.Errorf("engine: list patient beads %s in tx: scan: %w", patientRoot, err)
		}
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("engine: list patient beads %s in tx: %w", patientRoot, err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("engine: list patient beads %s in tx: close rows: %w", patientRoot, err)
	}
	return e.readPatientBeadRefs(patientRoot, refs)
}

func (e *Engine) readPatientBeadRefs(patientRoot string, refs []index.BeadRef) ([]bead.Bead, error) {
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
		// ref.PodPath is stored dataDir-relative (see index.Reindex/CatchUp's
		// doc comments); resolve to a real, openable path against this
		// Engine's own dataDir before using it, and key the reader cache on
		// that resolved path so it stays correct regardless of what string
		// form pods.path happens to hold.
		absPath := e.podStore.AbsPath(ref.PodPath)
		r, ok := readers[absPath]
		if !ok {
			opened, err := pod.OpenReader(absPath)
			if err != nil {
				return nil, fmt.Errorf("engine: list patient beads %s: open %s: %w", patientRoot, absPath, err)
			}
			readers[absPath] = opened
			r = opened
		}

		rec, err := r.ReadAtVerified(ref.Offset)
		if err != nil {
			return nil, fmt.Errorf("engine: list patient beads %s: read %s at %d: %w", patientRoot, absPath, ref.Offset, err)
		}
		b, err := decodeBeadRecord(rec, false)
		if err != nil {
			return nil, fmt.Errorf("engine: list patient beads %s: decode %s: %w", patientRoot, ref.ID, err)
		}
		out = append(out, b)
	}
	return out, nil
}

// readBeadAt opens podPath, reads the single frame at offset with CRC-32C
// verification (ReadAtVerified), and decodes it into a bead.Bead —
// optionally also running pod.Record.SelfVerify if selfVerify is true (see
// GetBead/GetBeadVerified). It is factored out of both so a single lookup
// does not need a long-lived *pod.Reader.
func readBeadAt(podPath string, offset int64, selfVerify bool) (bead.Bead, error) {
	r, err := pod.OpenReader(podPath)
	if err != nil {
		return bead.Bead{}, fmt.Errorf("open %s: %w", podPath, err)
	}
	defer r.Close() //nolint:errcheck // read-only handle, nothing to flush

	rec, err := r.ReadAtVerified(offset)
	if err != nil {
		return bead.Bead{}, fmt.Errorf("read %s at %d: %w", podPath, offset, err)
	}
	return decodeBeadRecord(rec, selfVerify)
}

// decodeBeadRecord decompresses rec's core_bytes, unmarshals it into a
// bead.Bead, and restores its ID (core_bytes' JCS payload has no "id" field
// — see bead.Canonicalize) and its Clearance/Signature from rec.Meta (the
// hash-excluded fields' designed storage location — see pod.Meta's doc
// comment).
//
// If selfVerify is true, it also runs rec.SelfVerify (decompress + SHA-256
// == bead_id) before returning — see GetBead/GetBeadVerified's doc comments
// for when a caller should ask for that. It deliberately never calls
// bead.Verify (the JCS-re-canonicalize content-hash check): that check is
// this function's caller's responsibility to opt into at the coarser
// granularity of which entry point it used, not something decodeBeadRecord
// itself should default to paying on every read.
func decodeBeadRecord(rec pod.Record, selfVerify bool) (bead.Bead, error) {
	plain, err := rec.Decompress()
	if err != nil {
		return bead.Bead{}, fmt.Errorf("decompress: %w", err)
	}
	if selfVerify {
		if err := rec.SelfVerify(); err != nil {
			return bead.Bead{}, fmt.Errorf("self-verify: %w", err)
		}
	}
	var b bead.Bead
	if err := json.Unmarshal(plain, &b); err != nil {
		return bead.Bead{}, fmt.Errorf("unmarshal: %w", err)
	}
	b.ID = rec.BeadID
	b.Clearance = rec.Meta.Clearance
	b.Signature = rec.Meta.Signature
	return b, nil
}
