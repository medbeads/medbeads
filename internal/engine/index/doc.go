// Package index manages the SQLite index (index.db): schema migrations,
// IndexBead (the transactional write path: beads / bead_edges /
// bead_tags / beads_fts + the pods.indexed_upto watermark), read APIs
// (GetBead, ListPatientBeads, Search), and the Reindex/CatchUp rebuild-from-
// Pod paths. See specs/DESIGN_v3.md §2, §5.
//
// R3 scope: pods / beads / bead_edges / bead_antigens / beads_fts only.
// bead_apc_scan, clearance_rules/clearance_audit, and the sqlite-vec vec0
// table are later units' responsibility and are not created by this
// package's migrations yet. (bead_antigens was later superseded by bead_tags
// — specs/U2_projection_schema.md / U3a.)
//
// # SQLite driver and build tag
//
// This package uses github.com/mattn/go-sqlite3 (CGO) so that FTS5 (R3.3)
// and, later, a sqlite-vec extension load path (R4.2) are both available.
// FTS5 support itself is compiled into that driver only when building with
// the sqlite_fts5 build tag (e.g. `go build -tags sqlite_fts5 ./...`); this
// package's own Go source carries no build tags, since the tag only affects
// what mattn/go-sqlite3 compiles internally. A build without the tag still
// compiles and links, but any migration or query touching beads_fts
// (a virtual fts5 table) fails at runtime with "no such module: fts5" — see
// migrations/0001_init.sql.
package index
