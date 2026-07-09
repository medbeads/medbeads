// Package rest is the subordinate API for the UI: a thin projection of the
// engine that freezes the current contract. See specs/DESIGN_v3.md §2.
//
// # Contract source and freeze
//
// "現行契約を凍結" (docs/requirements.md R6.1) means the contract this
// package implements is v2.2.0's core/api (git tag v2.2.0, package
// core/api — server.go plus its handler functions), not a redesign: same
// paths, same query/body JSON shapes, same status codes, same header names
// (X-Viewer-Roles / X-User-ID / X-Service-Token / X-Access-Reason), same
// CORS/rate-limit behavior. ui/src/lib/api.ts (the UI's one API client, not
// touched by this unit) is the other half of that frozen contract: every
// path/param/response-field name below matches what api.ts's exported
// functions (fetchAllPatients, searchPatients, fetchResourceCounts,
// fetchPatientTimeline, fetchClearanceRules, createClearanceRule,
// deleteClearanceRule, checkAccess, fetchAvailableRoles) actually send and
// expect.
//
// # ID notation
//
// v2's core/types.Bead.ID (and every JSON response built from it) was always
// plain lower-case 64-hex, with no "sha256:" display prefix — unlike
// internal/mcpserver, which is a *new* v3 API surface free to adopt the
// sha256: convention (specs/DESIGN_v3.md §4). Because this package's
// contract is frozen to v2's actual wire format, it deliberately does NOT
// call bead.FormatID anywhere: every ID in a request or response here is
// plain hex, exactly as v2's core/api emitted and as ui/src/lib/api.ts's
// mapBeadToPatient (bead.id.substring(0, 8), no prefix stripping) assumes.
//
// # Ported vs. excluded endpoints
//
// Every v2.2.0 core/api endpoint the current UI (ui/src/lib/api.ts) actually
// calls is ported: GET /patients, GET /search, GET /resource-counts, GET
// /beads/context, GET /beads, GET /clearance, POST /clearance, DELETE
// /clearance, GET /clearance/check, GET /roles. Two v2 surfaces are
// deliberately NOT ported, per R6.1's "薄い投影" (thin projection) — a
// subordinate API projects what the UI needs, not everything the old
// monolith exposed:
//
//   - POST /beads (bead ingest over REST): no ui/src/lib/api.ts function
//     calls it, and docs/requirements.md R6.3 confines writes to the MCP
//     server's system-role tools ("書き込みは system ロール限定"). Adding a
//     second, unauthenticated write path here would contradict that.
//   - The Gemini AI-insight endpoint (v2's separate api/ Python sidecar,
//     called by ui/src/lib/api.ts's fetchAIInsight against a different base
//     URL, AI_API_BASE_URL/"/api/ai"): retired by R6.4 ("Python api/
//     （Gemini インサイト）は廃止"). fetchAIInsight is left as dead code in
//     api.ts (untouched, per this unit's "UI 側コードは1文字も変更しない"),
//     but this package serves no route for it.
//
// # Engine projection, not business logic
//
// Every handler in this package is a thin translation from an HTTP
// request to an existing engine/graph/index/clearance call and back to
// JSON — no ranking, filtering-beyond-clearance, or derived business rule
// lives here. Where v2's handler logic (resource-type filtering,
// ancestor/descendant BFS, patient-root resolution) is now engine-side
// (graph.Bundle.Ancestors/Descendants replacing v2's SQL-per-hop BFS, per
// graph/bfs.go's own doc comments), this package calls that engine API
// rather than re-implementing the walk.
package rest
