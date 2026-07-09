package pod

import (
	"encoding/hex"
	"fmt"
	"os"
	"sync"

	"github.com/medbeads/medbeads/internal/engine/bead"
)

// Writer appends frames to a single Pod file. Per specs/DESIGN_v3.md §3
// ("並行性: Pod ごとに単一ライター（mutex）"), a Writer serializes all Append
// calls against the one Pod file it owns with a mutex; callers needing to
// write to multiple Pods concurrently should use one Writer per Pod (a
// process-wide registry of per-path Writers is the caller's job — package
// pod only guarantees a single Writer instance is itself concurrency-safe).
type Writer struct {
	mu   sync.Mutex
	path string
	f    *os.File
}

// OpenWriter opens (creating if necessary) the Pod file at path for
// appending. The parent directory must already exist (see
// Store.EnsurePatientPodDir / Store.EnsurePodsDir) — OpenWriter does not
// create directories, keeping directory-creation policy in one place.
func OpenWriter(path string) (*Writer, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("pod: open writer: %w", err)
	}
	return &Writer{path: path, f: f}, nil
}

// Path returns the file path this Writer appends to.
func (w *Writer) Path() string {
	return w.path
}

// Close closes the underlying file. It does not fsync; callers that need a
// final durability guarantee should ensure the last Append has already
// synced (Append always fsyncs itself — see below).
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.f.Close(); err != nil {
		return fmt.Errorf("pod: close writer %s: %w", w.path, err)
	}
	return nil
}

// AppendResult reports where a frame landed, for the caller (typically the
// index writer) to record.
type AppendResult struct {
	Offset int64
	Length int64
}

// Append encodes b as a frame using codec and appends it to the Pod file,
// fsyncing before returning (specs/DESIGN_v3.md §3: "Pod append + fsync ->
// SQLite インデックス"). b must already have a valid ID (see bead.WithID) —
// Append does not compute IDs, it stores them.
//
// meta.PatientRoot is caller-supplied (pre-resolved from the index, per
// specs/DESIGN_v3.md §3) rather than derived here, since Writer has no
// access to the index and must not guess.
func (w *Writer) Append(b bead.Bead, codec Codec, meta Meta) (AppendResult, error) {
	if b.ID == "" {
		return AppendResult{}, fmt.Errorf("pod: append: bead has no ID (call bead.WithID first)")
	}
	idBytes, err := hex.DecodeString(b.ID)
	if err != nil || len(idBytes) != idFieldSize {
		return AppendResult{}, fmt.Errorf("pod: append: invalid bead ID %q: %w", b.ID, err)
	}

	canonical, err := bead.Canonicalize(b)
	if err != nil {
		return AppendResult{}, fmt.Errorf("pod: append: canonicalize: %w", err)
	}
	coreBytes, err := compress(codec, canonical)
	if err != nil {
		return AppendResult{}, fmt.Errorf("pod: append: compress: %w", err)
	}
	metaBytes, err := meta.encode()
	if err != nil {
		return AppendResult{}, fmt.Errorf("pod: append: %w", err)
	}

	var beadIDArr [idFieldSize]byte
	copy(beadIDArr[:], idBytes)

	frame, err := encodeFrame(beadIDArr, uint8(codec), coreBytes, metaBytes)
	if err != nil {
		return AppendResult{}, fmt.Errorf("pod: append: %w", err)
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	// os.O_APPEND guarantees the kernel positions each Write at the current
	// end of file atomically, but the *offset this frame will start at* is
	// only knowable to us by reading the pre-write file size. It is safe to
	// treat that size as this frame's offset because w.mu serializes every
	// Append this process makes against this file (the single-writer-per-Pod
	// invariant from specs/DESIGN_v3.md §3) — no concurrent writer from
	// within this process can grow the file between Stat and Write.
	info, err := w.f.Stat()
	if err != nil {
		return AppendResult{}, fmt.Errorf("pod: append: stat: %w", err)
	}
	offset := info.Size()

	n, err := w.f.Write(frame)
	if err != nil {
		return AppendResult{}, fmt.Errorf("pod: append: write: %w", err)
	}
	if n != len(frame) {
		return AppendResult{}, fmt.Errorf("pod: append: short write: wrote %d of %d bytes", n, len(frame))
	}
	if err := w.f.Sync(); err != nil {
		return AppendResult{}, fmt.Errorf("pod: append: fsync: %w", err)
	}

	return AppendResult{Offset: offset, Length: int64(len(frame))}, nil
}

// encodeFrame assembles the full on-disk byte layout of one frame: fixed
// header (magic, flags, core_len, meta_len, crc32c, bead_id) followed by
// core_bytes then meta_bytes.
func encodeFrame(beadID [idFieldSize]byte, flags uint8, coreBytes, metaBytes []byte) ([]byte, error) {
	if len(coreBytes) > 1<<32-1 {
		return nil, fmt.Errorf("pod: encode frame: core_bytes too large: %d bytes", len(coreBytes))
	}
	if len(metaBytes) > 1<<32-1 {
		return nil, fmt.Errorf("pod: encode frame: meta_bytes too large: %d bytes", len(metaBytes))
	}

	crc := crcTarget(flags, uint32(len(coreBytes)), uint32(len(metaBytes)), beadID, coreBytes, metaBytes)

	total := frameFixedSize + len(coreBytes) + len(metaBytes)
	buf := make([]byte, total)
	encodeHeader(buf, flags, uint32(len(coreBytes)), uint32(len(metaBytes)), crc, beadID)
	copy(buf[frameFixedSize:], coreBytes)
	copy(buf[frameFixedSize+len(coreBytes):], metaBytes)
	return buf, nil
}
