package pod

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"

	"github.com/medbeads/medbeads/internal/engine/bead"
)

// Frame format (specs/DESIGN_v3.md §3), all multi-byte integers little-endian:
//
//	magic     uint16   FrameMagic
//	flags     uint8    codec ID (low bits); see Codec* constants
//	core_len  uint32   len(core_bytes)
//	meta_len  uint32   len(meta_bytes)
//	crc32c    uint32   CRC-32C (Castagnoli) over magic || flags || core_len || meta_len ||
//	                   bead_id || core_bytes || meta_bytes — i.e. every frame byte except the
//	                   crc32c field itself (see crcTarget)
//	bead_id   [32]byte sha256.Size raw bytes (not hex)
//	core_bytes  [core_len]byte  compressed JCS-canonical Bead JSON (self-verifying: see doc.go)
//	meta_bytes  [meta_len]byte  frame metadata JSON (NOT bead-hash target, but IS covered by
//	                            the CRC below — see crcTarget)
//
// headerSize is the fixed-size portion written before bead_id.
const headerSize = 2 + 1 + 4 + 4 + 4 // magic + flags + core_len + meta_len + crc32c

// idFieldSize is the size of the bead_id field: the raw (non-hex) SHA-256
// digest, matching bead.IDLen.
const idFieldSize = bead.IDLen

// FrameMagic identifies a valid Pod frame header. It intentionally has no
// separate "version" field: format changes that are not backward compatible
// must bump FrameMagic itself, per the "don't grow the format" risk note in
// specs/DESIGN_v3.md §10.
const FrameMagic uint16 = 0xB6AD

// Codec identifies how core_bytes is compressed. It occupies the low bits of
// the frame's flags byte.
type Codec uint8

const (
	// CodecRaw stores core_bytes uncompressed.
	CodecRaw Codec = 0
	// CodecZstd stores core_bytes zstd-compressed without a shared dictionary.
	CodecZstd Codec = 1
	// CodecZstdDict stores core_bytes zstd-compressed with the shared
	// dictionary (dict/zstd-v1.dict, specs/DESIGN_v3.md §3). The dictionary
	// loader/ID plumbing exists from R1 on, but no writer selects this codec
	// until the dictionary is trained during migration (R7.2); see doc.go.
	CodecZstdDict Codec = 2
)

// flagsCodecMask covers the bits of the flags byte currently assigned to the
// codec ID. All other bits are reserved and must be zero on write; a reader
// rejects frames with unknown reserved bits set (forward-compatibility trip
// wire rather than silently misinterpreting a future flag).
const flagsCodecMask = 0x0F

func (c Codec) valid() bool {
	switch c {
	case CodecRaw, CodecZstd, CodecZstdDict:
		return true
	default:
		return false
	}
}

func (c Codec) String() string {
	switch c {
	case CodecRaw:
		return "raw"
	case CodecZstd:
		return "zstd"
	case CodecZstdDict:
		return "zstd-dict"
	default:
		return fmt.Sprintf("codec(%d)", uint8(c))
	}
}

// crcTable is the CRC-32C (Castagnoli) table used for frame integrity, per
// specs/DESIGN_v3.md §3.
var crcTable = crc32.MakeTable(crc32.Castagnoli)

// frameHeader is the fixed-size, decoded form of a frame's header fields
// (everything before bead_id would also be included, but bead_id is decoded
// alongside it since both are fixed-size and read together).
type frameHeader struct {
	Flags   uint8
	CoreLen uint32
	MetaLen uint32
	CRC32C  uint32
	BeadID  [idFieldSize]byte
}

// Codec extracts the codec ID from the header's flags byte.
func (h frameHeader) Codec() Codec {
	return Codec(h.Flags & flagsCodecMask)
}

// frameFixedSize is the total size of magic + flags + core_len + meta_len +
// crc32c + bead_id, i.e. everything before the variable-length core_bytes /
// meta_bytes payload.
const frameFixedSize = headerSize + idFieldSize

// crcTarget computes the CRC-32C over every frame byte except the crc32c
// field itself: magic || flags || core_len || meta_len || bead_id ||
// core_bytes || meta_bytes.
//
// Earlier revisions of this package covered only bead_id||core_bytes||
// meta_bytes, omitting flags/core_len/meta_len (and magic). That left a real
// gap: a "boundary shift" corruption — e.g. core_len decremented by 1 and
// meta_len incremented by 1 — changes where core_bytes ends and meta_bytes
// begins, but leaves the concatenation bead_id||core_bytes||meta_bytes
// byte-for-byte identical, so the old CRC could not detect it at all (found
// by data-reviewer via direct measurement: stored/original/shifted CRCs all
// equal). Covering the length fields (and flags, and magic, for the same
// "cover everything but the CRC's own field" reasoning) closes that gap:
// changing core_len/meta_len/flags/magic now changes the CRC input directly,
// regardless of what payload bytes follow.
//
// This is still deliberately NOT the same byte set as bead's hash-target
// fields (core_bytes is the compressed form of the JCS canonical Bead, and
// meta_bytes/flags/lengths are Pod-local framing that Bead itself knows
// nothing about) — the CRC's job is "did this frame survive on disk
// unmodified", not "is this a valid Bead". Self-verification of core_bytes
// against bead_id is a separate, stronger check (see Verify in verify.go).
func crcTarget(flags uint8, coreLen, metaLen uint32, beadID [idFieldSize]byte, coreBytes, metaBytes []byte) uint32 {
	h := crc32.New(crcTable)
	var lenBuf [2 + 1 + 4 + 4]byte // magic + flags + core_len + meta_len
	binary.LittleEndian.PutUint16(lenBuf[0:2], FrameMagic)
	lenBuf[2] = flags
	binary.LittleEndian.PutUint32(lenBuf[3:7], coreLen)
	binary.LittleEndian.PutUint32(lenBuf[7:11], metaLen)
	h.Write(lenBuf[:])
	h.Write(beadID[:])
	h.Write(coreBytes)
	h.Write(metaBytes)
	return h.Sum32()
}

// encodeHeader writes the fixed-size frame header (magic through bead_id) to
// buf, which must be at least frameFixedSize bytes.
func encodeHeader(buf []byte, flags uint8, coreLen, metaLen uint32, crc uint32, beadID [idFieldSize]byte) {
	binary.LittleEndian.PutUint16(buf[0:2], FrameMagic)
	buf[2] = flags
	binary.LittleEndian.PutUint32(buf[3:7], coreLen)
	binary.LittleEndian.PutUint32(buf[7:11], metaLen)
	binary.LittleEndian.PutUint32(buf[11:15], crc)
	copy(buf[15:15+idFieldSize], beadID[:])
}

// decodeHeader parses the fixed-size frame header (magic through bead_id)
// from buf, which must be exactly frameFixedSize bytes. It validates magic
// and reserved flag bits but does not validate CRC or lengths against actual
// payload data (callers do that once the payload is read).
func decodeHeader(buf []byte) (frameHeader, error) {
	if len(buf) != frameFixedSize {
		return frameHeader{}, fmt.Errorf("pod: decode header: want %d bytes, got %d", frameFixedSize, len(buf))
	}
	magic := binary.LittleEndian.Uint16(buf[0:2])
	if magic != FrameMagic {
		return frameHeader{}, fmt.Errorf("%w: got %#04x, want %#04x", ErrBadMagic, magic, FrameMagic)
	}
	flags := buf[2]
	if flags&^uint8(flagsCodecMask) != 0 {
		return frameHeader{}, fmt.Errorf("pod: decode header: reserved flag bits set: %#02x", flags)
	}
	codec := Codec(flags & flagsCodecMask)
	if !codec.valid() {
		return frameHeader{}, fmt.Errorf("%w: %d", ErrUnknownCodec, uint8(codec))
	}

	var h frameHeader
	h.Flags = flags
	h.CoreLen = binary.LittleEndian.Uint32(buf[3:7])
	h.MetaLen = binary.LittleEndian.Uint32(buf[7:11])
	h.CRC32C = binary.LittleEndian.Uint32(buf[11:15])
	copy(h.BeadID[:], buf[15:15+idFieldSize])
	return h, nil
}
