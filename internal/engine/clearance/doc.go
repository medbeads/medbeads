// Package clearance implements MedBeads' two-layer access-control model,
// ported from v2's core/store.go (v2.2.0 tag): department-scoped roles, a
// hybrid blacklist/whitelist DB rule table (clearance_rules), an audit log
// (clearance_audit), and content masking (FilterByAccess). See
// specs/DESIGN_v3.md §2 ("clearance/ embedded + DB ルール・監査ログ（v2 から
// 移植）") and §5 ("clearance_rules / clearance_audit は v2 踏襲").
//
// # Two layers
//
// v3's Bead carries an embedded, hash-excluded bead.Clearance overlay
// (internal/engine/bead's Clearance struct: DeniedRoles/AllowedRoles/Reason/
// ExpiresAt) that did not exist in v2 — v2's only clearance state lived in
// the DB-only clearance_rules table, keyed by bead_id. v3 keeps that DB
// layer (this package's Rule type + SaveRule/GetRules/... below, schema in
// migrations/0003_clearance.sql) and adds the embedded layer as an
// additional, independent source of the same DeniedRoles/AllowedRoles shape.
// HasAccessWithRules (this package) evaluates a bead's *combined* rule set —
// its own embedded bead.Clearance (if any) plus every DB clearance_rules row
// for its ID — as one flat list of independent constraints: a viewer must
// satisfy all of them, matching v2's HasAccessWithRules semantics extended
// to the extra embedded source. See Rule.
//
// The two layers differ in persistence and mutability, by design, not by
// accident:
//
//   - Embedded (bead.Bead.Clearance): fixed at Bead creation time. It is
//     excluded from the content hash (specs/DESIGN_v3.md §4) but is not
//     lost — pod.Writer.Append copies it into the frame's meta_bytes
//     (pod.Meta.Clearance, "minimal derived info, outside the hash" per
//     DESIGN §3), and every decode path (engine.GetBead, graph.LoadBundle,
//     index.Reindex/CatchUp) restores it from there. Because Pod is
//     append-only, there is no in-place edit path for an already-written
//     frame, so this layer can only be set once, at Ingest — it is a
//     durable but immutable annotation on that one Bead.
//   - DB (clearance_rules, via Rule/SaveRule/GetRules/DeleteRule): mutable
//     at any time, independent of the Bead's own storage — rules can be
//     added, replaced, deleted, or left to expire (ExpiresAt/IsRuleActive)
//     without touching the Bead itself. This is the layer a future M4
//     rule-editing API would operate on.
//
// A caller that needs to change access after a Bead already exists must use
// the DB layer; the embedded layer is not a substitute for that.
//
// # v3.0 scope (docs/requirements.md §8)
//
// v3.0 is schema + masking only: rule evaluation (HasAccess/
// HasAccessWithRules), content masking (FilterByAccess), and a minimal audit
// log write path (LogAction) exist here, but there is no rule-authoring
// HTTP/MCP API, no UI clearance editor, and no audit *query/report* surface
// — those are explicitly deferred to M4 ("Embedded Clearance の UI/監査完備
// は M4"). Writing a clearance_rules row today is done directly via
// SaveRule (e.g. from a test, an operator script, or a future M4 admin
// tool), not through any request-handling code in this package.
package clearance
