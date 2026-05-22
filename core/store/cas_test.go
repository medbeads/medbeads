package store

import (
	"testing"

	"github.com/sojin25/medbeads/core/types"
)

func TestSaveAndLoadFromCAS(t *testing.T) {
	setupTestStore(t)

	original := types.Bead{
		Type:      "observation",
		Timestamp: "2026-03-01T10:00:00Z",
		Parents:   []string{},
		Content:   map[string]interface{}{"bp": "120/80"},
	}

	id, err := SaveToCAS(original)
	if err != nil {
		t.Fatalf("SaveToCAS: %v", err)
	}
	if id == "" {
		t.Fatal("SaveToCAS returned an empty id")
	}

	loaded, err := LoadFromCAS(id)
	if err != nil {
		t.Fatalf("LoadFromCAS: %v", err)
	}
	if loaded.ID != id {
		t.Errorf("loaded.ID = %q, want %q", loaded.ID, id)
	}
	if loaded.Type != original.Type {
		t.Errorf("loaded.Type = %q, want %q", loaded.Type, original.Type)
	}
	if loaded.Content["bp"] != "120/80" {
		t.Errorf("loaded.Content[bp] = %v, want 120/80", loaded.Content["bp"])
	}
}

func TestSaveToCAS_HashIsDeterministic(t *testing.T) {
	setupTestStore(t)

	bead := types.Bead{
		Type:      "condition",
		Timestamp: "2026-03-01T10:00:00Z",
		Parents:   []string{},
		Content:   map[string]interface{}{"code": "diabetes"},
	}

	id1, err := SaveToCAS(bead)
	if err != nil {
		t.Fatalf("first SaveToCAS: %v", err)
	}
	id2, err := SaveToCAS(bead)
	if err != nil {
		t.Fatalf("second SaveToCAS: %v", err)
	}
	if id1 != id2 {
		t.Errorf("identical beads produced different ids: %q vs %q", id1, id2)
	}
}

func TestGetFromCAS_NotFound(t *testing.T) {
	setupTestStore(t)

	if _, err := GetFromCAS("does-not-exist"); err == nil {
		t.Error("GetFromCAS returned nil error for a missing id")
	}
}

func TestSaveToCAS_IndexesMetadata(t *testing.T) {
	setupTestStore(t)

	patientID := seedPatient(t, "Test Patient")
	childID := seedChildBead(t, patientID, "encounter", map[string]interface{}{"note": "checkup visit"})

	// beads table
	var beadType string
	if err := DB.QueryRow("SELECT type FROM beads WHERE id = ?", childID).Scan(&beadType); err != nil {
		t.Fatalf("beads row lookup: %v", err)
	}
	if beadType != "encounter" {
		t.Errorf("indexed type = %q, want encounter", beadType)
	}

	// FTS table — the content text should be searchable.
	var ftsID string
	if err := DB.QueryRow("SELECT id FROM beads_fts WHERE content MATCH ?", "checkup").Scan(&ftsID); err != nil {
		t.Fatalf("FTS lookup: %v", err)
	}
	if ftsID != childID {
		t.Errorf("FTS matched %q, want %q", ftsID, childID)
	}

	// edges table — child -> parent edge must exist.
	var edgeParent string
	if err := DB.QueryRow("SELECT parent_id FROM bead_edges WHERE child_id = ?", childID).Scan(&edgeParent); err != nil {
		t.Fatalf("edge lookup: %v", err)
	}
	if edgeParent != patientID {
		t.Errorf("edge parent = %q, want %q", edgeParent, patientID)
	}
}
