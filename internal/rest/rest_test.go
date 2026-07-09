package rest

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/medbeads/medbeads/internal/engine"
	"github.com/medbeads/medbeads/internal/engine/bead"
	"github.com/medbeads/medbeads/internal/engine/clearance"
)

// --- shared test scaffolding (mirrors internal/mcpserver/mcpserver_test.go's
// openT/unsavedBead/ingestT/seedPatient/seedChildBead conventions — this
// project's established "each package's tests re-derive these small
// helpers" pattern) ---------------------------------------------------

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

func unsavedBead(typ string, parents []string, content map[string]any) bead.Bead {
	if content == nil {
		content = map[string]any{}
	}
	return bead.Bead{
		Type:      typ,
		Timestamp: nextTimestamp(),
		Author:    "did:medbeads:doctor:12345",
		Parents:   parents,
		Content:   content,
	}
}

func ingestT(t *testing.T, e *engine.Engine, b bead.Bead) bead.Bead {
	t.Helper()
	out, err := e.Ingest(b)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	return out
}

func seedPatient(t *testing.T, e *engine.Engine, name string) bead.Bead {
	t.Helper()
	return ingestT(t, e, unsavedBead("patient_registration", nil, map[string]any{
		"name": name, "birthDate": "1990-01-01", "gender": "female",
	}))
}

func seedChildBead(t *testing.T, e *engine.Engine, parent bead.Bead, typ string, content map[string]any) bead.Bead {
	t.Helper()
	return ingestT(t, e, unsavedBead(typ, []string{parent.ID}, content))
}

// newServerT builds a rest.Server directly over e, for httptest-driven
// handler tests.
func newServerT(t testing.TB, e *engine.Engine) *Server {
	t.Helper()
	s, err := New(Config{Engine: e, CORSAllowedOrigins: []string{"*"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// decodeJSON decodes rr's body into v, failing the test on any JSON error.
func decodeJSON(t *testing.T, rr *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rr.Body.Bytes(), v); err != nil {
		t.Fatalf("decode response %q: %v", rr.Body.String(), err)
	}
}

// saveRuleT saves a clearance rule denying deniedRoles for beadID, failing
// the test on error.
func saveRuleT(t *testing.T, e *engine.Engine, id, beadID string, deniedRoles []string) {
	t.Helper()
	if err := clearance.SaveRule(e.Index(), clearance.Rule{
		ID:          id,
		BeadID:      beadID,
		DeniedRoles: deniedRoles,
		CreatedBy:   "test",
		CreatedAt:   "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("SaveRule: %v", err)
	}
}
