package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"

	"github.com/sojin25/medbeads/core/types"
)

// predictHash reproduces the CAS hash that SaveToCAS computes for a bead, so a
// test can craft a parent edge that points back at the bead being saved.
func predictHash(t *testing.T, b types.Bead) string {
	t.Helper()
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		t.Fatalf("marshal bead: %v", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestHasAncestor_EmptyAndMissing(t *testing.T) {
	setupTestStore(t)

	if hasAncestor("", []string{"anything"}) {
		t.Error("hasAncestor with empty target should be false")
	}
	if hasAncestor("target", nil) {
		t.Error("hasAncestor with no start parents should be false")
	}
	if hasAncestor("target", []string{"nonexistent"}) {
		t.Error("hasAncestor should be false when the start parent is unknown")
	}
}

func TestHasAncestor_DirectAndIndirect(t *testing.T) {
	setupTestStore(t)

	// Build a normal DAG: A (root) <- B <- C.
	a := seedPatient(t, "A")
	b := seedChildBead(t, a, "encounter", map[string]interface{}{"n": "B"})
	c := seedChildBead(t, b, "observation", map[string]interface{}{"n": "C"})

	// Direct: A is a parent of B.
	if !hasAncestor(a, []string{b}) {
		t.Error("hasAncestor(A, [B]) should be true (direct parent)")
	}
	// Indirect: A is reachable from C via B.
	if !hasAncestor(a, []string{c}) {
		t.Error("hasAncestor(A, [C]) should be true (indirect ancestor)")
	}
	// C is a descendant of A, not an ancestor.
	if hasAncestor(c, []string{a}) {
		t.Error("hasAncestor(C, [A]) should be false (C is a descendant)")
	}
}

func TestSaveToCAS_NormalDAGSucceeds(t *testing.T) {
	setupTestStore(t)

	// A three-level DAG must save without a false cycle detection.
	a := seedPatient(t, "patient")
	b := seedChildBead(t, a, "encounter", map[string]interface{}{"n": "visit"})
	if _, err := SaveToCAS(types.Bead{
		Type:      "observation",
		Timestamp: "2026-01-03T00:00:00Z",
		Parents:   []string{b},
		Content:   map[string]interface{}{"n": "result"},
	}); err != nil {
		t.Errorf("SaveToCAS on a valid DAG returned an error: %v", err)
	}
}

func TestSaveToCAS_RejectsCycle(t *testing.T) {
	setupTestStore(t)

	// The bead we are about to save.
	bead := types.Bead{
		Type:      "observation",
		Timestamp: "2026-01-04T00:00:00Z",
		Parents:   []string{"crafted-parent"},
		Content:   map[string]interface{}{"n": "cyclic"},
	}
	// Predict its hash and craft a parent whose own parents point back at it,
	// so walking up from the bead's parents reaches the bead itself.
	selfHash := predictHash(t, bead)
	parentsJSON, _ := json.Marshal([]string{selfHash})
	if _, err := DB.Exec(
		`INSERT INTO beads (id, type, timestamp, parents) VALUES (?, 'encounter', '2026-01-01T00:00:00Z', ?)`,
		"crafted-parent", string(parentsJSON),
	); err != nil {
		t.Fatalf("insert crafted parent: %v", err)
	}

	_, err := SaveToCAS(bead)
	if !errors.Is(err, ErrCycleDetected) {
		t.Errorf("SaveToCAS error = %v, want ErrCycleDetected", err)
	}
}
