package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/medbeads/medbeads/internal/engine"
	"github.com/medbeads/medbeads/internal/engine/apc"
	"github.com/medbeads/medbeads/internal/engine/index"
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

// TestBuildEmbedClients_NoEmbedderURL checks the "-embedder not set" case:
// no Clients at all, regardless of -embed-model/-embed-model-query (which
// is exactly what happens when a caller only sets those two but forgets
// -embedder — should not silently build a Client that is never used).
func TestBuildEmbedClients_NoEmbedderURL(t *testing.T) {
	passage, query := buildEmbedClients("", "e5-passage", "e5-query")
	if passage != nil || query != nil {
		t.Errorf("buildEmbedClients(embedderURL=\"\") = (%v, %v), want (nil, nil)", passage, query)
	}
}

// TestBuildEmbedClients_QueryFlagUnset_ReusesPassageClient is the backward-
// compatibility guarantee: leaving -embed-model-query unset (embedModelQuery
// == "") must reproduce the exact pre-flag behavior — one shared
// *embedder.Client for both roles, sending -embed-model's value on every
// request regardless of which role called Embed. This is what every
// existing `medbeadsd serve -embedder ... -embed-model ...` invocation
// (that predates -embed-model-query) must keep doing unchanged.
func TestBuildEmbedClients_QueryFlagUnset_ReusesPassageClient(t *testing.T) {
	passage, query := buildEmbedClients("http://example.invalid", "ruri-v3", "")
	if passage == nil || query == nil {
		t.Fatalf("buildEmbedClients: got (%v, %v), want two non-nil Clients", passage, query)
	}
	if passage != query {
		t.Errorf("buildEmbedClients(embedModelQuery=\"\"): passage and query Clients are different pointers, want the same *embedder.Client reused for both roles")
	}
	if passage.Model() != "ruri-v3" {
		t.Errorf("passage.Model() = %q, want %q", passage.Model(), "ruri-v3")
	}
	if query.Model() != "ruri-v3" {
		t.Errorf("query.Model() = %q, want %q", query.Model(), "ruri-v3")
	}
}

// TestBuildEmbedClients_QueryFlagMatchesPassage_ReusesClient checks an
// operator explicitly passing -embed-model-query equal to -embed-model
// (not just leaving it unset) still gets the single-Client-reuse behavior,
// not a redundant second Client with an identical model string.
func TestBuildEmbedClients_QueryFlagMatchesPassage_ReusesClient(t *testing.T) {
	passage, query := buildEmbedClients("http://example.invalid", "e5", "e5")
	if passage != query {
		t.Errorf("buildEmbedClients(embedModel == embedModelQuery): passage and query Clients are different pointers, want reuse")
	}
}

// TestBuildEmbedClients_DistinctQueryModel_SendsDifferentModelPerRole is the
// R (lead decision) this flag exists for: -embed-model-query set to a
// different value than -embed-model must produce two separate Clients that
// each report their own distinct Model() — i.e. the passage-side Client
// (wired to StartEmbedIndexer) and the query-side Client (wired to
// mcpCfg.Embedder) genuinely send different `model` field values on the
// wire, which is the only signal an E5-family sidecar has to pick the
// right task-prefix (see buildEmbedClients' doc comment).
func TestBuildEmbedClients_DistinctQueryModel_SendsDifferentModelPerRole(t *testing.T) {
	passage, query := buildEmbedClients("http://example.invalid", "e5-passage", "e5-query")
	if passage == nil || query == nil {
		t.Fatalf("buildEmbedClients: got (%v, %v), want two non-nil Clients", passage, query)
	}
	if passage == query {
		t.Errorf("buildEmbedClients(distinct models): passage and query Clients are the same pointer, want two separate Clients")
	}
	if passage.Model() != "e5-passage" {
		t.Errorf("passage.Model() = %q, want %q", passage.Model(), "e5-passage")
	}
	if query.Model() != "e5-query" {
		t.Errorf("query.Model() = %q, want %q", query.Model(), "e5-query")
	}
}

// TestServeWiring_EmbedderRoles_HTTPFake is the end-to-end version of the
// buildEmbedClients unit tests above: runs the actual passage.Embed/
// query.Embed HTTP round trips against one httptest.Server recording every
// request's `model` field, checking a passage-role Embed call and a
// query-role Embed call really do arrive with different model strings when
// -embed-model-query differs from -embed-model — i.e. this is not just
// buildEmbedClients returning the right Model() string cosmetically, the
// wire request itself carries it (mirrors client.go's embeddingsRequest
// contract, see cmd/medbeadsd/embed_test.go's fakeEmbeddingServer for the
// existing precedent of this technique in this package).
func TestServeWiring_EmbedderRoles_HTTPFake(t *testing.T) {
	var mu sync.Mutex
	var modelsSeen []string

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/embeddings", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		modelsSeen = append(modelsSeen, req.Model)
		mu.Unlock()

		type item struct {
			Embedding []float32 `json:"embedding"`
		}
		resp := struct {
			Data []item `json:"data"`
		}{}
		for range req.Input {
			resp.Data = append(resp.Data, item{Embedding: make([]float32, index.EmbedDim)})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	passage, query := buildEmbedClients(srv.URL, "e5-passage", "e5-query")

	ctx := context.Background()
	if _, err := passage.Embed(ctx, []string{"blood pressure 140/90"}); err != nil {
		t.Fatalf("passage.Embed: %v", err)
	}
	if _, err := query.Embed(ctx, []string{"hypertension"}); err != nil {
		t.Fatalf("query.Embed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(modelsSeen) != 2 {
		t.Fatalf("modelsSeen = %v, want 2 recorded requests", modelsSeen)
	}
	if modelsSeen[0] != "e5-passage" {
		t.Errorf("passage Embed sent model=%q, want %q", modelsSeen[0], "e5-passage")
	}
	if modelsSeen[1] != "e5-query" {
		t.Errorf("query Embed sent model=%q, want %q", modelsSeen[1], "e5-query")
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
