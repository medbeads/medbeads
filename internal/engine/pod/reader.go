package pod

import (
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

// Reader provides random-access reads of individual frames from a Pod file
// by byte offset (specs/DESIGN_v3.md §3: "(a) offset 指定の単一フレーム読み").
// Multiple Readers (and a concurrent Writer) may safely operate on the same
// path: Reader only ever opens the file read-only and uses ReadAt, which
// does not share or mutate any file-offset state, so no locking is needed
// here.
type Reader struct {
	f *os.File
}

// OpenReader opens the Pod file at path for reading.
func OpenReader(path string) (*Reader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("pod: open reader: %w", err)
	}
	return &Reader{f: f}, nil
}

// Close closes the underlying file.
func (r *Reader) Close() error {
	if err := r.f.Close(); err != nil {
		return fmt.Errorf("pod: close reader: %w", err)
	}
	return nil
}

// ReadAt reads and decodes the single frame starting at byte offset, without
// verifying the CRC (see ReadAtVerified for that). It returns ErrShortFrame
// if the file does not contain enough bytes at offset to hold a complete
// frame per the header's declared lengths.
func (r *Reader) ReadAt(offset int64) (Record, error) {
	return readFrameAt(r.f, offset, false)
}

// ReadAtVerified is ReadAt plus a CRC-32C check against the frame's stored
// crc32c, returning ErrCRCMismatch if they disagree.
func (r *Reader) ReadAtVerified(offset int64) (Record, error) {
	return readFrameAt(r.f, offset, true)
}

// readFrameAt is the shared implementation behind ReadAt / ReadAtVerified: a
// single-frame random-access read, used when a caller only wants one known
// frame (e.g. engine.GetBead resolving one Bead by its indexed offset) and
// opening+reading the whole Pod file would be wasted work. It costs two
// ReadAt syscalls (fixed header, then payload) since the payload's length
// isn't known until the header is decoded. Scan (scanner.go), by contrast,
// reads a Pod file's entire byte range once up front and parses every frame
// from that in-memory buffer via decodeFrameFrom — sequential scans do not
// pay a per-frame syscall, since (unlike this function's single-frame case)
// they already need almost the whole file's bytes anyway.
func readFrameAt(f *os.File, offset int64, verifyCRC bool) (Record, error) {
	headerBuf := make([]byte, frameFixedSize)
	n, err := f.ReadAt(headerBuf, offset)
	if err != nil {
		if err == io.EOF && n == 0 {
			return Record{}, fmt.Errorf("%w: no data at offset %d", ErrShortFrame, offset)
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return Record{}, fmt.Errorf("%w: header truncated at offset %d (got %d of %d bytes)",
				ErrShortFrame, offset, n, frameFixedSize)
		}
		return Record{}, fmt.Errorf("pod: read frame header at %d: %w", offset, err)
	}

	h, err := decodeHeader(headerBuf)
	if err != nil {
		return Record{}, fmt.Errorf("pod: read frame at %d: %w", offset, err)
	}

	payloadLen := int64(h.CoreLen) + int64(h.MetaLen)
	payloadBuf := make([]byte, payloadLen)
	if payloadLen > 0 {
		pn, err := f.ReadAt(payloadBuf, offset+frameFixedSize)
		if err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return Record{}, fmt.Errorf("%w: payload truncated at offset %d (got %d of %d bytes)",
					ErrShortFrame, offset, pn, payloadLen)
			}
			return Record{}, fmt.Errorf("pod: read frame payload at %d: %w", offset, err)
		}
	}

	return decodeFrameBytes(offset, h, payloadBuf, verifyCRC)
}

// decodeFrameFrom parses one frame starting at byte offset within buf (an
// in-memory copy of an entire Pod file, or at least everything from offset
// onward) — Scan's per-frame step, avoiding the two ReadAt syscalls
// readFrameAt needs when reading directly from a file (see Scan's doc
// comment). Error semantics exactly mirror readFrameAt: ErrShortFrame if buf
// does not extend far enough past offset to hold a complete frame, wrapping
// decodeHeader's ErrBadMagic, and ErrCRCMismatch when verifyCRC is true and
// the check fails.
func decodeFrameFrom(buf []byte, offset int64, verifyCRC bool) (Record, error) {
	if offset < 0 || offset >= int64(len(buf)) {
		return Record{}, fmt.Errorf("%w: no data at offset %d", ErrShortFrame, offset)
	}
	remaining := buf[offset:]
	if int64(len(remaining)) < frameFixedSize {
		return Record{}, fmt.Errorf("%w: header truncated at offset %d (got %d of %d bytes)",
			ErrShortFrame, offset, len(remaining), frameFixedSize)
	}
	h, err := decodeHeader(remaining[:frameFixedSize])
	if err != nil {
		return Record{}, fmt.Errorf("pod: read frame at %d: %w", offset, err)
	}

	payloadLen := int64(h.CoreLen) + int64(h.MetaLen)
	payloadAvail := remaining[frameFixedSize:]
	if int64(len(payloadAvail)) < payloadLen {
		return Record{}, fmt.Errorf("%w: payload truncated at offset %d (got %d of %d bytes)",
			ErrShortFrame, offset, len(payloadAvail), payloadLen)
	}
	payloadBuf := payloadAvail[:payloadLen]

	return decodeFrameBytes(offset, h, payloadBuf, verifyCRC)
}

// decodeFrameBytes builds a Record from an already-decoded header h plus its
// payload bytes (core_bytes||meta_bytes, exactly h.CoreLen+h.MetaLen long),
// optionally checking the CRC-32C. It is the tail shared by readFrameAt (file
// ReadAt path) and decodeFrameFrom (Scan's in-memory path) once each has, by
// its own means, obtained the header and payload bytes.
func decodeFrameBytes(offset int64, h frameHeader, payloadBuf []byte, verifyCRC bool) (Record, error) {
	coreBytes := payloadBuf[:h.CoreLen]
	metaBytes := payloadBuf[h.CoreLen:]

	if verifyCRC {
		got := crcTarget(h.Flags, h.CoreLen, h.MetaLen, h.BeadID, coreBytes, metaBytes)
		if got != h.CRC32C {
			return Record{}, fmt.Errorf("%w: at offset %d: stored=%#08x computed=%#08x",
				ErrCRCMismatch, offset, h.CRC32C, got)
		}
	}

	meta, err := decodeMeta(metaBytes)
	if err != nil {
		return Record{}, fmt.Errorf("pod: read frame at %d: %w", offset, err)
	}

	payloadLen := int64(h.CoreLen) + int64(h.MetaLen)
	return Record{
		BeadID:       hex.EncodeToString(h.BeadID[:]),
		Codec:        h.Codec(),
		CoreBytes:    coreBytes,
		MetaBytes:    metaBytes,
		Meta:         meta,
		Offset:       offset,
		Length:       frameFixedSize + payloadLen,
		StoredCRC32C: h.CRC32C,
	}, nil
}
