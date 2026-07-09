// Package antigen implements deterministic antigen extraction rules and the
// static mapping dictionary used to tag Beads. See specs/DESIGN_v3.md §2, §6
// and docs/requirements.md R4.4.
//
// # Purity and call-site responsibility
//
// Extract is a pure function: content (and beadType) in, an antigens slice
// out. It performs no I/O beyond the one-time embedded dictionary load and
// never touches the Bead ID.
//
// Antigens are a hash-target field of Bead (specs/DESIGN_v3.md §4: ID =
// sha256(JCS({..., antigens, content, ...}))), so this package is
// deliberately NOT wired into engine.Ingest. Extraction must happen once, by
// the caller, before a Bead's ID is computed (bead.WithID /
// bead.Canonicalize) — typically the FHIR ingest pipeline (bench/ingest) or
// MCP create_bead. Calling Extract again after a Bead has already been
// ingested and re-assigning its Antigens would change the Bead's content
// hash, which is exactly what the append-only, content-addressed design
// forbids. engine.Ingest itself is untouched by this package on purpose.
//
// # No LLM in the ingest path (R4.4)
//
// Extract only ever performs deterministic, rule-based derivation: direct
// FHIR coding-system-URI prefixing (snomed:/loinc:/rxnorm:), static
// dictionary lookups (rxnorm: -> atc:/risk:/organ:), and beadType-based
// temporal: tagging. No LLM call is made here, and none may be added to this
// package or to any code that runs before a Bead's ID is computed. This is
// load-bearing, not a style preference: Bead IDs are content hashes used as
// join keys across an append-only Merkle DAG, so extraction must be
// bit-for-bit reproducible for the same input on every machine, at every
// time, forever — an LLM call is neither deterministic nor reproducible.
// specs/DESIGN_v3.md §6 permits LLM assistance only as an OFFLINE aid for
// drafting new dictionary.json entries, which a human then reviews and
// commits; the dictionary itself, once committed, is applied purely
// deterministically by Extract.
package antigen
