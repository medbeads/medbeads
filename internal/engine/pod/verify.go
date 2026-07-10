package pod

import (
	"encoding/hex"
	"fmt"
)

// FrameResult is the verification outcome for one frame within a Pod.
type FrameResult struct {
	// Offset is the frame's byte offset within its Pod file.
	Offset int64
	// BeadID is the frame's claimed Bead ID (plain hex).
	BeadID string
	// OK is true iff the frame's CRC-32C matched and its decompressed
	// core_bytes hashes (sha256) to BeadID.
	OK bool
	// Err describes the failure when OK is false (ErrCRCMismatch,
	// ErrSelfVerifyMismatch, or a decompression error).
	Err error
}

// PodReport is the verification outcome for a single Pod file: R1.4's "全レ
// コードの CRC 検証 + core_bytes 解凍→再ハッシュ = bead_id 照合".
type PodReport struct {
	// Path is the Pod file that was verified.
	Path string
	// Frames holds one FrameResult per successfully-parsed frame (frames
	// that failed to parse at all — bad magic / short read — are not
	// represented here; see Truncated/TruncatedAt below for that case).
	Frames []FrameResult
	// Truncated is true if the scan stopped early due to a damaged/
	// incomplete trailing frame (tail-truncate condition), distinct from a
	// mid-file CRC or self-verify failure on an otherwise well-formed frame.
	Truncated bool
	// TruncatedAt is the byte offset where the damage was found, valid only
	// when Truncated is true. This is the same value Scan would report as
	// ValidUpto, i.e. where Truncate(path, TruncatedAt) would cut the file.
	TruncatedAt int64
	// TruncationErr is the underlying scan error when Truncated is true.
	TruncationErr error
}

// OK reports whether every frame in the report passed verification and the
// scan reached end-of-file without truncation.
func (r PodReport) OK() bool {
	if r.Truncated {
		return false
	}
	for _, f := range r.Frames {
		if !f.OK {
			return false
		}
	}
	return true
}

// FailedFrames returns the subset of r.Frames that failed verification.
func (r PodReport) FailedFrames() []FrameResult {
	var out []FrameResult
	for _, f := range r.Frames {
		if !f.OK {
			out = append(out, f)
		}
	}
	return out
}

// VerifyPod scans the Pod file at path with CRC verification enabled and
// checks, for every frame that passes the scan: (1) CRC-32C over the whole
// frame except the crc32c field itself
// (magic||flags||core_len||meta_len||bead_id||core_bytes||meta_bytes — see
// crcTarget in frame.go), and (2) self-verification —
// sha256(decompress(core_bytes)) == bead_id (specs/DESIGN_v3.md §3).
//
// Scanning with CRC verification on (rather than checking it only after the
// fact, per-Record) matters here: if core_len/meta_len themselves are
// corrupted, Scan reading with verifyCRC=false would mis-split the payload
// into a garbage core_bytes/meta_bytes pair before any check ever runs, and
// meta_bytes decode could fail on the garbage split — a different, weaker
// failure signal than the true cause (a CRC mismatch on the header/length
// fields). Checking CRC inline during the scan catches that corruption at
// its source, before mis-split payload bytes are ever interpreted.
//
// A frame that fails CRC (or a short/incomplete trailing frame, or a bad
// magic number) stops the scan at that offset, exactly like the
// tail-truncate policy for a crash-mid-append tail: frames read successfully
// *before* the damaged one are still reported in Frames (this package's
// core promise is that damage never discards already-verified data), while
// frames *after* it are unreachable — once a frame's on-disk length can no
// longer be trusted, this sequential/append-only format has no way to
// locate where the next frame begins. That is surfaced via
// Truncated/TruncatedAt rather than as a FrameResult, since there is no
// complete, locatable frame at that offset to report on.
func VerifyPod(path string) (PodReport, error) {
	scan, err := Scan(path, true)
	if err != nil {
		return PodReport{}, fmt.Errorf("pod: verify %s: %w", path, err)
	}

	report := PodReport{Path: path}
	for _, rec := range scan.Records {
		report.Frames = append(report.Frames, verifyRecord(rec))
	}
	if scan.Damaged {
		report.Truncated = true
		report.TruncatedAt = scan.ValidUpto
		report.TruncationErr = scan.DamageErr
	}
	return report, nil
}

// Report is the aggregate result of VerifyAll: one PodReport per Pod file
// found under a Store's pods/ directory.
type Report struct {
	// Pods holds one PodReport per Pod file, in Store.ListPodFiles order.
	Pods []PodReport
}

// OK reports whether every Pod in the report passed verification.
func (r Report) OK() bool {
	for _, p := range r.Pods {
		if !p.OK() {
			return false
		}
	}
	return true
}

// TotalFrames returns the number of frames checked across all Pods.
func (r Report) TotalFrames() int {
	n := 0
	for _, p := range r.Pods {
		n += len(p.Frames)
	}
	return n
}

// FailedPods returns the subset of r.Pods that did not pass verification
// (either a failed frame or a truncation condition).
func (r Report) FailedPods() []PodReport {
	var out []PodReport
	for _, p := range r.Pods {
		if !p.OK() {
			out = append(out, p)
		}
	}
	return out
}

// VerifyAll runs VerifyPod against every Pod file under store's pods/
// directory (specs/DESIGN_v3.md §3, R1.4's "全 Pod 走査"). It does not stop
// at the first failing Pod — every Pod is verified so a single run reports
// every problem found.
func VerifyAll(store *Store) (Report, error) {
	paths, err := store.ListPodFiles()
	if err != nil {
		return Report{}, fmt.Errorf("pod: verify all: %w", err)
	}

	var report Report
	for _, path := range paths {
		pr, err := VerifyPod(path)
		if err != nil {
			return Report{}, fmt.Errorf("pod: verify all: %w", err)
		}
		report.Pods = append(report.Pods, pr)
	}
	return report, nil
}

// verifyRecord checks one already-parsed frame's CRC-32C (recomputed over
// the exact on-disk bead_id/core_bytes/meta_bytes retained on rec) and its
// self-verification: sha256(decompress(core_bytes)) must equal bead_id (see
// Record.SelfVerify).
func verifyRecord(rec Record) FrameResult {
	fr := FrameResult{Offset: rec.Offset, BeadID: rec.BeadID}

	var idBytes [idFieldSize]byte
	decoded, err := hex.DecodeString(rec.BeadID)
	if err != nil || len(decoded) != idFieldSize {
		fr.Err = fmt.Errorf("pod: verify: malformed bead_id %q", rec.BeadID)
		return fr
	}
	copy(idBytes[:], decoded)

	gotCRC := crcTarget(uint8(rec.Codec), uint32(len(rec.CoreBytes)), uint32(len(rec.MetaBytes)), idBytes, rec.CoreBytes, rec.MetaBytes)
	if gotCRC != rec.StoredCRC32C {
		fr.Err = fmt.Errorf("%w: at offset %d: stored=%#08x computed=%#08x",
			ErrCRCMismatch, rec.Offset, rec.StoredCRC32C, gotCRC)
		return fr
	}

	if err := rec.SelfVerify(); err != nil {
		fr.Err = fmt.Errorf("%w: at offset %d", err, rec.Offset)
		return fr
	}

	fr.OK = true
	return fr
}
