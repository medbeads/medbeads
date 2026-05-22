package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sojin25/medbeads/core/types"
)

// TestSaveToCAS_StoresGzipped verifies objects are written gzip-compressed
// (gzip magic 0x1f 0x8b), not as plain JSON.
func TestSaveToCAS_StoresGzipped(t *testing.T) {
	setupTestStore(t)

	bead := types.Bead{
		Type:      "observation",
		Timestamp: "2026-03-01T10:00:00Z",
		Parents:   []string{},
		Content:   map[string]interface{}{"note": "compressible compressible compressible"},
	}
	id, err := SaveToCAS(bead)
	if err != nil {
		t.Fatalf("SaveToCAS: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(StorageDir, id))
	if err != nil {
		t.Fatalf("read object file: %v", err)
	}
	if len(raw) < 2 || raw[0] != 0x1f || raw[1] != 0x8b {
		t.Errorf("object file is not gzip-compressed; first bytes = %v", raw[:min(2, len(raw))])
	}
}

// TestSaveToCAS_HashUnchangedByCompression verifies the file name (= Bead ID)
// is still the SHA-256 of the uncompressed JSON, so compression does not change
// content-addressing.
func TestSaveToCAS_HashUnchangedByCompression(t *testing.T) {
	setupTestStore(t)

	bead := types.Bead{
		Type:      "condition",
		Timestamp: "2026-03-01T10:00:00Z",
		Parents:   []string{},
		Content:   map[string]interface{}{"code": "diabetes"},
	}
	id, err := SaveToCAS(bead)
	if err != nil {
		t.Fatalf("SaveToCAS: %v", err)
	}
	if want := predictHash(t, bead); id != want {
		t.Errorf("id = %q, want hash of uncompressed JSON %q", id, want)
	}
}

// TestGetFromCAS_ReadsLegacyUncompressed verifies objects written before
// compression was introduced (plain JSON) are still readable.
func TestGetFromCAS_ReadsLegacyUncompressed(t *testing.T) {
	setupTestStore(t)

	legacy := types.Bead{
		Type:      "encounter",
		Timestamp: "2025-01-01T00:00:00Z",
		Parents:   []string{},
		Content:   map[string]interface{}{"note": "legacy uncompressed object"},
	}
	plain, err := json.MarshalIndent(legacy, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	const legacyID = "legacy-plain-object"
	if err := os.WriteFile(filepath.Join(StorageDir, legacyID), plain, 0644); err != nil {
		t.Fatalf("write legacy file: %v", err)
	}

	got, err := GetFromCAS(legacyID)
	if err != nil {
		t.Fatalf("GetFromCAS legacy: %v", err)
	}
	if string(got) != string(plain) {
		t.Errorf("GetFromCAS returned %q, want %q", got, plain)
	}

	loaded, err := LoadFromCAS(legacyID)
	if err != nil {
		t.Fatalf("LoadFromCAS legacy: %v", err)
	}
	if loaded.Content["note"] != "legacy uncompressed object" {
		t.Errorf("legacy content = %v", loaded.Content["note"])
	}
}

// TestCompact runs the maintenance routine on a populated store.
func TestCompact(t *testing.T) {
	setupTestStore(t)

	patientID := seedPatient(t, "Compact Patient")
	seedChildBead(t, patientID, "encounter", map[string]interface{}{"note": "visit"})

	if err := Compact(); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	// Store must remain readable after compaction.
	if _, err := LoadFromCAS(patientID); err != nil {
		t.Errorf("LoadFromCAS after Compact: %v", err)
	}
}
