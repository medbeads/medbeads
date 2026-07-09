package clearance_test

import (
	"testing"

	"github.com/medbeads/medbeads/internal/engine"
	"github.com/medbeads/medbeads/internal/engine/bead"
	"github.com/medbeads/medbeads/internal/engine/clearance"
)

// --- shared test scaffolding ---------------------------------------------
//
// v2.2.0's core/store/testhelpers_test.go pointed a package-level global DB
// at an isolated temp SQLite file per test. v3 has no such global: every
// test instead opens its own *engine.Engine over a t.TempDir() data
// directory (engine.Open), exactly as internal/engine/apc/apc_test.go and
// internal/engine/graph/graph_test.go's identical scaffolding already do —
// this project's established "each engine-adjacent package's tests
// re-derive this same small helper set" convention (see apc_test.go's own
// comment to that effect), rather than sharing a test-only helper package.

func openT(t testing.TB) *engine.Engine {
	t.Helper()
	dir := t.TempDir()
	e, err := engine.Open(dir)
	if err != nil {
		t.Fatalf("engine.Open: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	return e
}

var timestampCounter int

func nextTimestamp() string {
	timestampCounter++
	sec := timestampCounter
	h := sec / 3600
	m := (sec % 3600) / 60
	s := sec % 60
	return fmtTimestamp(h, m, s)
}

func fmtTimestamp(h, m, s int) string {
	const digits = "0123456789"
	pad := func(n int) string {
		return string([]byte{digits[n/10%10], digits[n%10]})
	}
	return "2026-01-01T" + pad(h) + ":" + pad(m) + ":" + pad(s) + "Z"
}

// seedPatient stores a patient_registration bead and returns it as ingested
// (with its computed ID), mirroring v2.2.0's seedPatient test helper (which
// returned just the CAS id; v3's engine.Ingest returns the full Bead, which
// every caller here uses for both its ID and its Content).
func seedPatient(t *testing.T, e *engine.Engine, name string) bead.Bead {
	t.Helper()
	out, err := e.Ingest(bead.Bead{
		Type:      "patient_registration",
		Timestamp: nextTimestamp(),
		Parents:   []string{},
		Content:   map[string]any{"name": name},
	})
	if err != nil {
		t.Fatalf("seedPatient(%q): %v", name, err)
	}
	return out
}

// seedChildBead stores a bead with the given type/antigens/content as a
// child of parent and returns it as ingested, mirroring v2.2.0's
// seedChildBead test helper.
func seedChildBead(t *testing.T, e *engine.Engine, parent bead.Bead, beadType string, antigens []string, content map[string]any) bead.Bead {
	t.Helper()
	out, err := e.Ingest(bead.Bead{
		Type:      beadType,
		Timestamp: nextTimestamp(),
		Parents:   []string{parent.ID},
		Antigens:  antigens,
		Content:   content,
	})
	if err != nil {
		t.Fatalf("seedChildBead(parent=%s, type=%s): %v", parent.ID, beadType, err)
	}
	return out
}

// seedClearanceRule stores a clearance.Rule denying the given roles on
// beadID, mirroring v2.2.0's seedClearanceRule test helper. expiresAt may be
// nil for a permanent rule.
func seedClearanceRule(t *testing.T, e *engine.Engine, beadID string, deniedRoles []string, expiresAt *string) {
	t.Helper()
	rule := clearance.Rule{
		ID:          "rule-" + beadID,
		BeadID:      beadID,
		DeniedRoles: deniedRoles,
		CreatedBy:   "test",
		CreatedAt:   "2026-01-01T00:00:00Z",
		ExpiresAt:   expiresAt,
	}
	if err := clearance.SaveRule(e.Index(), rule); err != nil {
		t.Fatalf("seedClearanceRule(%s): %v", beadID, err)
	}
}
