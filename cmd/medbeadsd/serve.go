package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/medbeads/medbeads/internal/engine"
	"github.com/medbeads/medbeads/internal/engine/apc"
	"github.com/medbeads/medbeads/internal/mcpserver"
)

// shutdownGrace bounds how long runServeHTTP waits for in-flight HTTP
// requests to finish during a SIGINT/SIGTERM-triggered graceful shutdown
// before giving up.
const shutdownGrace = 10 * time.Second

// runServe implements `medbeadsd serve`: opens the Engine at -data, builds
// an mcpserver.Server over it for -role, and runs the MCP server over stdio
// (the default transport) or Streamable HTTP if -http <addr> is given
// (specs/DESIGN_v3.md §2/§8, docs/requirements.md R6.1's "stdio + Streamable
// HTTP"). It blocks until SIGINT/SIGTERM, then shuts down gracefully: the
// MCP transport's Run/http.Server.Shutdown is given ctx to react to, and the
// Engine is Closed only after that unwinds — so a client mid-request over
// stdio or HTTP is not cut off by the Engine's lock release / index.db close
// racing its own in-flight query.
func runServe(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data", "", "MedBeads data directory (contains pods/, dict/, index.db)")
	role := fs.String("role", mcpserver.DefaultRole, "clearance/write role this server presents as "+
		"(e.g. viewer, system; system additionally unlocks create_bead)")
	httpAddr := fs.String("http", "", "if set, serve Streamable HTTP on this address instead of stdio (e.g. :8090)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dataDir == "" {
		fmt.Fprintln(stderr, "medbeadsd serve: -data <dir> is required")
		return 2
	}

	eng, err := engine.Open(*dataDir)
	if err != nil {
		fmt.Fprintf(stderr, "medbeadsd serve: open engine: %v\n", err)
		return 1
	}
	defer eng.Close() //nolint:errcheck // best-effort unwind; process is exiting either way

	srv, err := mcpserver.New(mcpserver.Config{
		Engine:    eng,
		Role:      *role,
		APCConfig: apc.Default(),
	})
	if err != nil {
		fmt.Fprintf(stderr, "medbeadsd serve: build MCP server: %v\n", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if *httpAddr == "" {
		return runServeStdio(ctx, srv, stdout, stderr)
	}
	return runServeHTTP(ctx, srv, *httpAddr, stdout, stderr)
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

// runServeHTTP runs srv's mcp.NewStreamableHTTPHandler on addr, blocking
// until ctx is cancelled, then gracefully shuts the http.Server down.
func runServeHTTP(ctx context.Context, srv *mcpserver.Server, addr string, stdout, stderr *os.File) int {
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return srv.MCPServer()
	}, nil)

	httpSrv := &http.Server{Addr: addr, Handler: handler}

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
