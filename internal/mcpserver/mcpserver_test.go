package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/medbeads/medbeads/internal/engine"
	"github.com/medbeads/medbeads/internal/engine/apc"
	"github.com/medbeads/medbeads/internal/engine/bead"
)

// --- shared test scaffolding (mirrors internal/engine/apc/apc_test.go's
// openT/unsavedBead/ingestT/seedPatient/seedChildBead conventions — this
// project's established "each package's tests re-derive these small
// helpers" pattern, per apc_test.go's own doc comment on why) -------------

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

// unsavedBead returns an ID-less Bead for the given type/parents/content. No
// antigens parameter: v3.1 removed Bead.Antigens entirely (see bead.Bead's
// doc comment) — tag derivation now happens only at index-projection time
// (antigen.Extract), a fixed deterministic function with no override hook.
// See seedAntigens below for how a caller gets specific bead_antigens rows
// onto a seeded Bead for tests whose subject is Scanner/tag-filter behavior
// rather than tag derivation itself (mirrors
// internal/engine/apc/apc_test.go's identical helper and reasoning).
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

// seedAntigens inserts bead_antigens rows for the already-ingested Bead b
// directly (bypassing antigen.Extract entirely) — see unsavedBead's doc
// comment. patient_root is resolved from the index (e.Index().GetBead) —
// not trusted from a caller-supplied parameter — since b's own parent Bead
// is not necessarily the patient root (e.g. a Bead seeded under an
// intermediate encounter), and every downstream Scanner query
// (candidatesFor/frequentAntigens) is scoped by patient_root, so a wrong
// value here would silently break IDF-threshold/matching assertions for a
// reason unrelated to what a test is actually checking (mirrors
// internal/engine/apc/apc_test.go's identical helper and reasoning).
func seedAntigens(t *testing.T, e *engine.Engine, b bead.Bead, tags ...string) {
	t.Helper()
	ref, err := e.Index().GetBead(b.ID)
	if err != nil {
		t.Fatalf("seedAntigens(%s): resolve patient_root: %v", b.ID, err)
	}
	var root any
	if ref.PatientRoot != "" {
		root = ref.PatientRoot
	}
	for _, tag := range tags {
		if _, err := e.Index().SQLDB().Exec(
			`INSERT OR IGNORE INTO bead_antigens (antigen, bead_id, patient_root) VALUES (?, ?, ?)`,
			tag, b.ID, root,
		); err != nil {
			t.Fatalf("seedAntigens(%s, %v): %v", b.ID, tags, err)
		}
	}
}

func seedPatient(t *testing.T, e *engine.Engine, name string) bead.Bead {
	t.Helper()
	return ingestT(t, e, unsavedBead("patient_registration", nil, map[string]any{"name": name}))
}

// seedChildBead ingests a Bead of the given type/content as a child of
// parent, then (if antigens is non-empty) injects bead_antigens rows for it
// directly via seedAntigens — see unsavedBead's doc comment.
func seedChildBead(t *testing.T, e *engine.Engine, parent bead.Bead, typ string, antigens []string, content map[string]any) bead.Bead {
	t.Helper()
	b := ingestT(t, e, unsavedBead(typ, []string{parent.ID}, content))
	if len(antigens) > 0 {
		seedAntigens(t, e, b, antigens...)
	}
	return b
}

// padWithNoiseBeads mirrors internal/engine/apc/apc_test.go's helper of the
// same name: n Beads under parent, each carrying a distinct antigen no other
// Bead in the patient shares, so a genuinely-shared antigen elsewhere stays
// comfortably under apc.Config's default 30% patient-local IDF frequency
// threshold. Tests in this package that need a real APC sibling_link (e.g.
// the get_sibling_links clearance regression) need this the same way
// apc_test.go's own tests do.
func padWithNoiseBeads(t *testing.T, e *engine.Engine, parent bead.Bead, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		seedChildBead(t, e, parent, "fhir_observation",
			[]string{fmt.Sprintf("loinc:noise-%d", i)},
			map[string]any{"noise": i})
	}
}

// newServerT builds an mcpserver.Server directly (bypassing MCP transport
// plumbing) for tests that call tool handler methods in-process.
func newServerT(t testing.TB, e *engine.Engine, role string) *Server {
	t.Helper()
	s, err := New(Config{Engine: e, Role: role, APCConfig: apc.Default()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// decodeResult unmarshals a *mcp.CallToolResult's single TextContent item
// (jsonResult's own output shape) into v, failing the test on any shape
// mismatch. Used by the in-memory-transport integration test, which only
// gets back a *mcp.CallToolResult (not the typed Go Out value) from
// ClientSession.CallTool.
func decodeResult(t *testing.T, res *mcp.CallToolResult, v any) {
	t.Helper()
	if res == nil {
		t.Fatalf("decodeResult: nil CallToolResult")
	}
	if len(res.Content) != 1 {
		t.Fatalf("decodeResult: want 1 content item, got %d", len(res.Content))
	}
	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("decodeResult: content item is %T, want *mcp.TextContent", res.Content[0])
	}
	if err := json.Unmarshal([]byte(text.Text), v); err != nil {
		t.Fatalf("decodeResult: unmarshal %q: %v", text.Text, err)
	}
}

// connectInMemoryT connects srv to a fresh mcp.Client over
// mcp.NewInMemoryTransports, per that function's own doc comment ("Servers
// must be connected before clients"), and registers cleanup for both ends.
func connectInMemoryT(t *testing.T, srv *Server) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()

	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	serverSession, err := srv.MCPServer().Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("Server.Connect: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "mcpserver-test", Version: "v0"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("Client.Connect: %v", err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })

	return clientSession
}
