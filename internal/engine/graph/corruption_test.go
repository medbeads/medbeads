package graph_test

import (
	"errors"
	"os"
	"testing"

	"github.com/medbeads/medbeads/internal/engine/graph"
	"github.com/medbeads/medbeads/internal/engine/pod"
)

// TestLoadBundle_DetectsStorageCorruptionViaCRC confirms LoadBundle still
// fails on a corrupted frame even though decodeBundleRecord no longer runs
// bead.Verify's JCS re-canonicalization on every Bead (see bundle.go's doc
// comments for the threat-model reasoning this task's lead ruling settled
// on): CRC-32C (pod.Scan(path, true), unconditionally on) must still catch
// storage bit-rot, since that is the guarantee LoadBundle's own doc comment
// promises regardless of whether the read path also re-verifies content
// hashes.
func TestLoadBundle_DetectsStorageCorruptionViaCRC(t *testing.T) {
	e := openT(t)
	root := seedPatient(t, e, "corruption test")
	seedChildBead(t, e, root, "fhir_observation", map[string]any{"note": "will be corrupted downstream"})

	store := storeFor(e)
	podPath, err := store.PatientPodPath(root.ID)
	if err != nil {
		t.Fatalf("PatientPodPath: %v", err)
	}

	// Sanity check: LoadBundle succeeds before any tamper.
	if _, err := graph.LoadBundle(store, root.ID); err != nil {
		t.Fatalf("LoadBundle (untampered): %v", err)
	}

	// Flip a byte inside the second frame's core_bytes region, well past the
	// fixed header (magic+flags+core_len+meta_len+crc32c+bead_id — see
	// pod/frame.go's frameFixedSize), so it lands inside actual content
	// bytes rather than corrupting the header/length fields themselves.
	info, err := os.Stat(podPath)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	const frameFixedSize = 15 + 32 // headerSize(15) + idFieldSize(32)
	tamperOffset := info.Size() - 10
	if tamperOffset < frameFixedSize {
		t.Fatalf("pod file too small to tamper safely past its frame header: size=%d", info.Size())
	}

	f, err := os.OpenFile(podPath, os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("open for tamper: %v", err)
	}
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

	if _, err := graph.LoadBundle(store, root.ID); err == nil {
		t.Error("LoadBundle(tampered pod) = nil error, want CRC mismatch")
	} else if !errors.Is(err, pod.ErrCRCMismatch) {
		t.Errorf("LoadBundle(tampered pod) error = %v, want wrapping pod.ErrCRCMismatch", err)
	}
}
