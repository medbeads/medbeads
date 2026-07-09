package pod

import (
	"errors"
	"fmt"
	"os"
)

// ScanResult is the outcome of a full sequential scan of a Pod file
// (specs/DESIGN_v3.md §3: "(b) Pod 全体の順次スキャン" and the tail-truncate
// recovery strategy). Records holds every frame that decoded cleanly, in
// file order, up to the first damage encountered. If the file has no
// damage, ValidUpto equals the file's total size and Records covers the
// entire file.
type ScanResult struct {
	// Records is every frame read successfully, in ascending offset order.
	Records []Record
	// ValidUpto is the byte offset immediately after the last fully valid
	// frame — i.e. where a Truncate call would cut the file to discard only
	// damaged/incomplete trailing bytes.
	ValidUpto int64
	// Damaged is true if the scan stopped before reaching end-of-file
	// because it hit a short/incomplete frame or a bad magic number. Damaged
	// alone does not distinguish CRC corruption mid-file from a normal
	// crash-mid-append tail (see DamageErr).
	Damaged bool
	// DamageErr is the error that stopped the scan (ErrShortFrame,
	// ErrBadMagic, ErrCRCMismatch when verifyCRC is true, or ErrMetaDecode),
	// or nil if the scan reached EOF cleanly. It is nil whenever Damaged is
	// false.
	DamageErr error
}

// Scan reads every frame in the Pod file at path sequentially from the
// start, stopping at the first sign of damage rather than failing outright
// (specs/DESIGN_v3.md §3 tail-truncate policy: "そこまでで正常終了 + 破損位置を返す").
// It does not verify CRCs by default — pass verifyCRC=true to also stop at
// the first CRC mismatch (treating it the same as a short frame: a form of
// damage that truncation would need to address, even though a CRC mismatch
// in the middle of an otherwise well-formed frame is a different failure
// mode than a truncated tail — see doc.go).
func Scan(path string, verifyCRC bool) (ScanResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return ScanResult{}, fmt.Errorf("pod: scan: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return ScanResult{}, fmt.Errorf("pod: scan: stat: %w", err)
	}
	size := info.Size()

	var result ScanResult
	var offset int64
	for offset < size {
		rec, err := readFrameAt(f, offset, verifyCRC)
		if err != nil {
			if errors.Is(err, ErrShortFrame) || errors.Is(err, ErrBadMagic) ||
				errors.Is(err, ErrCRCMismatch) || errors.Is(err, ErrMetaDecode) {
				// All four are frame-level damage, not scan-level failure:
				// stop here and report what was read so far, per the
				// tail-truncate recovery policy (specs/DESIGN_v3.md §3).
				// ErrMetaDecode reaching here (rather than being caught
				// earlier as ErrCRCMismatch) should be rare now that
				// crcTarget covers core_len/meta_len/flags, but a corrupt
				// meta_bytes payload must never abort the whole scan and
				// discard already-collected Records — see errors.go.
				result.Damaged = true
				result.DamageErr = err
				break
			}
			return ScanResult{}, fmt.Errorf("pod: scan %s at offset %d: %w", path, offset, err)
		}
		result.Records = append(result.Records, rec)
		offset += rec.Length
	}
	result.ValidUpto = offset
	return result, nil
}

// Truncate cuts the Pod file at path to exactly validUpto bytes, discarding
// any damaged/incomplete trailing data so that further Append calls resume
// cleanly. Callers should pass ScanResult.ValidUpto from a prior Scan.
// Truncate is destructive and is never called automatically by Scan or
// Verify — per the task's recovery policy, execution is the caller's
// decision.
func Truncate(path string, validUpto int64) error {
	if validUpto < 0 {
		return fmt.Errorf("pod: truncate %s: negative offset %d", path, validUpto)
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("pod: truncate %s: %w", path, err)
	}
	defer f.Close()

	if err := f.Truncate(validUpto); err != nil {
		return fmt.Errorf("pod: truncate %s to %d: %w", path, validUpto, err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("pod: truncate %s: fsync: %w", path, err)
	}
	return nil
}
