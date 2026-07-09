// Package apc implements the APC (Antigen Presenting Cell) batch scanner
// that generates sibling_link Beads by matching shared antigens between
// Beads within the same patient. See specs/DESIGN_v3.md §2, §7,
// specs/MEDBEADS_SIBLING_SPEC.md, and docs/requirements.md R5.
//
// v3.0 is batch-only (Scan is called explicitly, e.g. after an ingest batch)
// — no resident goroutine or event-driven trigger is implemented here;
// that is M4 scope (docs/requirements.md §8).
//
// Scanner depends on a narrow ingester interface (Ingest + GetBead) rather
// than importing package engine directly, and on *index.DB for the
// SQL queries its own read API does not expose (bead_apc_scan,
// sibling_pairs, bead_antigens-by-(antigen,patient_root) lookups) — the same
// "apc does not import engine" convention package graph documents for
// itself (see graph/doc.go): apc is a sibling of engine/pod, engine/index,
// engine/graph under internal/engine/, and it is engine's/cmd's job to wire
// a *Scanner on top of a live *engine.Engine (via engine.Engine.Index()),
// not apc's job to import engine.
package apc
