package main

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/medbeads/medbeads/internal/engine"
	"github.com/medbeads/medbeads/internal/engine/apc"
	"github.com/medbeads/medbeads/internal/mcpserver"
)

// serve's own transports (mcp.StdioTransport hardcodes os.Stdin/os.Stdout,
// not an injectable io.Reader/Writer — see the go-sdk's transport.go) cannot
// be safely exercised against this test process's real stdin/stdout, so
// this package tests runServe's actual wiring — Engine open ->
// mcpserver.New -> tool registration — via a smoke test that stops short of
// calling Run over a real transport, per the task's own suggested fallback
// ("サーバー構築関数(NewServer)のスモークでよい"). internal/mcpserver's own
// test suite (TestIntegration_RetrieveOneRoundTrip and friends) exercises
// the MCP protocol surface end-to-end over mcp.NewInMemoryTransports, which
// is the realistic substitute for a stdio round trip that this package
// cannot provide.

// TestServeWiring_BuildsMCPServerOverRealEngine smoke-tests the exact
// sequence runServe/runServeStdio/runServeHTTP perform before touching any
// transport: engine.Open a real (temp-dir) data directory, then
// mcpserver.New over it for both the default (viewer) and system roles,
// checking each ends up with the tool registration cmd/medbeadsd's -role
// flag is documented to control (create_bead present only for system).
func TestServeWiring_BuildsMCPServerOverRealEngine(t *testing.T) {
	dir := t.TempDir()
	eng, err := engine.Open(dir)
	if err != nil {
		t.Fatalf("engine.Open: %v", err)
	}
	defer eng.Close()

	viewer, err := mcpserver.New(mcpserver.Config{Engine: eng, Role: mcpserver.DefaultRole, APCConfig: apc.Default()})
	if err != nil {
		t.Fatalf("mcpserver.New(role=%s): %v", mcpserver.DefaultRole, err)
	}
	if viewer.Role() != mcpserver.DefaultRole {
		t.Errorf("viewer.Role() = %q, want %q", viewer.Role(), mcpserver.DefaultRole)
	}
	if hasCreateBeadTool(t, viewer) {
		t.Errorf("viewer-role server registers create_bead; want it absent")
	}

	system, err := mcpserver.New(mcpserver.Config{Engine: eng, Role: mcpserver.SystemRole, APCConfig: apc.Default()})
	if err != nil {
		t.Fatalf("mcpserver.New(role=%s): %v", mcpserver.SystemRole, err)
	}
	if !hasCreateBeadTool(t, system) {
		t.Errorf("system-role server does not register create_bead; want it present")
	}
}

// TestServeWiring_MissingEngineErrors checks mcpserver.New's own input
// validation (Config.Engine required), which runServe relies on to fail
// fast rather than panicking on a nil Engine.
func TestServeWiring_MissingEngineErrors(t *testing.T) {
	if _, err := mcpserver.New(mcpserver.Config{}); err == nil {
		t.Fatalf("mcpserver.New with nil Engine: want error, got nil")
	}
}

// hasCreateBeadTool lists s's registered tools over an in-process
// mcp.NewInMemoryTransports connection (the same technique internal/
// mcpserver's own tests use) and reports whether create_bead is among them.
func hasCreateBeadTool(t *testing.T, s *mcpserver.Server) bool {
	t.Helper()
	ctx := context.Background()

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := s.MCPServer().Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("Server.Connect: %v", err)
	}
	defer serverSession.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "serve-test", Version: "v0"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("Client.Connect: %v", err)
	}
	defer clientSession.Close()

	tools, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	for _, tool := range tools.Tools {
		if tool.Name == "create_bead" {
			return true
		}
	}
	return false
}
