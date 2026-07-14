package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/medbeads/medbeads/internal/engine"
	"github.com/medbeads/medbeads/internal/engine/embedder"
	"github.com/medbeads/medbeads/internal/engine/trust"
	"github.com/medbeads/medbeads/internal/mcpserver"
	"github.com/medbeads/medbeads/internal/rest"
)

// shutdownGrace bounds how long runServeHTTP waits for in-flight HTTP
// requests to finish during a SIGINT/SIGTERM-triggered graceful shutdown
// before giving up.
const shutdownGrace = 10 * time.Second

// runServe implements `medbeadsd serve`: opens the Engine at -data, builds
// an mcpserver.Server over it for -role, and runs the MCP server over stdio
// (the default transport) or Streamable HTTP if -http <addr> is given
// (specs/DESIGN_v3.md §2/§8, docs/requirements.md R6.1's "stdio + Streamable
// HTTP"). When serving over HTTP, the MCP Streamable HTTP handler (at /mcp)
// and the REST projection for the UI (internal/rest, v2.2.0's frozen
// core/api paths, at the mux root) share one *http.Server on the same
// listener — "同一 mux で MCP と REST を共存" per this unit's task, so a
// single -http addr is the one HTTP surface medbeadsd exposes. It blocks
// until SIGINT/SIGTERM, then shuts down gracefully: the MCP transport's
// Run/http.Server.Shutdown is given ctx to react to, and the Engine is
// Closed only after that unwinds — so a client mid-request over stdio or
// HTTP is not cut off by the Engine's lock release / index.db close racing
// its own in-flight query.
func runServe(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data", "", "MedBeads data directory (contains pods/, dict/, index.db)")
	role := fs.String("role", mcpserver.DefaultRole, "clearance/write role this server presents as "+
		"(e.g. viewer, system; system additionally unlocks create_bead)")
	httpAddr := fs.String("http", "", "if set, serve Streamable HTTP on this address instead of stdio (e.g. :8090)")
	embedderURL := fs.String("embedder", "", "if set, base URL of an OpenAI-compatible /v1/embeddings "+
		"server (e.g. http://localhost:8080); enables retrieve(semantic=true), rag_search, and starts the "+
		"async embed indexer (R4.2). Unset (default): semantic search tools return a clear error, and no "+
		"embed-indexer goroutine is started at all.")
	embedModel := fs.String("embed-model", embedder.DefaultModel, "model name sent to the -embedder server "+
		"for PASSAGE (document/search_text) embedding — the async embed indexer's every request "+
		"(default \"ruri-v3\", cl-nagoya/ruri-v3-310m, 768 dims — must match index.db's migrations/"+
		"0004_embed.sql vec0 column width)")
	embedModelQuery := fs.String("embed-model-query", "", "model name sent to the -embedder server for QUERY "+
		"embedding (retrieve/rag_search's single-string embed calls) — lets an E5-family sidecar apply its "+
		"query: task-prefix instead of the passage: one -embed-model gets (e.g. -embed-model e5-passage "+
		"-embed-model-query e5-query). Unset (default): reuses -embed-model's value and its Client instance "+
		"for query embedding too — today's original single-model-string behavior, unchanged.")
	projectionCodeVersion := fs.String("projection-code-version", engine.DefaultProjectionCodeVersion(),
		"code/build version recorded for automatic patient-local projections; changing it starts a prioritized rolling reproject")
	recordStateCodeVersion := fs.String("record-state-code-version", engine.DefaultRecordStateProjectionCodeVersion(),
		"record_state algorithm contract version; change only when correction-state semantics require a full status rebuild")
	linkReprojectBatch := fs.Int("link-reproject-batch", 25,
		"patients updated per background rolling-link batch; 0 disables background draining (new patient data is still projected immediately)")
	linkReprojectInterval := fs.Duration("link-reproject-interval", 30*time.Second,
		"interval between background rolling-link batches")
	linkInactiveAfter := fs.Duration("link-inactive-after", engine.DefaultReprojectionInactiveAfter,
		"patients without an encounter in this window are processed after recent patients")
	trustPolicyPath := fs.String("trust-policy", "", "public trust policy JSON; require_knowledge_release=true rejects unsigned link-rule generations")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dataDir == "" {
		fmt.Fprintln(stderr, "medbeadsd serve: -data <dir> is required")
		return 2
	}

	var trustPolicy *trust.Policy
	if *trustPolicyPath != "" {
		loadedPolicy, err := trust.LoadPolicy(*trustPolicyPath)
		if err != nil {
			fmt.Fprintf(stderr, "medbeadsd serve: %v\n", err)
			return 1
		}
		trustPolicy = loadedPolicy
	}

	eng, err := engine.OpenWithOptions(*dataDir, engine.OpenOptions{
		AutoProject:                  true,
		ProjectionCodeVersion:        *projectionCodeVersion,
		RecordStateProjectionVersion: *recordStateCodeVersion,
		TrustPolicy:                  trustPolicy,
	})
	if err != nil {
		fmt.Fprintf(stderr, "medbeadsd serve: open engine: %v\n", err)
		return 1
	}
	defer eng.Close() //nolint:errcheck // best-effort unwind; process is exiting either way

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	noWorker := make(chan struct{})
	close(noWorker)
	var workerDone <-chan struct{} = noWorker
	if *linkReprojectBatch > 0 {
		workerDone = startLinkReprojectionWorker(
			ctx, eng, *linkReprojectBatch, *linkReprojectInterval,
			*linkInactiveAfter, stderr,
		)
	}
	defer func() {
		stop()
		<-workerDone
	}()

	// R4.2: an embedder is opt-in via -embedder. When unset, mcpCfg.Embedder
	// stays nil (mcpserver.Config's own "no embedder configured" default —
	// retrieve(semantic=true)/rag_search return a clear tool-level error) and
	// no embed-indexer goroutine is ever started, preserving this project's
	// "既存の「常駐 goroutine なし」衛生を維持" decision for every invocation
	// that does not explicitly ask for one.
	passageClient, queryClient := buildEmbedClients(*embedderURL, *embedModel, *embedModelQuery)
	if passageClient != nil {
		// StartEmbedIndexer's goroutine is scoped to ctx (the same
		// SIGINT/SIGTERM shutdown context the MCP/HTTP server below uses),
		// so it stops at the same point the rest of this process starts
		// winding down, rather than needing its own separate lifecycle
		// management. Its returned done channel is intentionally not waited
		// on here: eng.Close() below (deferred) does not depend on the
		// indexer goroutine having fully exited first (it only stops
		// enqueuing new batch transactions once ctx is Done; any batch
		// transaction already in flight completes or rolls back on its own
		// via the standard database/sql Tx semantics, same as any other
		// in-flight write this process might have open at shutdown).
		fmt.Fprintf(stderr, "medbeadsd serve: embed indexer enabled (embedder=%s, passage-model=%s, query-model=%s)\n",
			*embedderURL, passageClient.Model(), queryClient.Model())
		eng.StartEmbedIndexer(ctx, passageClient, engine.EmbedIndexerOptions{})
	}

	mcpCfg := mcpserver.Config{
		Engine: eng,
		Role:   *role,
	}
	if queryClient != nil {
		mcpCfg.Embedder = queryClient
	}
	srv, err := mcpserver.New(mcpCfg)
	if err != nil {
		fmt.Fprintf(stderr, "medbeadsd serve: build MCP server: %v\n", err)
		return 1
	}

	if *httpAddr == "" {
		return runServeStdio(ctx, srv, stdout, stderr)
	}

	restSrv, err := rest.New(rest.Config{
		Engine:             eng,
		ServiceToken:       os.Getenv("MEDBEADS_SERVICE_TOKEN"),
		CORSAllowedOrigins: parseCSVEnv("MEDBEADS_CORS_ORIGINS"),
		RateLimit:          parseIntEnv("MEDBEADS_RATE_LIMIT"),
	})
	if err != nil {
		fmt.Fprintf(stderr, "medbeadsd serve: build REST server: %v\n", err)
		return 1
	}

	return runServeHTTP(ctx, srv, restSrv, *httpAddr, stdout, stderr)
}

// startLinkReprojectionWorker drains only a bounded patient batch per tick.
// ProcessLinkReprojectionQueue releases Engine's ingest mutex between patients,
// so ordinary writes can enter between maintenance transactions. The first
// batch runs immediately; subsequent batches are deliberately rate-limited.
func startLinkReprojectionWorker(ctx context.Context, eng *engine.Engine, batchSize int, interval, inactiveAfter time.Duration, stderr *os.File) <-chan struct{} {
	done := make(chan struct{})
	if batchSize <= 0 {
		close(done)
		return done
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			result, err := eng.ProcessLinkReprojectionQueue(batchSize, time.Now().UTC(), inactiveAfter)
			if err != nil {
				fmt.Fprintf(stderr, "medbeadsd serve: rolling link reprojection: %v\n", err)
			} else if result.Projected > 0 || result.Failed > 0 {
				fmt.Fprintf(stderr, "medbeadsd serve: rolling link reprojection: projected=%d recent=%d inactive=%d deceased=%d failed=%d remaining=%d\n",
					result.Projected, result.Recent, result.Inactive, result.Deceased, result.Failed, result.Remaining)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return done
}

// buildEmbedClients builds the (up to) two embedder.Client values runServe
// wires up: passage is used for StartEmbedIndexer's batch document
// (search_text) embedding, query is used for mcpCfg.Embedder's single-string
// query embedding (retrieve/rag_search). Both are nil if embedderURL is ""
// (the "-embedder not set" case — no embedder configured at all).
//
// query gets its own *embedder.Client only when embedModelQuery is
// non-empty AND differs from embedModel — otherwise it is literally the same
// *embedder.Client pointer as passage, which is both a minor allocation
// saving and, more importantly, the exact behavior this function replaces:
// before -embed-model-query existed, runServe passed one shared
// *embedder.Client to both StartEmbedIndexer and mcpCfg.Embedder, so every
// invocation that leaves -embed-model-query unset (the default) must
// reproduce that identically — this is what makes the flag backward
// compatible rather than a behavior change for existing callers.
//
// Splitting into two Clients (rather than one Client sending a role flag
// through some side channel) matters because an E5-family embedding sidecar
// needs a per-request, wire-visible way to tell a passage embed request from
// a query one to apply the right task-prefix ("passage: " vs "query: "),
// and the only field a stock OpenAI-compatible /v1/embeddings request
// carries for that is `model` — see bench/README.md's "Embedding sidecar"
// section for the sidecar side of this contract (bench/embed_sidecar's
// --prefix-mode model_suffix inspects an -query/-passage model-name suffix).
func buildEmbedClients(embedderURL, embedModel, embedModelQuery string) (passage, query *embedder.Client) {
	if embedderURL == "" {
		return nil, nil
	}
	passage = embedder.New(embedderURL, embedModel, nil)

	queryModel := embedModelQuery
	if queryModel == "" {
		queryModel = embedModel
	}
	if queryModel == embedModel {
		return passage, passage
	}
	return passage, embedder.New(embedderURL, queryModel, nil)
}

// parseCSVEnv reads a comma-separated env var, trimming entries, per
// v2.2.0's core/api.parseCSVEnv (MEDBEADS_CORS_ORIGINS). An unset/empty env
// var yields nil, which rest.New treats as "use its own default allowlist"
// (v2's own fallback of localhost:5173/5174/3000).
func parseCSVEnv(name string) []string {
	raw := os.Getenv(name)
	if raw == "" {
		return nil
	}
	var out []string
	for _, item := range strings.Split(raw, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// parseIntEnv reads name as a positive int (MEDBEADS_RATE_LIMIT), returning
// 0 (rest.New's "use the default rate limit" sentinel) if unset or
// unparseable, per v2.2.0's core/api.StartServer's identical fallback.
func parseIntEnv(name string) int {
	v := os.Getenv(name)
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// runServeStdio runs srv over mcp.StdioTransport (the default transport),
// blocking until the client disconnects or ctx is cancelled (SIGINT/
// SIGTERM).
func runServeStdio(ctx context.Context, srv *mcpserver.Server, stdout, stderr *os.File) int {
	fmt.Fprintf(stderr, "medbeadsd serve: stdio transport, role=%s\n", srv.Role())
	if err := srv.MCPServer().Run(ctx, &mcp.StdioTransport{}); err != nil && ctx.Err() == nil {
		// ctx.Err() != nil means Run returned because of our own shutdown
		// signal, not a genuine transport failure — report only the latter
		// as an error exit.
		fmt.Fprintf(stderr, "medbeadsd serve: %v\n", err)
		return 1
	}
	return 0
}

// runServeHTTP mounts srv's mcp.NewStreamableHTTPHandler at /mcp and
// restSrv's REST projection (v2.2.0's frozen core/api paths — /patients,
// /search, /beads, /beads/context, /clearance, /clearance/check, /roles,
// /resource-counts) at the mux root onto one *http.Server, so a single
// -http addr serves both the MCP tool surface and the UI's REST API
// (docs/requirements.md R6.1's "REST は UI 用の従属 API"). It blocks until
// ctx is cancelled, then gracefully shuts the http.Server down.
func runServeHTTP(ctx context.Context, srv *mcpserver.Server, restSrv *rest.Server, addr string, stdout, stderr *os.File) int {
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return srv.MCPServer()
	}, nil)

	mux := restSrv.Mux()
	mux.Handle("/mcp", mcpHandler)
	mux.Handle("/mcp/", mcpHandler)

	httpSrv := &http.Server{Addr: addr, Handler: mux}

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- httpSrv.ListenAndServe()
	}()

	fmt.Fprintf(stderr, "medbeadsd serve: Streamable HTTP on %s, role=%s\n", addr, srv.Role())

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			fmt.Fprintf(stderr, "medbeadsd serve: shutdown: %v\n", err)
			return 1
		}
		return 0
	case err := <-serveErr:
		if err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(stderr, "medbeadsd serve: %v\n", err)
			return 1
		}
		return 0
	}
}
