package graph

import (
	"fmt"

	"github.com/medbeads/medbeads/internal/engine/index"
)

// maxChainDepthCeiling is a hard upper bound on the depth argument callers
// may request from ChainAcrossPatients, independent of whatever value they
// pass in. specs/DESIGN_v3.md §3 calls this "患者横断の浅いチェーン" (a
// *shallow* chain, e.g. a _shared drug_master revision referenced by more
// than one patient) — a recursive CTE with no depth ceiling at all could walk
// arbitrarily far across the whole DAG, which is exactly the unbounded-scan
// cost patient-scoped Bundle+BFS exists to avoid. A caller-supplied depth
// above this ceiling is clamped rather than rejected, so a slightly-too-high
// request degrades to "as deep as we allow" instead of erroring.
const maxChainDepthCeiling = 20

// ChainRef is one Bead reached by ChainAcrossPatients: its ID, patient_root
// (so a caller can tell which patient — or the shared Pod, "" — it belongs
// to), and how many parent-hops it is from the anchor.
type ChainRef struct {
	ID          string
	PatientRoot string
	Depth       int
}

// ChainAcrossPatients walks id's ancestor chain via bead_edges using a
// recursive CTE — the "患者横断の浅いチェーン... 再帰 CTE" escape hatch
// specs/DESIGN_v3.md §3 and §6 (L3 Chain) call for when a chain needs to
// cross patient boundaries (e.g. a prescription Bead whose parent is a
// _shared drug_master revision bead also referenced by other patients),
// where Bundle+BFS cannot help because Bundle only ever holds one patient's
// Pod contents. depth is clamped to [0, maxChainDepthCeiling] — the "深さ上
// 限必須" requirement — since bead_edges has no patient partition to bound
// the walk the way a single Pod scan naturally does. depth=0 returns just
// id itself (if indexed); depth=1 returns id and its direct parents; and so
// on, mirroring Ancestors' depth semantics.
func ChainAcrossPatients(db *index.DB, id string, depth int) ([]ChainRef, error) {
	if depth < 0 {
		depth = 0
	}
	if depth > maxChainDepthCeiling {
		depth = maxChainDepthCeiling
	}

	rows, err := db.SQLDB().Query(`
		WITH RECURSIVE chain(id, patient_root, depth) AS (
			SELECT b.id, COALESCE(b.patient_root, ''), 0
			FROM beads b
			WHERE b.id = ?

			UNION

			SELECT p.id, COALESCE(p.patient_root, ''), c.depth + 1
			FROM chain c
			JOIN bead_edges e ON e.child_id = c.id AND e.edge_type = 'parent'
			JOIN beads p ON p.id = e.parent_id
			WHERE c.depth < ?
		)
		SELECT id, patient_root, MIN(depth)
		FROM chain
		GROUP BY id
		ORDER BY MIN(depth), id`, id, depth)
	if err != nil {
		return nil, fmt.Errorf("graph: chain across patients %s: %w", id, err)
	}
	defer rows.Close()

	var out []ChainRef
	for rows.Next() {
		var ref ChainRef
		if err := rows.Scan(&ref.ID, &ref.PatientRoot, &ref.Depth); err != nil {
			return nil, fmt.Errorf("graph: chain across patients %s: scan: %w", id, err)
		}
		out = append(out, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("graph: chain across patients %s: %w", id, err)
	}
	return out, nil
}
