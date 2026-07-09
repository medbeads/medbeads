package pod

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/medbeads/medbeads/internal/engine/bead"
)

// testBead returns a small, deterministic Bead with a real content-hash ID,
// suitable for round-trip / codec tests.
func testBead(t *testing.T, note string) bead.Bead {
	t.Helper()
	b := bead.Bead{
		Type:      "observation",
		Timestamp: "2026-03-01T10:00:00Z",
		Author:    "did:medbeads:doctor:12345",
		Parents:   []string{"a1"},
		Antigens:  []string{"organ:renal"},
		Content: map[string]any{
			"note": note,
		},
	}
	withID, err := bead.WithID(b)
	if err != nil {
		t.Fatalf("bead.WithID: %v", err)
	}
	return withID
}

func openWriterT(t *testing.T, path string) *Writer {
	t.Helper()
	w, err := OpenWriter(path)
	if err != nil {
		t.Fatalf("OpenWriter(%s): %v", path, err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return w
}

// --- round-trip: Append -> ReadAt / Scan, for all three codec IDs --------

func TestRoundTrip_AppendReadAt(t *testing.T) {
	for _, codec := range []Codec{CodecRaw, CodecZstd} {
		t.Run(codec.String(), func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "patient.pod")
			w := openWriterT(t, path)

			b := testBead(t, "round-trip "+codec.String())
			meta := NewMeta("deadbeef")

			res, err := w.Append(b, codec, meta)
			if err != nil {
				t.Fatalf("Append: %v", err)
			}

			r, err := OpenReader(path)
			if err != nil {
				t.Fatalf("OpenReader: %v", err)
			}
			defer r.Close()

			rec, err := r.ReadAtVerified(res.Offset)
			if err != nil {
				t.Fatalf("ReadAtVerified: %v", err)
			}
			if rec.BeadID != b.ID {
				t.Errorf("rec.BeadID = %s, want %s", rec.BeadID, b.ID)
			}
			if rec.Length != res.Length {
				t.Errorf("rec.Length = %d, want %d", rec.Length, res.Length)
			}
			if rec.Meta.PatientRoot != "deadbeef" {
				t.Errorf("rec.Meta.PatientRoot = %q, want %q", rec.Meta.PatientRoot, "deadbeef")
			}

			plain, err := rec.Decompress()
			if err != nil {
				t.Fatalf("Decompress: %v", err)
			}
			wantCanonical, err := bead.Canonicalize(b)
			if err != nil {
				t.Fatalf("Canonicalize: %v", err)
			}
			if string(plain) != string(wantCanonical) {
				t.Errorf("decompressed core_bytes mismatch:\n got  = %s\n want = %s", plain, wantCanonical)
			}
		})
	}
}

func TestRoundTrip_AppendThenScan(t *testing.T) {
	for _, codec := range []Codec{CodecRaw, CodecZstd} {
		t.Run(codec.String(), func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "patient.pod")
			w := openWriterT(t, path)

			var beads []bead.Bead
			for i := 0; i < 5; i++ {
				b := testBead(t, codec.String()+"-"+string(rune('a'+i)))
				if _, err := w.Append(b, codec, NewMeta("root1")); err != nil {
					t.Fatalf("Append: %v", err)
				}
				beads = append(beads, b)
			}

			result, err := Scan(path, true)
			if err != nil {
				t.Fatalf("Scan: %v", err)
			}
			if result.Damaged {
				t.Fatalf("Scan reported damage on a clean file: %v", result.DamageErr)
			}
			if len(result.Records) != len(beads) {
				t.Fatalf("Scan returned %d records, want %d", len(result.Records), len(beads))
			}
			for i, rec := range result.Records {
				if rec.BeadID != beads[i].ID {
					t.Errorf("record %d BeadID = %s, want %s", i, rec.BeadID, beads[i].ID)
				}
			}

			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("Stat: %v", err)
			}
			if result.ValidUpto != info.Size() {
				t.Errorf("ValidUpto = %d, want file size %d", result.ValidUpto, info.Size())
			}
		})
	}
}

// CodecZstdDict has no writer yet (see doc.go) — compress() must report that
// clearly rather than silently falling back to another codec.
func TestCodecZstdDict_CompressNotYetSupported(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "patient.pod")
	w := openWriterT(t, path)

	b := testBead(t, "zstd-dict")
	_, err := w.Append(b, CodecZstdDict, NewMeta("root1"))
	if err == nil {
		t.Fatal("Append with CodecZstdDict = nil error, want error (no dictionary loaded yet)")
	}
	if !errors.Is(err, ErrUnknownCodec) {
		t.Errorf("Append with CodecZstdDict error = %v, want wrapping ErrUnknownCodec", err)
	}
}

// --- self-verification: decompress -> sha256 == bead_id ------------------

func TestSelfVerification_DecompressHashMatchesBeadID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "patient.pod")
	w := openWriterT(t, path)

	b := testBead(t, "self-verify")
	res, err := w.Append(b, CodecZstd, NewMeta("root1"))
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	r, err := OpenReader(path)
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer r.Close()

	rec, err := r.ReadAtVerified(res.Offset)
	if err != nil {
		t.Fatalf("ReadAtVerified: %v", err)
	}
	plain, err := rec.Decompress()
	if err != nil {
		t.Fatalf("Decompress: %v", err)
	}
	sum := sha256.Sum256(plain)
	got := hex.EncodeToString(sum[:])
	if got != b.ID {
		t.Errorf("sha256(decompressed core_bytes) = %s, want bead_id %s", got, b.ID)
	}

	report, err := VerifyPod(path)
	if err != nil {
		t.Fatalf("VerifyPod: %v", err)
	}
	if !report.OK() {
		t.Errorf("VerifyPod(untampered) report not OK: %+v", report.FailedFrames())
	}
}

// --- tamper detection: flip one byte of core_bytes on disk ---------------

// TestVerifyPod_DetectsTamperedCoreBytes tampers core_bytes in the *second*
// of two frames. VerifyPod now scans with CRC verification enabled (see
// VerifyPod's doc comment), so a corrupted frame is caught by Scan itself —
// surfaced as Truncated/TruncationErr, not as a FrameResult — and the scan
// stops there. The first (untouched) frame must still be reported, proving
// damage does not discard already-read valid frames.
func TestVerifyPod_DetectsTamperedCoreBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "patient.pod")
	w := openWriterT(t, path)

	survivor := testBead(t, "tamper-survivor")
	res1, err := w.Append(survivor, CodecRaw, NewMeta("root1"))
	if err != nil {
		t.Fatalf("Append (survivor): %v", err)
	}
	victim := testBead(t, "tamper-me") // raw codec: core_bytes == canonical JSON, easy to flip meaningfully
	res2, err := w.Append(victim, CodecRaw, NewMeta("root1"))
	if err != nil {
		t.Fatalf("Append (victim): %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Flip one byte inside the second frame's core_bytes (right after its
	// fixed header + bead_id).
	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("open for tamper: %v", err)
	}
	tamperOffset := res2.Offset + frameFixedSize // first byte of core_bytes
	buf := make([]byte, 1)
	if _, err := f.ReadAt(buf, tamperOffset); err != nil {
		t.Fatalf("read byte to tamper: %v", err)
	}
	buf[0] ^= 0xFF
	if _, err := f.WriteAt(buf, tamperOffset); err != nil {
		t.Fatalf("write tampered byte: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close after tamper: %v", err)
	}

	report, err := VerifyPod(path)
	if err != nil {
		t.Fatalf("VerifyPod: %v", err)
	}
	if report.OK() {
		t.Fatal("VerifyPod(tampered) report OK, want failure")
	}

	if len(report.Frames) != 1 {
		t.Fatalf("VerifyPod(tampered) checked %d frames, want 1 (the frame before the damage must still be reported)", len(report.Frames))
	}
	if !report.Frames[0].OK {
		t.Errorf("frame 1 (untouched) reported failure, want OK: %v", report.Frames[0].Err)
	}
	if report.Frames[0].Offset != res1.Offset {
		t.Errorf("frame 1 offset = %d, want %d", report.Frames[0].Offset, res1.Offset)
	}

	if !report.Truncated {
		t.Fatal("VerifyPod(tampered) reported Truncated=false, want true")
	}
	if report.TruncatedAt != res2.Offset {
		t.Errorf("TruncatedAt = %d, want %d (start of the tampered frame)", report.TruncatedAt, res2.Offset)
	}
	if !errors.Is(report.TruncationErr, ErrCRCMismatch) {
		t.Errorf("TruncationErr = %v, want ErrCRCMismatch (flipping core_bytes must break the CRC)", report.TruncationErr)
	}
}

// TestVerifyPod_DetectsBoundaryShiftCorruption exercises the fix for the
// data-reviewer finding: a "boundary shift" — core_len decremented by 1 and
// meta_len incremented by 1, with every on-disk byte otherwise untouched —
// leaves the concatenation bead_id||core_bytes||meta_bytes byte-for-byte
// identical, so a CRC that only covered that concatenation could not detect
// it (reviewer measured stored==original==shifted CRC). crcTarget now also
// covers flags/core_len/meta_len, so this must be caught as ErrCRCMismatch —
// and it must be caught as exactly that, via Scan's CRC check, rather than
// surfacing as a meta_bytes JSON-decode failure on a mis-split payload.
//
// The Pod has two frames; only the second is corrupted. The first frame is
// read successfully *before* the scan reaches the damage: VerifyPod must
// still report it (this is the other half of the reviewer's finding — Scan
// must not discard already-read, valid frames just because damage follows
// them). The second frame's length fields can no longer be trusted once
// corrupted, so — like any other tail-truncate-shaped damage in this
// sequential/append-only format — it is reported via Truncated/TruncatedAt,
// not as a FrameResult, since there is no reliably-locatable frame left to
// report on beyond the point of damage.
func TestVerifyPod_DetectsBoundaryShiftCorruption(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "patient.pod")
	w := openWriterT(t, path)

	survivor := testBead(t, "boundary-shift-survivor")
	res1, err := w.Append(survivor, CodecRaw, NewMeta("root1"))
	if err != nil {
		t.Fatalf("Append (survivor): %v", err)
	}
	victim := testBead(t, "boundary-shift-victim")
	res2, err := w.Append(victim, CodecRaw, NewMeta("root1"))
	if err != nil {
		t.Fatalf("Append (victim): %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("open for tamper: %v", err)
	}

	// Header layout (frame.go): magic(2) flags(1) core_len(4) meta_len(4)
	// crc32c(4) bead_id(32). core_len is buf[3:7], meta_len is buf[7:11].
	coreLenOff := res2.Offset + 3
	metaLenOff := res2.Offset + 7

	lenBuf := make([]byte, 4)
	if _, err := f.ReadAt(lenBuf, coreLenOff); err != nil {
		t.Fatalf("read core_len: %v", err)
	}
	coreLen := binary.LittleEndian.Uint32(lenBuf)
	if coreLen == 0 {
		t.Fatal("test precondition failed: core_len is 0, boundary shift would be a no-op")
	}

	newCoreLen := make([]byte, 4)
	binary.LittleEndian.PutUint32(newCoreLen, coreLen-1)
	if _, err := f.WriteAt(newCoreLen, coreLenOff); err != nil {
		t.Fatalf("write shifted core_len: %v", err)
	}

	if _, err := f.ReadAt(lenBuf, metaLenOff); err != nil {
		t.Fatalf("read meta_len: %v", err)
	}
	metaLen := binary.LittleEndian.Uint32(lenBuf)
	newMetaLen := make([]byte, 4)
	binary.LittleEndian.PutUint32(newMetaLen, metaLen+1)
	if _, err := f.WriteAt(newMetaLen, metaLenOff); err != nil {
		t.Fatalf("write shifted meta_len: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close after tamper: %v", err)
	}

	report, err := VerifyPod(path)
	if err != nil {
		t.Fatalf("VerifyPod: %v", err)
	}
	if report.OK() {
		t.Fatal("VerifyPod(boundary-shifted) report OK, want failure")
	}

	// The first frame must have survived being read (this is the reviewer's
	// "discarded valid frames" bug: it must NOT be silently dropped just
	// because damage follows it in the file).
	if len(report.Frames) != 1 {
		t.Fatalf("VerifyPod(boundary-shifted) checked %d frames, want 1 (the frame before the damage must still be reported)", len(report.Frames))
	}
	if !report.Frames[0].OK {
		t.Errorf("frame 1 (untouched, precedes the damage) reported failure, want OK: %v", report.Frames[0].Err)
	}
	if report.Frames[0].Offset != res1.Offset {
		t.Errorf("frame 1 offset = %d, want %d", report.Frames[0].Offset, res1.Offset)
	}
	if report.Frames[0].BeadID != survivor.ID {
		t.Errorf("frame 1 BeadID = %s, want %s", report.Frames[0].BeadID, survivor.ID)
	}

	// The second (boundary-shifted) frame must be caught via the CRC check
	// during Scan — reported as damage, not as a meta-decode failure or a
	// silently-passing frame.
	if !report.Truncated {
		t.Fatal("VerifyPod(boundary-shifted) reported Truncated=false, want true (corrupted length fields make the rest of the file unlocatable)")
	}
	if report.TruncatedAt != res2.Offset {
		t.Errorf("TruncatedAt = %d, want %d (start of the corrupted frame)", report.TruncatedAt, res2.Offset)
	}
	if !errors.Is(report.TruncationErr, ErrCRCMismatch) {
		t.Errorf("TruncationErr = %v, want ErrCRCMismatch (boundary shift must be caught as a CRC failure, not a meta-decode failure)", report.TruncationErr)
	}
}

// TestVerifyPod_DetectsFlagsCorruption confirms flags (the codec byte) is
// covered by the CRC: flipping it alone, with core_bytes/meta_bytes/lengths
// untouched, must be caught as ErrCRCMismatch. As with the other corruption
// tests above, VerifyPod's CRC-verified scan catches this during Scan
// itself (Truncated/TruncationErr), and a frame appended before the
// tampered one must still survive in Frames.
func TestVerifyPod_DetectsFlagsCorruption(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "patient.pod")
	w := openWriterT(t, path)

	survivor := testBead(t, "flags-survivor")
	res1, err := w.Append(survivor, CodecZstd, NewMeta("root1"))
	if err != nil {
		t.Fatalf("Append (survivor): %v", err)
	}
	victim := testBead(t, "flags-tamper")
	res2, err := w.Append(victim, CodecZstd, NewMeta("root1"))
	if err != nil {
		t.Fatalf("Append (victim): %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("open for tamper: %v", err)
	}
	// flags is the byte immediately after the 2-byte magic (frame.go layout).
	flagsOff := res2.Offset + 2
	buf := make([]byte, 1)
	if _, err := f.ReadAt(buf, flagsOff); err != nil {
		t.Fatalf("read flags: %v", err)
	}
	original := buf[0]
	// Flip to a different *valid* codec ID (CodecZstd -> CodecRaw) so this
	// exercises "flags corruption that still decodes as a plausible header"
	// rather than accidentally tripping the reserved-bits check in
	// decodeHeader, which would be a different (already-covered) failure
	// mode.
	buf[0] = uint8(CodecRaw)
	if buf[0] == original {
		t.Fatal("test precondition failed: flipped flags equals original")
	}
	if _, err := f.WriteAt(buf, flagsOff); err != nil {
		t.Fatalf("write tampered flags: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close after tamper: %v", err)
	}

	report, err := VerifyPod(path)
	if err != nil {
		t.Fatalf("VerifyPod: %v", err)
	}
	if report.OK() {
		t.Fatal("VerifyPod(flags-tampered) report OK, want failure")
	}

	if len(report.Frames) != 1 {
		t.Fatalf("VerifyPod(flags-tampered) checked %d frames, want 1 (the frame before the damage must still be reported)", len(report.Frames))
	}
	if !report.Frames[0].OK {
		t.Errorf("frame 1 (untouched) reported failure, want OK: %v", report.Frames[0].Err)
	}
	if report.Frames[0].Offset != res1.Offset {
		t.Errorf("frame 1 offset = %d, want %d", report.Frames[0].Offset, res1.Offset)
	}

	if !report.Truncated {
		t.Fatal("VerifyPod(flags-tampered) reported Truncated=false, want true")
	}
	if report.TruncatedAt != res2.Offset {
		t.Errorf("TruncatedAt = %d, want %d (start of the tampered frame)", report.TruncatedAt, res2.Offset)
	}
	if !errors.Is(report.TruncationErr, ErrCRCMismatch) {
		t.Errorf("TruncationErr = %v, want ErrCRCMismatch", report.TruncationErr)
	}
}

// --- tail-truncate recovery -----------------------------------------------

func TestScan_TailTruncateRecovery(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "patient.pod")
	w := openWriterT(t, path)

	var lastGoodEnd int64
	for i := 0; i < 3; i++ {
		b := testBead(t, "tail-"+string(rune('a'+i)))
		res, err := w.Append(b, CodecZstd, NewMeta("root1"))
		if err != nil {
			t.Fatalf("Append: %v", err)
		}
		lastGoodEnd = res.Offset + res.Length
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Simulate a crash mid-append: append a 4th frame, then chop off its
	// last few bytes (leaving a plausible-looking but incomplete header).
	w2 := openWriterT(t, path)
	partial := testBead(t, "tail-partial")
	res, err := w2.Append(partial, CodecZstd, NewMeta("root1"))
	if err != nil {
		t.Fatalf("Append (to be truncated): %v", err)
	}
	if err := w2.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	fullSize := res.Offset + res.Length
	chopTo := fullSize - 5 // cut 5 bytes off the tail: incomplete frame
	if err := os.Truncate(path, chopTo); err != nil {
		t.Fatalf("os.Truncate: %v", err)
	}

	result, err := Scan(path, true)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if !result.Damaged {
		t.Fatal("Scan on truncated file reported no damage, want Damaged=true")
	}
	if !errors.Is(result.DamageErr, ErrShortFrame) {
		t.Errorf("DamageErr = %v, want ErrShortFrame", result.DamageErr)
	}
	if len(result.Records) != 3 {
		t.Fatalf("Scan returned %d valid records, want 3", len(result.Records))
	}
	if result.ValidUpto != lastGoodEnd {
		t.Errorf("ValidUpto = %d, want %d (end of 3rd good frame)", result.ValidUpto, lastGoodEnd)
	}

	// Truncate to ValidUpto, then confirm we can append again cleanly.
	if err := Truncate(path, result.ValidUpto); err != nil {
		t.Fatalf("Truncate: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat after Truncate: %v", err)
	}
	if info.Size() != lastGoodEnd {
		t.Errorf("file size after Truncate = %d, want %d", info.Size(), lastGoodEnd)
	}

	w3 := openWriterT(t, path)
	recovery := testBead(t, "post-recovery")
	if _, err := w3.Append(recovery, CodecZstd, NewMeta("root1")); err != nil {
		t.Fatalf("Append after recovery: %v", err)
	}
	if err := w3.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	final, err := Scan(path, true)
	if err != nil {
		t.Fatalf("final Scan: %v", err)
	}
	if final.Damaged {
		t.Fatalf("final Scan reported damage: %v", final.DamageErr)
	}
	if len(final.Records) != 4 {
		t.Fatalf("final Scan returned %d records, want 4 (3 original + 1 post-recovery)", len(final.Records))
	}
	if final.Records[3].BeadID != recovery.ID {
		t.Errorf("final record BeadID = %s, want %s", final.Records[3].BeadID, recovery.ID)
	}
}

// --- Store layout: routing to <first 2 hex>/<64 hex>.pod and _shared -----

func TestStore_PatientPodPath(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	b := testBead(t, "layout")
	path, err := store.PatientPodPath(b.ID)
	if err != nil {
		t.Fatalf("PatientPodPath: %v", err)
	}

	wantDir := filepath.Join(dir, "pods", b.ID[:2])
	wantFile := filepath.Join(wantDir, b.ID+".pod")
	if path != wantFile {
		t.Errorf("PatientPodPath(%s) = %s, want %s", b.ID, path, wantFile)
	}
}

func TestStore_SharedPodPath(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	want := filepath.Join(dir, "pods", "_shared.pod")
	if got := store.SharedPodPath(); got != want {
		t.Errorf("SharedPodPath() = %s, want %s", got, want)
	}
}

func TestStore_MultiplePatientsRouteCorrectly(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	patients := []bead.Bead{
		testBead(t, "patient-A"),
		testBead(t, "patient-B"),
		testBead(t, "patient-C"),
	}

	// Write each patient's root Bead to its own Pod, plus one shared Bead.
	for _, p := range patients {
		podPath, err := store.EnsurePatientPodDir(p.ID)
		if err != nil {
			t.Fatalf("EnsurePatientPodDir(%s): %v", p.ID, err)
		}
		w := openWriterT(t, podPath)
		if _, err := w.Append(p, CodecZstd, NewMeta(p.ID)); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	if err := store.EnsurePodsDir(); err != nil {
		t.Fatalf("EnsurePodsDir: %v", err)
	}
	sharedBead := testBead(t, "shared-drug-master")
	sw := openWriterT(t, store.SharedPodPath())
	if _, err := sw.Append(sharedBead, CodecZstd, NewMeta("")); err != nil {
		t.Fatalf("Append to shared: %v", err)
	}

	files, err := store.ListPodFiles()
	if err != nil {
		t.Fatalf("ListPodFiles: %v", err)
	}
	if len(files) != len(patients)+1 {
		t.Fatalf("ListPodFiles returned %d files, want %d", len(files), len(patients)+1)
	}

	// Each patient's Bead must be readable from (only) its own Pod path.
	for _, p := range patients {
		wantPath, err := store.PatientPodPath(p.ID)
		if err != nil {
			t.Fatalf("PatientPodPath: %v", err)
		}
		result, err := Scan(wantPath, true)
		if err != nil {
			t.Fatalf("Scan(%s): %v", wantPath, err)
		}
		if len(result.Records) != 1 {
			t.Fatalf("Scan(%s) returned %d records, want 1", wantPath, len(result.Records))
		}
		if result.Records[0].BeadID != p.ID {
			t.Errorf("Scan(%s) record BeadID = %s, want %s", wantPath, result.Records[0].BeadID, p.ID)
		}
	}

	sharedResult, err := Scan(store.SharedPodPath(), true)
	if err != nil {
		t.Fatalf("Scan(shared): %v", err)
	}
	if len(sharedResult.Records) != 1 || sharedResult.Records[0].BeadID != sharedBead.ID {
		t.Fatalf("Scan(shared) = %+v, want single record with BeadID %s", sharedResult.Records, sharedBead.ID)
	}
}

// --- concurrency: multiple goroutines Append to the same Writer ----------

func TestWriter_ConcurrentAppend(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "patient.pod")
	w := openWriterT(t, path)

	const n = 50
	beads := make([]bead.Bead, n)
	for i := range beads {
		beads[i] = testBead(t, "concurrent-"+string(rune('a'+i%26))+string(rune('0'+i/26)))
	}

	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := w.Append(beads[i], CodecZstd, NewMeta("root-concurrent"))
			errs[i] = err
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}

	result, err := Scan(path, true)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if result.Damaged {
		t.Fatalf("Scan reported damage after concurrent Append: %v", result.DamageErr)
	}
	if len(result.Records) != n {
		t.Fatalf("Scan returned %d records, want %d", len(result.Records), n)
	}

	seen := make(map[string]bool, n)
	for _, rec := range result.Records {
		seen[rec.BeadID] = true
	}
	for _, b := range beads {
		if !seen[b.ID] {
			t.Errorf("bead %s missing from scanned records after concurrent Append", b.ID)
		}
	}
}
