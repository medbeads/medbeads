package graph

import "github.com/medbeads/medbeads/internal/engine/bead"

// Ancestors walks up from id via the bundle's parent adjacency
// (breadth-first, by level) and returns id itself plus every Bead reached
// within maxDepth parent-hops, in level order (id first). This is Bundle's
// map-only replacement for v2.2.0's core/store.GetContext (a SQL-free BFS,
// per specs/DESIGN_v3.md §6's "siblings 展開・descendants が map 操作"
// principle extended to ancestors too): maxDepth=0 returns only id;
// maxDepth=1 returns id and its direct parents; and so on. A Bead ID with no
// entry in the bundle (not id itself, and not reachable from it) is simply
// absent from parents, so BFS from an unknown id yields just that id if it
// exists in the bundle, or nothing if it does not.
func (bd *Bundle) Ancestors(id string, maxDepth int) []bead.Bead {
	return bd.walk(id, maxDepth, bd.parents)
}

// Descendants walks down from id via the bundle's child adjacency
// (breadth-first, by level) and returns id itself plus every Bead reached
// within maxDepth child-hops, in level order (id first). This is Bundle's
// map-only replacement for v2.2.0's core/store.GetBeadsByParent.
func (bd *Bundle) Descendants(id string, maxDepth int) []bead.Bead {
	return bd.walk(id, maxDepth, bd.children)
}

// walk is the shared level-by-level BFS behind Ancestors/Descendants: adj
// selects which adjacency (bd.parents or bd.children) to follow. It mirrors
// v2.2.0's GetContext/GetBeadsByParent loop shape exactly (queue seeded with
// id, "for len(queue) > 0 && currentDepth <= maxDepth", one level per
// iteration) so that depth semantics match: maxDepth=1 reaches id's direct
// neighbors but not their neighbors in turn.
func (bd *Bundle) walk(id string, maxDepth int, adj map[string][]string) []bead.Bead {
	if _, ok := bd.beads[id]; !ok {
		return nil
	}

	visited := map[string]bool{id: true}
	queue := []string{id}
	var out []bead.Bead

	currentDepth := 0
	for len(queue) > 0 && currentDepth <= maxDepth {
		levelSize := len(queue)
		for i := 0; i < levelSize; i++ {
			curID := queue[0]
			queue = queue[1:]

			// A neighbor ID may reference a Bead outside this bundle (e.g. a
			// cross-patient parent living in _shared.pod) — walk only stays
			// within Beads this Bundle actually holds, per LoadBundle's
			// single-Pod scope; ChainAcrossPatients is the SQL escape hatch
			// for that case.
			b, ok := bd.beads[curID]
			if !ok {
				continue
			}
			out = append(out, b)

			for _, next := range adj[curID] {
				if !visited[next] {
					visited[next] = true
					queue = append(queue, next)
				}
			}
		}
		currentDepth++
	}
	return out
}

// Siblings returns every Bead related to id "horizontally": implicit
// siblings (Beads sharing at least one direct parent with id, excluding id
// itself — v2.2.0's dynamic GetSiblings semantics per
// specs/MEDBEADS_SIBLING_SPEC.md §2.6) plus explicit siblings (Beads linked
// via a bidirectional edge_type='sibling' bead_edges row, injected into this
// Bundle via AddSiblingEdge — see specs/MEDBEADS_SIBLING_SPEC.md §5). The
// return value deduplicates a Bead reachable through both an implicit and an
// explicit sibling edge.
func (bd *Bundle) Siblings(id string) []bead.Bead {
	if _, ok := bd.beads[id]; !ok {
		return nil
	}

	seen := map[string]bool{id: true}
	var out []bead.Bead
	add := func(candidateID string) {
		if seen[candidateID] {
			return
		}
		b, ok := bd.beads[candidateID]
		if !ok {
			return
		}
		seen[candidateID] = true
		out = append(out, b)
	}

	for _, parentID := range bd.parents[id] {
		for _, childID := range bd.children[parentID] {
			add(childID)
		}
	}
	for _, siblingID := range bd.siblings[id] {
		add(siblingID)
	}

	return out
}
