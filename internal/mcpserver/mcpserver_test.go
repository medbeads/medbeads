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

func unsavedBead(typ string, parents, antigens []string, content map[string]any) bead.Bead {
	if content == nil {
		content = map[string]any{}
	}
	return bead.Bead{
		Type:      typ,
		Timestamp: nextTimestamp(),
		Author:    "did:medbeads:doctor:12345",
		Parents:   parents,
		Antigens:  antigens,
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
	return ingestT(t, e, unsavedBead("patient_registration", nil, nil, map[string]any{"name": name}))
}

func seedChildBead(t *testing.T, e *engine.Engine, parent bead.Bead, typ string, antigens []string, content map[string]any) bead.Bead {
	t.Helper()
	return ingestT(t, e, unsavedBead(typ, []string{parent.ID}, antigens, content))
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
