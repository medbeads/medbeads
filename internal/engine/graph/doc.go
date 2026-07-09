// Package graph handles in-memory expansion of a patient bundle: loading a
// whole patient's Beads with one sequential Pod scan (LoadBundle), BFS
// traversal over the resulting in-memory adjacency lists (Ancestors /
// Descendants / Siblings), a shallow cross-patient chain via a recursive
// bead_edges CTE (ChainAcrossPatients), and token-budgeted context bundle
// assembly for agent retrieval (BuildContext). See specs/DESIGN_v3.md §3, §6,
// §8 and docs/requirements.md R4.3.
//
// graph deliberately depends only on package pod and package index (and
// package bead for the Bead type itself), not on package engine: per
// specs/DESIGN_v3.md §2, engine/graph is a sibling of engine/pod and
// engine/index under internal/engine/, and engine is expected to wire graph
// on top of its own *pod.Store / *index.DB in a later unit.
package graph
