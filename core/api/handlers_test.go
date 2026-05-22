package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/sojin25/medbeads/core/store"
	"github.com/sojin25/medbeads/core/types"
)

// --- C1: handlePatients masks restricted beads per viewer role ---

func TestHandlePatients_MasksRestrictedForInsurance(t *testing.T) {
	setupAPITest(t)

	open := seedPatientBead(t, "Open Patient")
	restricted := seedPatientBead(t, "Restricted Patient")
	if err := store.SaveClearanceRule(types.ClearanceRule{
		ID: "r1", BeadID: restricted, DeniedRoles: []string{"insurance"},
		CreatedBy: "test", CreatedAt: "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("SaveClearanceRule: %v", err)
	}

	r := httptest.NewRequest("GET", "/patients", nil)
	r.Header.Set("X-Viewer-Roles", "insurance")
	w := httptest.NewRecorder()
	handlePatients(w, r)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var beads []types.Bead
	if err := json.Unmarshal(w.Body.Bytes(), &beads); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	byID := map[string]types.Bead{}
	for _, b := range beads {
		byID[b.ID] = b
	}
	if byID[open].Content["name"] != "Open Patient" {
		t.Errorf("open patient should be visible, got %v", byID[open].Content)
	}
	if byID[restricted].Content["_restricted"] != true {
		t.Errorf("restricted patient should be masked, got %v", byID[restricted].Content)
	}
}

// --- C1/C2: getBeadHandler returns 403 for a denied viewer ---

func TestGetBeadHandler_ForbiddenForDeniedRole(t *testing.T) {
	setupAPITest(t)

	bead := seedPatientBead(t, "Secret Patient")
	if err := store.SaveClearanceRule(types.ClearanceRule{
		ID: "r1", BeadID: bead, DeniedRoles: []string{"insurance"},
		CreatedBy: "test", CreatedAt: "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("SaveClearanceRule: %v", err)
	}

	r := httptest.NewRequest("GET", "/beads?id="+bead, nil)
	r.Header.Set("X-Viewer-Roles", "insurance")
	w := httptest.NewRecorder()
	handleBeads(w, r)

	if w.Code != 403 {
		t.Errorf("status = %d, want 403 for a denied role", w.Code)
	}
}

func TestGetBeadHandler_AllowedForPermittedRole(t *testing.T) {
	setupAPITest(t)

	bead := seedPatientBead(t, "Patient")
	r := httptest.NewRequest("GET", "/beads?id="+bead, nil)
	r.Header.Set("X-Viewer-Roles", "primary_care")
	w := httptest.NewRecorder()
	handleBeads(w, r)

	if w.Code != 200 {
		t.Errorf("status = %d, want 200 for a permitted role", w.Code)
	}
}

// --- H4: createClearanceHandler validation ---

func postClearance(t *testing.T, body string, userID string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", "/clearance", bytes.NewBufferString(body))
	if userID != "" {
		r.Header.Set("X-User-ID", userID)
	}
	w := httptest.NewRecorder()
	createClearanceHandler(w, r)
	return w
}

func TestCreateClearanceHandler_Validation(t *testing.T) {
	setupAPITest(t)
	bead := seedPatientBead(t, "Patient")

	t.Run("missing X-User-ID is unauthorized", func(t *testing.T) {
		w := postClearance(t, `{"bead_id":"`+bead+`","denied_roles":["insurance"]}`, "")
		if w.Code != 401 {
			t.Errorf("status = %d, want 401", w.Code)
		}
	})

	t.Run("unknown bead_id is not found", func(t *testing.T) {
		w := postClearance(t, `{"bead_id":"nonexistent","denied_roles":["insurance"]}`, "dr-smith")
		if w.Code != 404 {
			t.Errorf("status = %d, want 404", w.Code)
		}
	})

	t.Run("invalid role is rejected", func(t *testing.T) {
		w := postClearance(t, `{"bead_id":"`+bead+`","denied_roles":["wizard"]}`, "dr-smith")
		if w.Code != 400 {
			t.Errorf("status = %d, want 400", w.Code)
		}
	})

	t.Run("denying a bypass role is rejected", func(t *testing.T) {
		w := postClearance(t, `{"bead_id":"`+bead+`","denied_roles":["system"]}`, "dr-smith")
		if w.Code != 400 {
			t.Errorf("status = %d, want 400", w.Code)
		}
	})

	t.Run("valid request is created", func(t *testing.T) {
		w := postClearance(t, `{"bead_id":"`+bead+`","denied_roles":["insurance"]}`, "dr-smith")
		if w.Code != 201 {
			t.Errorf("status = %d, want 201", w.Code)
		}
	})
}

// --- H5: handleContext depth validation ---

func TestHandleContext_DepthValidation(t *testing.T) {
	setupAPITest(t)
	bead := seedPatientBead(t, "Patient")

	cases := []struct {
		depth string
		want  int
	}{
		{"0", 400},
		{"51", 400},
		{"abc", 400},
		{"10", 200},
	}
	for _, tc := range cases {
		t.Run("depth="+tc.depth, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/beads/context?id="+bead+"&depth="+tc.depth, nil)
			r.Header.Set("X-Viewer-Roles", "primary_care")
			w := httptest.NewRecorder()
			handleContext(w, r)
			if w.Code != tc.want {
				t.Errorf("depth=%s: status = %d, want %d", tc.depth, w.Code, tc.want)
			}
		})
	}
}

// --- H2: saveHandler rejects a cycle with 400 ---

func TestSaveHandler_RejectsCycle(t *testing.T) {
	setupAPITest(t)

	// The bead saveHandler will reconstruct from this JSON body.
	bead := types.Bead{
		Type:      "observation",
		Timestamp: "2026-01-04T00:00:00Z",
		Parents:   []string{"crafted-parent"},
		Content:   map[string]interface{}{"n": "cyclic"},
	}
	data, _ := json.MarshalIndent(bead, "", "  ")
	sum := sha256.Sum256(data)
	selfHash := hex.EncodeToString(sum[:])

	// Craft a parent whose own parents point back at the bead's future hash.
	parentsJSON, _ := json.Marshal([]string{selfHash})
	if _, err := store.DB.Exec(
		`INSERT INTO beads (id, type, timestamp, parents) VALUES (?, 'encounter', '2026-01-01T00:00:00Z', ?)`,
		"crafted-parent", string(parentsJSON),
	); err != nil {
		t.Fatalf("insert crafted parent: %v", err)
	}

	body, _ := json.Marshal(bead)
	r := httptest.NewRequest("POST", "/beads", bytes.NewBuffer(body))
	w := httptest.NewRecorder()
	handleBeads(w, r)

	if w.Code != 400 {
		t.Errorf("status = %d, want 400 for a cycle-forming save", w.Code)
	}
}
