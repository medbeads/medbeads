package pod

import (
	"errors"
	"path/filepath"
	"testing"
)

// --- Record.SelfVerify ----------------------------------------------------
//
// These tests exercise Record.SelfVerify directly (rather than only through
// VerifyPod/verifyRecord), since engine.GetBeadVerified and (indirectly)
// engine.decodeBeadRecord now call it as the cheaper, non-JCS alternative to
// bead.Verify on the read path (see internal/engine/read.go's doc comments).

func TestRecordSelfVerify_UntamperedFramePasses(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "patient.pod")
	w := openWriterT(t, path)

	b := testBead(t, "self-verify-ok")
	res, err := w.Append(b, CodecZstd, NewMeta("root1"))
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	r, err := OpenReader(path)
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer r.Close()

	rec, err := r.ReadAt(res.Offset)
	if err != nil {
		t.Fatalf("ReadAt: %v", err)
	}
	if err := rec.SelfVerify(); err != nil {
		t.Errorf("SelfVerify(untampered) = %v, want nil", err)
	}
}

// TestRecordSelfVerify_MismatchedBeadIDFails constructs a Record whose
// BeadID does not match its CoreBytes' content (as if a Pod frame had been
// written, by some bug, with the wrong bead_id paired against a given
// core_bytes) — SelfVerify must catch this even though nothing here touches
// CRC at all, demonstrating the two checks are logically independent: CRC
// binds a frame's bytes together as-stored (catches storage corruption
// after the fact), while SelfVerify/bead.Verify binds core_bytes' *content*
// to the specific ID a client trusts (catches the ID/content pairing itself
// being wrong, wherever that wrongness came from).
func TestRecordSelfVerify_MismatchedBeadIDFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "patient.pod")
	w := openWriterT(t, path)

	a := testBead(t, "record-a")
	b := testBead(t, "record-b")
	resA, err := w.Append(a, CodecRaw, NewMeta("root1"))
	if err != nil {
		t.Fatalf("Append(a): %v", err)
	}
	if _, err := w.Append(b, CodecRaw, NewMeta("root1")); err != nil {
		t.Fatalf("Append(b): %v", err)
	}

	r, err := OpenReader(path)
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer r.Close()

	recA, err := r.ReadAt(resA.Offset)
	if err != nil {
		t.Fatalf("ReadAt(a): %v", err)
	}

	// Swap in b's ID against a's CoreBytes: a mismatched (BeadID, CoreBytes)
	// pairing that never touched the on-disk CRC (this Record is built
	// purely in memory), so SelfVerify is the only thing that can catch it.
	mismatched := recA
	mismatched.BeadID = b.ID

	if err := mismatched.SelfVerify(); err == nil {
		t.Error("SelfVerify(mismatched bead_id/content) = nil, want error")
	} else if !errors.Is(err, ErrSelfVerifyMismatch) {
		t.Errorf("SelfVerify(mismatched) error = %v, want ErrSelfVerifyMismatch", err)
	}
}

func TestRecordSelfVerify_MalformedBeadIDFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "patient.pod")
	w := openWriterT(t, path)

	b := testBead(t, "malformed-id")
	res, err := w.Append(b, CodecRaw, NewMeta("root1"))
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	r, err := OpenReader(path)
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer r.Close()

	rec, err := r.ReadAt(res.Offset)
	if err != nil {
		t.Fatalf("ReadAt: %v", err)
	}
	rec.BeadID = "not-hex"
	if err := rec.SelfVerify(); err == nil {
		t.Error("SelfVerify(malformed bead_id) = nil, want error")
	}
}
