package api

import (
	"testing"

	"github.com/sojin25/medbeads/core/store"
	"github.com/sojin25/medbeads/core/types"
)

// setupAPITest initializes the api-package globals (service token, CORS
// allowlist, rate limiter) and an isolated store backed by a temp directory.
// Because the store uses a single global DB connection, api tests that touch
// the store must NOT run with t.Parallel().
func setupAPITest(t *testing.T) {
	t.Helper()

	origToken, origCORS, origRL := serviceToken, corsAllowedOrigins, globalRateLimiter
	serviceToken = "test-token"
	corsAllowedOrigins = []string{"http://localhost:5173"}
	globalRateLimiter = newRateLimiter(1000)

	origStorage, origDBSource, origDB := store.StorageDir, store.DBSource, store.DB
	dir := t.TempDir()
	store.StorageDir = dir + "/objects"
	store.DBSource = dir + "/metadata.db"
	if err := store.EnsureStorageDir(); err != nil {
		t.Fatalf("EnsureStorageDir: %v", err)
	}
	if err := store.InitDB(); err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	t.Cleanup(func() {
		if store.DB != nil {
			store.DB.Close()
		}
		store.StorageDir, store.DBSource, store.DB = origStorage, origDBSource, origDB
		serviceToken, corsAllowedOrigins, globalRateLimiter = origToken, origCORS, origRL
	})
}

// seedPatientBead stores a patient_registration bead and returns its CAS id.
func seedPatientBead(t *testing.T, name string) string {
	t.Helper()
	id, err := store.SaveToCAS(types.Bead{
		Type:      "patient_registration",
		Timestamp: "2026-01-01T00:00:00Z",
		Parents:   []string{},
		Content:   map[string]interface{}{"name": name},
	})
	if err != nil {
		t.Fatalf("seedPatientBead(%q): %v", name, err)
	}
	return id
}
