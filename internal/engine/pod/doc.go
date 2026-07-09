// Package pod implements the Pod append-only pack file format: Writer,
// Reader, Scanner, and CRC verification. See specs/DESIGN_v3.md §2, §3.
//
// # Layout
//
// Under a MedBeads data directory, one Pod file holds all Beads for a single
// patient (keyed by their patient_root Bead ID), plus one shared Pod for
// Beads with no single patient (see Store):
//
//	pods/<root first 2 hex>/<root 64 hex>.pod
//	pods/_shared.pod
//
// # Frame format
//
// Every record in a Pod is one length-prefixed, checksummed frame, all
// multi-byte integers little-endian (see frame.go):
//
//	magic     uint16   FrameMagic (0xB6AD)
//	flags     uint8    codec ID in low 4 bits (Codec*); high bits reserved, must be 0
//	core_len  uint32   len(core_bytes)
//	meta_len  uint32   len(meta_bytes)
//	crc32c    uint32   CRC-32C (Castagnoli) over bead_id || core_bytes || meta_bytes
//	bead_id   [32]byte raw SHA-256 digest (not hex)
//	core_bytes  [core_len]byte
//	meta_bytes  [meta_len]byte
//
// core_bytes is bead.Canonicalize's JCS output, compressed per the frame's
// codec. Decompressing it and hashing the result with SHA-256 must reproduce
// bead_id exactly — this is the frame's self-verification property (see
// VerifyPod) and is independent of the CRC, which only proves the bytes were
// not corrupted at rest, not that they are semantically a valid Bead.
//
// meta_bytes is a small JSON blob (see Meta) carrying derived/administrative
// data — currently patient_root and the write timestamp. It is covered by
// the CRC (so bit-rot there is still caught) but explicitly outside
// self-verification: Meta makes no claim that participates in bead_id.
//
// # Compression
//
// Three codecs are representable in the frame's flags byte: CodecRaw,
// CodecZstd, and CodecZstdDict (github.com/klauspost/compress/zstd, pure
// Go). Only CodecRaw and CodecZstd have a writer path today. CodecZstdDict's
// wire format, codec ID, and decompression path (decompressWithDict) exist
// so a future dictionary-aware writer is additive, but no dictionary is
// trained yet — that happens during the v2->v3 migration (R7.2), which has
// the corpus needed to train one. Until then, Append's default codec is
// plain zstd (no dictionary).
//
// # Write protocol
//
// A Writer owns exactly one Pod file and serializes Append calls against it
// with a mutex (specs/DESIGN_v3.md §3: "並行性: Pod ごとに単一ライター"). Each
// Append writes one full frame and fsyncs before returning, and reports the
// frame's (offset, length) for the caller (the index writer) to record.
// There is no partial-frame write visible to readers: a frame only exists
// once its bytes (including the trailing meta_bytes) are on disk.
//
// # Recovery: tail-truncate
//
// A crash mid-Append can leave a Pod file ending in a partially-written
// frame. Scan reads sequentially from the start and, on hitting a
// short/incomplete frame or a bad magic number, stops and reports
// ValidUpto — the offset of the last complete, well-formed frame — rather
// than failing the whole scan. Truncate cuts the file to ValidUpto so that
// subsequent Append calls resume cleanly. Neither Scan nor VerifyPod calls
// Truncate automatically; executing it is always the caller's decision.
package pod
