package pod

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/medbeads/medbeads/internal/engine/bead"
)

// Meta is the frame's meta_bytes payload: minimal derived/administrative
// information about a write, as JSON. Per specs/DESIGN_v3.md §3, patient_root
// is derived information that lives only here (frame meta) and in the index
// — it is NOT a field of bead.Bead and is NOT part of the Bead content hash.
// meta_bytes is covered by the frame's CRC-32C (so bit-rot in it is still
// detected), but it is explicitly outside VerifyPod's self-verification
// check (decompress(core_bytes) -> sha256 == bead_id), since Meta carries no
// claim about Bead content.
//
// # Clearance / Signature placement (lead decision, see task history)
//
// bead.Bead.Clearance and bead.Bead.Signature are, by design (specs/
// DESIGN_v3.md §4), excluded from the content hash and therefore never
// appear in a frame's core_bytes (bead.Canonicalize's hashPayload has no
// such fields — see bead.go). DESIGN §3 describes meta_bytes itself as
// "minimal... 導出情報" (derived information) carried outside the hash — the
// designed placement for exactly this kind of hash-excluded-but-still-
// needs-a-home data. Storing Clearance/Signature here (rather than adding a
// field to Bead's own hashPayload, which would be a hash-format change, or a
// new frame section, which would be a frame-format change) is therefore an
// extension of meta_bytes' existing opaque-JSON contents, not a change to
// the frame layout itself (magic|flags|core_len|meta_len|crc32c|bead_id|
// core_bytes|meta_bytes is unchanged).
//
// Because Pod is append-only, a Bead's embedded Clearance/Signature is
// therefore fixed at creation time — there is no in-place update path for
// it (an already-written frame's meta_bytes cannot be edited). Any need to
// change access restrictions on an existing Bead after the fact is the DB
// clearance_rules layer's job (package clearance's Rule/SaveRule/GetRules),
// which is mutable by design; the embedded layer here is a create-time-only
// overlay. This is the "2層の役割分担" the lead's design ruling calls for:
// embedded = immutable, set once at Ingest; DB rules = mutable, added/
// removed/expired independently at any time.
type Meta struct {
	// PatientRoot is the plain-hex Bead ID of the patient_registration root
	// this Bead belongs to, or "" for a Bead stored in the shared Pod.
	PatientRoot string `json:"patient_root,omitempty"`
	// WrittenAt is the wall-clock time this frame was appended, RFC 3339.
	WrittenAt string `json:"written_at"`
	// Clearance mirrors the Bead's own bead.Clearance at the moment it was
	// appended (nil if the Bead had none). Excluded from the content hash,
	// same as bead.Bead.Clearance itself — see the type doc comment above.
	Clearance *bead.Clearance `json:"clearance,omitempty"`
	// Signature mirrors the Bead's own bead.Signature at the moment it was
	// appended ("" if the Bead had none). Excluded from the content hash,
	// same as bead.Bead.Signature itself — see the type doc comment above.
	Signature string `json:"signature,omitempty"`
}

// NewMeta returns a Meta for a write happening now, with patientRoot as
// given ("" for the shared Pod). Clearance/Signature are left unset;
// Writer.Append fills them in from the Bead being appended (see Append's
// doc comment) so every caller does not need to remember to do so itself.
func NewMeta(patientRoot string) Meta {
	return Meta{
		PatientRoot: patientRoot,
		WrittenAt:   time.Now().UTC().Format(time.RFC3339Nano),
	}
}

// encode marshals m to JSON for storage as a frame's meta_bytes.
func (m Meta) encode() ([]byte, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("pod: encode meta: %w", err)
	}
	return b, nil
}

// decodeMeta unmarshals a frame's meta_bytes back into a Meta. A malformed
// raw (not valid JSON, or valid JSON of the wrong shape) is reported via
// ErrMetaDecode rather than a bare json error, so callers that need to treat
// "meta didn't parse" as frame-level corruption (see Scan in scanner.go) can
// test for it with errors.Is.
func decodeMeta(raw []byte) (Meta, error) {
	var m Meta
	if len(raw) == 0 {
		return m, nil
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return Meta{}, fmt.Errorf("%w: %v", ErrMetaDecode, err)
	}
	return m, nil
}

// Record is a single decoded frame: the Bead ID it claims to hold, the
// still-compressed core_bytes, its codec, and decoded Meta. Readers that
// need the actual Bead JSON call Decompress; keeping core_bytes compressed
// here lets callers that only want metadata (e.g. a CRC-only scan) skip
// decompression entirely.
type Record struct {
	// BeadID is the plain lower-case hex Bead ID (bead.HexIDLen chars).
	BeadID string
	// Codec is the compression codec core_bytes is encoded with.
	Codec Codec
	// CoreBytes is the still-compressed core payload (JCS canonical Bead
	// JSON once decompressed).
	CoreBytes []byte
	// MetaBytes is the raw meta_bytes exactly as stored on disk (the bytes
	// crcTarget was computed over) — kept alongside the decoded Meta below
	// because JSON re-encoding is not guaranteed to reproduce identical
	// bytes, and CRC recomputation must use the exact on-disk bytes.
	MetaBytes []byte
	// Meta is the decoded frame metadata.
	Meta Meta
	// Offset is this frame's starting byte offset within its Pod file.
	Offset int64
	// Length is this frame's total on-disk size in bytes (header + payload),
	// i.e. the value to add to Offset to reach the next frame.
	Length int64
	// StoredCRC32C is the crc32c value read from the frame header, kept so
	// that verification code can recompute and compare against it without a
	// second disk read.
	StoredCRC32C uint32
}

// Decompress returns the decompressed core_bytes: the JCS canonical Bead
// JSON. Per specs/DESIGN_v3.md §3, sha256(result) must equal r.BeadID for a
// self-verifying frame; callers that need that guarantee should use Verify
// (verify.go) rather than re-deriving it themselves.
func (r Record) Decompress() ([]byte, error) {
	return decompress(r.Codec, r.CoreBytes)
}

// SelfVerify decompresses r.CoreBytes and checks that sha256(plain) equals
// r.BeadID, returning ErrSelfVerifyMismatch if not. It is the same check
// VerifyPod/verifyRecord performs on every frame during a full-Pod verify
// pass, exposed here so a single-Record reader (engine.GetBead,
// graph.LoadBundle) can opt into it too without duplicating the hex-decode +
// hash-compare logic.
//
// This is deliberately NOT bead.Verify: bead.Verify re-derives core_bytes'
// exact on-disk form from a decoded bead.Bead struct via JCS re-
// canonicalization (json.Marshal + jcs.Transform), which is redundant work
// on a read path — core_bytes already *is* bead.Canonicalize's output,
// compressed (see writer.go's Append and doc.go's frame-format notes).
// SelfVerify instead hashes the raw decompressed bytes directly, which is
// the cheaper of the two ways to ask the same question ("does this frame's
// content still match the ID it claims"). See package engine's read.go and
// package graph's bundle.go doc comments for why callers may skip even this
// on the default read path (CRC-32C, always checked separately via
// ReadAtVerified/pod.Scan(verifyCRC=true), already binds bead_id to
// core_bytes at the frame level for corruption-detection purposes).
func (r Record) SelfVerify() error {
	decoded, err := hex.DecodeString(r.BeadID)
	if err != nil || len(decoded) != idFieldSize {
		return fmt.Errorf("pod: self-verify: malformed bead_id %q", r.BeadID)
	}
	plain, err := r.Decompress()
	if err != nil {
		return fmt.Errorf("pod: self-verify: decompress: %w", err)
	}
	sum := sha256.Sum256(plain)
	got := hex.EncodeToString(sum[:])
	if got != r.BeadID {
		return fmt.Errorf("%w: bead_id=%s recomputed=%s", ErrSelfVerifyMismatch, r.BeadID, got)
	}
	return nil
}
