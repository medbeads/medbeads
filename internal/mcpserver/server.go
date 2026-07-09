package mcpserver

import (
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/medbeads/medbeads/internal/engine"
	"github.com/medbeads/medbeads/internal/engine/apc"
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

// Server bundles one MedBeads Engine (plus the graph/apc/clearance
// collaborators built on top of it) behind the official MCP SDK, per
// specs/DESIGN_v3.md §2/§8 and docs/requirements.md R6. See doc.go for the
// package-level scope note (L2 semantic / rag_search excluded from this
// unit).
type Server struct {
	eng   *engine.Engine
	store *pod.Store
	scan  *apc.Scanner
	role  string
	mcp   *mcp.Server
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

	// APCConfig configures the apc_trigger tool's Scanner (system role only —
	// see registerWriteTools). The zero value (apc.Config{}) would disable
	// every match (MinScoreThreshold 0 still works, but every
	// runaway-prevention cap would also be 0); callers should pass
	// apc.Default() unless they have a specific reason not to.
	APCConfig apc.Config

	// Implementation identifies this server to MCP clients (name/version in
	// the initialize handshake). A nil value uses a package default.
	Implementation *mcp.Implementation
}

// defaultImplementation is used when Config.Implementation is nil.
func defaultImplementation() *mcp.Implementation {
	return &mcp.Implementation{Name: "medbeadsd", Version: "v3.0.0-m1"}
}

// New builds a Server over cfg.Engine: an apc.Scanner wired to the same
// Engine (for apc_status/apc_trigger), and every MCP tool registered per
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
		eng:   cfg.Engine,
		store: pod.NewStore(cfg.Engine.DataDir()),
		scan:  apc.New(cfg.Engine, cfg.Engine.Index(), cfg.APCConfig),
		role:  role,
		mcp:   mcp.NewServer(impl, &mcp.ServerOptions{Instructions: instructions}),
	}

	s.registerReadTools()
	if role == SystemRole {
		s.registerWriteTools()
	}

	return s, nil
}

// instructions is surfaced to MCP clients during initialize (ServerOptions.
// Instructions) so an agent immediately knows retrieve's semantic-search
// scope limit without needing to call a tool and get an error first.
const instructions = `MedBeads v3 MCP server (M1). retrieve(semantic=true) is not yet available: ` +
	`L2 semantic search (sqlite-vec + embedder) lands in a later unit. Use retrieve with ` +
	`semantic=false (the default) for FTS anchor + graph expansion + token-budgeted context.`

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
