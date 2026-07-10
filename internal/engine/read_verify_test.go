package engine

import (
	"errors"
	"os"
	"testing"

	"github.com/medbeads/medbeads/internal/engine/pod"
)

// --- GetBead (CRC-only default) vs GetBeadVerified (CRC + self-verify) ----
//
// These tests exercise the read-path verification-mode split this task
// introduced: GetBead no longer runs bead.Verify's JCS re-canonicalization
// on every call (see read.go's doc comments for the threat-model reasoning),
// but it still always checks CRC-32C (ReadAtVerified). GetBeadVerified adds
// pod.Record.SelfVerify (decompress + SHA-256 == bead_id) on top of that,
// for a caller that wants the stronger per-Bead guarantee inline.

// TestGetBead_And_GetBeadVerified_AgreeOnUntamperedData confirms both entry
// points return the identical, correct Bead when nothing is wrong — the
// verification-mode split must never change the *data* a successful read
// returns, only how much integrity checking it does along the way.
func TestGetBead_And_GetBeadVerified_AgreeOnUntamperedData(t *testing.T) {
	e := openT(t)

	root, err := e.Ingest(unsavedBead("patient_registration", nil, nil, map[string]any{"name": "verify-mode test"}))
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	got, err := e.GetBead(root.ID)
	if err != nil {
		t.Fatalf("GetBead: %v", err)
	}
	gotVerified, err := e.GetBeadVerified(root.ID)
	if err != nil {
		t.Fatalf("GetBeadVerified: %v", err)
	}

	if got.ID != root.ID || gotVerified.ID != root.ID {
		t.Errorf("GetBead/GetBeadVerified ID = %s/%s, want %s", got.ID, gotVerified.ID, root.ID)
	}
	if got.Type != gotVerified.Type || got.Timestamp != gotVerified.Timestamp {
		t.Errorf("GetBead and GetBeadVerified disagree on content: %+v vs %+v", got, gotVerified)
	}
}

// TestGetBead_DetectsStorageCorruptionViaCRC confirms GetBead's CRC-only
// default still catches storage corruption (a flipped byte in core_bytes on
// disk) — the failure mode this task's lead ruling says must never be
// silently swallowed by the default read path, even though the (far more
// expensive) JCS content-hash re-verify is no longer run by default. Both
// GetBead and GetBeadVerified must fail on the same tampered frame, since
// both always run ReadAtVerified (CRC) first.
//
// This deliberately does NOT close and reopen the Engine around the tamper
// (contrast TestOpen_CatchUpRecoversPodOnlyWrite): Open's own crash-recovery
// CatchUp always re-scans a Pod's entire byte range with CRC verification
// (index.indexPodFrom's doc comment), so a corrupted already-indexed frame
// would fail Open itself, before ever reaching GetBead — a real and correct
// behavior, but not what this test is trying to isolate (GetBead/
// GetBeadVerified's own CRC check specifically). Tampering the file directly
// while the same Engine stays open is safe here: GetBead opens its own
// independent *pod.Reader per call (read.go's readBeadAt) rather than
// sharing the Engine's *pod.Writer file handle, and the data directory's
// flock is advisory only for other Engine/medbeadsd instances, not a
// per-file exclusive lock against this test's own os.OpenFile.
func TestGetBead_DetectsStorageCorruptionViaCRC(t *testing.T) {
	e := openT(t)

	root, err := e.Ingest(unsavedBead("patient_registration", nil, nil, map[string]any{"name": "corruption test"}))
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	store := pod.NewStore(e.DataDir())
	podPath, err := store.PatientPodPath(root.ID)
	if err != nil {
		t.Fatalf("PatientPodPath: %v", err)
	}
	ref, err := e.Index().GetBead(root.ID)
	if err != nil {
		t.Fatalf("Index().GetBead: %v", err)
	}

	// Flip one byte inside core_bytes (right after the fixed header +
	// bead_id, same tamper shape pod_test.go's
	// TestVerifyPod_DetectsTamperedCoreBytes uses) to simulate bit-rot at
	// rest.
	const frameFixedSize = 15 + 32 // headerSize(15) + idFieldSize(32), per pod/frame.go
	f, err := os.OpenFile(podPath, os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("open for tamper: %v", err)
	}
	buf := make([]byte, 1)
	tamperOffset := ref.Offset + frameFixedSize
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

	if _, err := e.GetBead(root.ID); err == nil {
		t.Error("GetBead(tampered) = nil error, want CRC mismatch")
	} else if !errors.Is(err, pod.ErrCRCMismatch) {
		t.Errorf("GetBead(tampered) error = %v, want wrapping pod.ErrCRCMismatch", err)
	}

	if _, err := e.GetBeadVerified(root.ID); err == nil {
		t.Error("GetBeadVerified(tampered) = nil error, want CRC mismatch")
	} else if !errors.Is(err, pod.ErrCRCMismatch) {
		t.Errorf("GetBeadVerified(tampered) error = %v, want wrapping pod.ErrCRCMismatch", err)
	}
}
