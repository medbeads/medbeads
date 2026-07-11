// Package mcpserver exposes the engine as the first-class interface via the
// official modelcontextprotocol/go-sdk (github.com/modelcontextprotocol/
// go-sdk v1.6.1), over stdio and Streamable HTTP. See specs/DESIGN_v3.md
// §2, §8 and docs/requirements.md R6.
//
// # Scope (this unit)
//
// New builds one *Server per Engine + role: every read tool (list_patients,
// search_beads, get_bead, get_context, get_timeline, get_links,
// search_antigens, verify_integrity, retrieve) is always registered;
// create_bead is registered only when Config.Role == SystemRole
// (docs/requirements.md R6.3's "書き込みは system ロール限定"). U5a
// (specs/U5_api_retrieve.md) removed the old get_siblings/get_sibling_links/
// apc_status/apc_trigger tools entirely along with package apc, the scanner
// that produced the sibling_link Beads those tools read — clinical_links
// (package projector, U3), surfaced via get_links and retrieve's
// ClinicalLinks sidecar, is now the sole link mechanism.
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
// alongside the Bead, and get_links exposes another Bead's mere
// existence-as-a-link plus its matched_tag (itself often a
// diagnosis-suggestive risk:/organ: tag) — none of which FilterByAccess's
// own Content-masking touches. Every such call site checks accessible()
// itself (see render.go's accessible doc comment) and drops the
// corresponding output row rather than relying on FilterByAccess alone.
//
// retrieve(semantic=true) and rag_search (pure vector top-k, DESIGN §8,
// docs/requirements.md R6.3) both require an embedder (Config.Embedder,
// R4.2): whenever New's caller does not configure one (Config.Embedder ==
// nil — most tests, and every CLI subcommand other than
// `serve -embedder ...`), both return a tool-level error rather than
// silently behaving as if semantic search were unavailable-but-ignored.
// rag_search is registered as a read tool (see registerReadTools) since it
// never mutates the data directory, even though it is explicitly an
// experimental/comparison tool (DESIGN §8's "比較実験用") rather than part of
// the main agent retrieval path.
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
