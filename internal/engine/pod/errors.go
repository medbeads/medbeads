package pod

import "errors"

// Sentinel errors returned by this package. Callers should use errors.Is to
// test for these rather than matching error strings.
var (
	// ErrBadMagic means a frame header's magic number did not match
	// FrameMagic — the data at this offset is not a Pod frame (corruption,
	// wrong offset, or a truncated/partial frame overlapping this position).
	ErrBadMagic = errors.New("pod: bad frame magic")

	// ErrUnknownCodec means a frame header named a codec ID this build does
	// not know how to decompress.
	ErrUnknownCodec = errors.New("pod: unknown codec")

	// ErrCRCMismatch means a frame's stored CRC-32C does not match the
	// recomputed CRC over its bead_id/core_bytes/meta_bytes — the frame's
	// bytes were altered or corrupted after being written.
	ErrCRCMismatch = errors.New("pod: CRC mismatch")

	// ErrShortFrame means a frame's header claimed a payload the underlying
	// file does not have enough remaining bytes to contain — i.e. the file
	// ends in the middle of a frame. This is the expected shape of damage
	// from a crash mid-Append and is recovered from via tail-truncate (see
	// Scan / Truncate in scanner.go), not necessarily a sign of bit-rot.
	ErrShortFrame = errors.New("pod: short/incomplete frame at EOF")

	// ErrSelfVerifyMismatch means core_bytes decompresses successfully and
	// its CRC is valid, but sha256(decompressed core_bytes) does not equal
	// the frame's bead_id — the strongest corruption signal Verify checks.
	ErrSelfVerifyMismatch = errors.New("pod: core_bytes does not hash to bead_id")

	// ErrMetaDecode means a frame's meta_bytes did not parse as the expected
	// JSON shape (Meta). Scan treats this the same as ErrShortFrame/
	// ErrBadMagic/ErrCRCMismatch: a form of frame-level damage that stops the
	// scan at that offset (reporting it via ScanResult.Damaged/DamageErr)
	// rather than aborting the whole scan and discarding frames already read
	// successfully before it. This can only reach a caller when CRC
	// verification was skipped or somehow passed despite corruption (with
	// crcTarget now covering core_len/meta_len/flags, a corrupt meta_bytes
	// length or content will normally be caught as ErrCRCMismatch first —
	// this sentinel exists as defense in depth, not as the primary detector).
	ErrMetaDecode = errors.New("pod: meta_bytes decode failed")
)
