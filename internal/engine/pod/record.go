package pod

import (
	"encoding/json"
	"fmt"
	"time"
)

// Meta is the frame's meta_bytes payload: minimal derived/administrative
// information about a write, as JSON. Per specs/DESIGN_v3.md §3, patient_root
// is derived information that lives only here (frame meta) and in the index
// — it is NOT a field of bead.Bead and is NOT part of the Bead content hash.
// meta_bytes is covered by the frame's CRC-32C (so bit-rot in it is still
// detected), but it is explicitly outside VerifyPod's self-verification
// check (decompress(core_bytes) -> sha256 == bead_id), since Meta carries no
// claim about Bead content.
type Meta struct {
	// PatientRoot is the plain-hex Bead ID of the patient_registration root
	// this Bead belongs to, or "" for a Bead stored in the shared Pod.
	PatientRoot string `json:"patient_root,omitempty"`
	// WrittenAt is the wall-clock time this frame was appended, RFC 3339.
	WrittenAt string `json:"written_at"`
}

// NewMeta returns a Meta for a write happening now, with patientRoot as
// given ("" for the shared Pod).
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
