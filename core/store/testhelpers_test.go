package store

import (
	"testing"

	"github.com/sojin25/medbeads/core/types"
)

// setupTestStore points the package-level StorageDir/DBSource/DB at an isolated
// temp location, initializes a fresh SQLite DB + CAS directory, and restores
// the originals on test cleanup. Because the store package uses a single
// global DB connection, tests that call this must NOT run with t.Parallel().
func setupTestStore(t *testing.T) {
	t.Helper()

	origStorage, origDB, origConn := StorageDir, DBSource, DB

	dir := t.TempDir()
	StorageDir = dir + "/objects"
	DBSource = dir + "/metadata.db"

	if err := EnsureStorageDir(); err != nil {
		t.Fatalf("EnsureStorageDir: %v", err)
	}
	if err := InitDB(); err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	t.Cleanup(func() {
		if DB != nil {
			DB.Close()
		}
		StorageDir, DBSource, DB = origStorage, origDB, origConn
	})
}

// seedPatient stores a patient_registration bead and returns its CAS id.
func seedPatient(t *testing.T, name string) string {
	t.Helper()
	id, err := SaveToCAS(types.Bead{
		Type:      "patient_registration",
		Timestamp: "2026-01-01T00:00:00Z",
		Parents:   []string{},
		Content:   map[string]interface{}{"name": name},
	})
	if err != nil {
		t.Fatalf("seedPatient(%q): %v", name, err)
	}
	return id
}

// seedChildBead stores a bead with the given type/content as a child of parent
// and returns its CAS id.
func seedChildBead(t *testing.T, parentID, beadType string, content map[string]interface{}) string {
	t.Helper()
	id, err := SaveToCAS(types.Bead{
		Type:      beadType,
		Timestamp: "2026-01-02T00:00:00Z",
		Parents:   []string{parentID},
		Content:   content,
	})
	if err != nil {
		t.Fatalf("seedChildBead(parent=%s, type=%s): %v", parentID, beadType, err)
	}
	return id
}

// seedClearanceRule stores a clearance rule denying the given roles on beadID.
// expiresAt may be nil for a permanent rule.
func seedClearanceRule(t *testing.T, beadID string, deniedRoles []string, expiresAt *string) {
	t.Helper()
	rule := types.ClearanceRule{
		ID:          "rule-" + beadID,
		BeadID:      beadID,
		DeniedRoles: deniedRoles,
		CreatedBy:   "test",
		CreatedAt:   "2026-01-01T00:00:00Z",
		ExpiresAt:   expiresAt,
	}
	if err := SaveClearanceRule(rule); err != nil {
		t.Fatalf("seedClearanceRule(%s): %v", beadID, err)
	}
}
