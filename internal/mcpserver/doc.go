// Package mcpserver exposes the engine as the first-class interface via the
// official modelcontextprotocol/go-sdk (github.com/modelcontextprotocol/
// go-sdk v1.6.1), over stdio and Streamable HTTP. See specs/DESIGN_v3.md
// §2, §8 and docs/requirements.md R6.
//
// # Scope (this unit)
//
// New builds one *Server per Engine + role: every read tool (list_patients,
// search_beads, get_bead, get_context, get_timeline, get_siblings,
// get_sibling_links, search_antigens, verify_integrity, apc_status,
// retrieve) is always registered; create_bead and apc_trigger are
// registered only when Config.Role == SystemRole (docs/requirements.md
// R6.3's "書き込みは system ロール限定"). apc_trigger is a write tool despite
// its empty input schema — apc.Scanner.Scan durably ingests any new
// sibling_link Bead it finds — so it is gated alongside create_bead rather
// than exposed to every role; see registerWriteTools' own doc comment.
//
// # Clearance: mask-then-drop, not mask-and-forward
//
// Every read tool's Bead-bearing output is passed through
// clearance.FilterByAccess with the server's own role as viewerRoles — a
// system-role server bypasses every rule (clearance.HasAccessWithRules's
// existing RoleSystem/RoleEmergency check), any other role is filtered.
// FilterByAccess itself masks a restricted Bead's Content to
// {"_restricted": true} *in place* (it never shrinks the slice it returns —
// see its own doc comment); this package's own, uniform policy on top of
// that is to treat a masked Bead as accessible() == false and drop it
// entirely from the response, never to forward the masked placeholder. This
// matters beyond get_bead/retrieve's own Content masking: several tools
// (search_beads, list_patients, get_timeline, search_antigens) attach
// index-derived metadata (a Summary fragment of the Bead's own Content)
// alongside the Bead, and get_sibling_links exposes another Bead's mere
// existence-as-a-sibling plus its matched_antigen (itself often a
// diagnosis-suggestive risk:/organ: tag) — none of which FilterByAccess's
// own Content-masking touches. Every such call site checks accessible()
// itself (see render.go's accessible doc comment) and drops the
// corresponding output row rather than relying on FilterByAccess alone.
//
// retrieve(semantic=true) is out of scope for this unit (L2 semantic search
// — sqlite-vec + an embedder — is a separate unit per docs/requirements.md
// R4.2) and returns a tool-level error rather than silently ignoring the
// flag. rag_search (pure vector top-k, DESIGN §8) is not registered at all
// yet, for the same reason.
//
// # ID notation
//
// Every Bead ID this package emits or accepts is passed through
// bead.FormatID/bead.ParseID at this exact boundary: internally (engine,
// graph, index) IDs stay plain 64-hex; the "sha256:" display prefix is
// applied only in this package's response types (render.go) and accepted
// (with or without the prefix) in every tool input (bead.ParseID) — see
// specs/DESIGN_v3.md §4's "内部は素の 64 hex、API/表示層でのみ sha256: プレ
// フィックス".
package mcpserver
