// Package projector implements the U3b link projector
// (specs/U3_link_projector.md's "U3b" section, specs/U2_projection_schema.md):
// it writes clinical_links rows directly from bead_tags cooccurrence,
// without minting any sibling_link Bead, and exposes Reproject as the
// deterministic, patient_root-batched entry point that (re)builds every
// patient's clinical_links from the already-indexed bead_tags/beads and
// flips projection_manifest's single active run.
//
// This is a *new*, separate mechanism from package apc's Scanner
// (bead_apc_scan / scan_generation / sibling_link Beads / engine.Ingest of a
// link Bead): per the U3 peer-reviewed design, U3b does not touch any of
// those tables or types, and does not delete package apc — U3c (a later
// unit) is what cuts the *read* side over to clinical_links and removes the
// old scanner. The two mechanisms coexist during U3b/U3c and must not
// interfere with each other (they write disjoint tables: bead_apc_scan/
// sibling_pairs/sibling_link Beads vs. clinical_links/projection_manifest).
//
// projector depends only on package bead, package index (direct SQL access
// for the tag/pair queries its own read API does not expose) and a narrow
// reader interface for engine.Engine's ListPatientBeads/GetBead — the same
// "does not import engine" convention package apc and package graph already
// document for themselves (see apc/scanner.go's ingester interface,
// graph/doc.go): it is engine's/cmd's job to wire a projector on top of a
// live *engine.Engine, not this package's job to import engine.
package projector
