package mcpserver

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/medbeads/medbeads/internal/engine"
	"github.com/medbeads/medbeads/internal/engine/clearance"
	"github.com/medbeads/medbeads/internal/engine/pod"
)

// Role names accepted by the -role flag (cmd/medbeadsd's serve subcommand).
// These are exactly clearance's own functional roles (internal/engine/
// clearance/roles.go): the MCP server's "role" is nothing more than the
// clearance viewerRoles this session presents as, per the lead's decision
// ("ローカルファースト前提のシンプルな方式" — one fixed role for the whole
// server process, not a per-request identity system). RoleSystem is the only
// role that unlocks create_bead (see registerWriteTools) and bypasses
// clearance.FilterByAccess (clearance.HasAccessWithRules's own existing
// bypass for RoleSystem/RoleEmergency — this package does not special-case
// it again).

// DefaultRole is the -role flag's default value when the caller specifies
// none: a read-only viewer with no elevated clearance-bypass role, per the
// lead's decision ("既定 \"viewer\"").
const DefaultRole = "viewer"

// SystemRole is the one role value that (a) registers create_bead and (b)
// is recognized by clearance.HasAccessWithRules as a clearance bypass
// (clearance.RoleSystem). Every other role string is passed through to
// clearance.FilterByAccess as this session's viewerRoles verbatim (so an
// operator can start the server as -role patient, -role primary_care, etc.
// and get real clearance-rule enforcement scoped to that functional role),
// but only "system" additionally unlocks the write tool.
const SystemRole = clearance.RoleSystem

// "viewer" is deliberately not one of clearance's own functional roles
// (clearance.AllRoles): it is this package's own default, meaning "no
// elevated role, subject to every active clearance rule" — passing
// []string{"viewer"} to clearance.FilterByAccess behaves identically to
// passing []string{} (clearance.HasAccessWithRules blocks any active rule
// for an unrecognized role either way), but keeping it as an explicit,
// named role string (rather than an empty slice) makes `-role` always have
// a concrete, loggable value and matches the lead's flag default verbatim.

// QueryEmbedder is the subset of internal/engine/embedder.Client's API
// retrieve/rag_search need to embed a caller-supplied query string before
// calling index.DB.SemanticSearch. Package mcpserver depends only on this
// interface (mirroring package engine's own identically-shaped Embedder
// interface, internal/engine/embedindex.go) — per the lead's "embedder を
// index 層に持ち込まない" decision, this package (like package engine) never
// constructs an embedder.Client itself; cmd/medbeadsd wires a real one in
// via Config.Embedder. A nil Config.Embedder means "no embedder configured
// for this process": retrieve(semantic=true) and rag_search both return a
// clear tool-level error rather than silently behaving as if semantic=false
// (see retrieve.go/rag_search.go).
type QueryEmbedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// Server bundles one MedBeads Engine (plus the graph/clearance
// collaborators built on top of it) behind the official MCP SDK, per
// specs/DESIGN_v3.md §2/§8 and docs/requirements.md R6.
type Server struct {
	eng      *engine.Engine
	store    *pod.Store
	role     string
	mcp      *mcp.Server
	embedder QueryEmbedder // nil if this process has no -embedder configured
}

// Config bundles the constructor arguments for New.
type Config struct {
	// Engine is the already-Open'd engine this server projects. New does not
	// take ownership of it: the caller (cmd/medbeadsd) opens and closes the
	// Engine.
	Engine *engine.Engine

	// Role is this server process's fixed clearance/write role (see the
	// SystemRole/DefaultRole doc comments above). Empty defaults to
	// DefaultRole.
	Role string

	// Embedder, if non-nil, enables retrieve(semantic=true) and rag_search
	// (R4.2/R6.3): query strings are embedded via this client before calling
	// index.DB.SemanticSearch. A nil Embedder (the default — most tests and
	// every CLI subcommand other than `serve -embedder ...`) makes both
	// semantic=true and rag_search return a tool-level "embedder not
	// configured" error rather than being silently unavailable.
	Embedder QueryEmbedder

	// Implementation identifies this server to MCP clients (name/version in
	// the initialize handshake). A nil value uses a package default.
	Implementation *mcp.Implementation
}

// defaultImplementation is used when Config.Implementation is nil.
func defaultImplementation() *mcp.Implementation {
	return &mcp.Implementation{Name: "medbeadsd", Version: "v3.0.0-m1"}
}

// New builds a Server over cfg.Engine: every MCP tool registered per
// cfg.Role (see registerReadTools/registerWriteTools). The returned Server's
// MCPServer() is ready to Run over any mcp.Transport (stdio or Streamable
// HTTP — see cmd/medbeadsd's serve subcommand).
func New(cfg Config) (*Server, error) {
	if cfg.Engine == nil {
		return nil, fmt.Errorf("mcpserver: new: Config.Engine must not be nil")
	}
	role := cfg.Role
	if role == "" {
		role = DefaultRole
	}
	impl := cfg.Implementation
	if impl == nil {
		impl = defaultImplementation()
	}

	s := &Server{
		eng:      cfg.Engine,
		store:    pod.NewStore(cfg.Engine.DataDir()),
		role:     role,
		mcp:      mcp.NewServer(impl, &mcp.ServerOptions{Instructions: instructions(cfg.Embedder != nil)}),
		embedder: cfg.Embedder,
	}

	s.registerReadTools()
	if role == SystemRole {
		s.registerWriteTools()
	}

	return s, nil
}

// instructions is surfaced to MCP clients during initialize (ServerOptions.
// Instructions) so an agent immediately knows retrieve's semantic-search
// availability without needing to call a tool and get an error first.
// embedderConfigured reflects whether this specific Server instance has a
// QueryEmbedder wired in (Config.Embedder != nil) — the message must not
// claim semantic search works when it does not for this process, nor claim
// it is unavailable when it is.
func instructions(embedderConfigured bool) string {
	if embedderConfigured {
		return `MedBeads v3 MCP server. retrieve(semantic=true) and rag_search are available: ` +
			`this process has an embedder configured, so L2 semantic search (sqlite-vec) runs ` +
			`alongside FTS anchor + graph expansion + token-budgeted context.`
	}
	return `MedBeads v3 MCP server. retrieve(semantic=true) and rag_search are NOT available: ` +
		`this process has no embedder configured (see serve's -embedder flag). Use retrieve with ` +
		`semantic=false (the default) for FTS anchor + graph expansion + token-budgeted context.`
}

// MCPServer returns the underlying *mcp.Server, ready to Connect/Run over a
// transport (mcp.StdioTransport, or mcp.NewStreamableHTTPHandler for HTTP —
// see cmd/medbeadsd's serve subcommand).
func (s *Server) MCPServer() *mcp.Server {
	return s.mcp
}

// Role returns the role this Server was constructed with (see Config.Role).
func (s *Server) Role() string {
	return s.role
}

// viewerRoles is the clearance role set every read-tool response is filtered
// through (clearance.FilterByAccess / clearance.HasAccessWithRules). system
// bypasses every clearance rule (clearance.HasAccessWithRules's own
// RoleSystem/RoleEmergency check) — this is "system はバイパス" from the task
// spec, and it falls straight out of passing the role through unchanged
// rather than this package special-casing it.
func (s *Server) viewerRoles() []string {
	return []string{s.role}
}
