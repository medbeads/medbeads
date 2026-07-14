package mcpserver

import (
	"fmt"
	"sort"

	"github.com/medbeads/medbeads/internal/engine/bead"
	"github.com/medbeads/medbeads/internal/engine/clearance"
	"github.com/medbeads/medbeads/internal/engine/graph"
	"github.com/medbeads/medbeads/internal/engine/index"
)

// accessiblePatientLink is a patient-scoped projection edge after both
// endpoints have passed record-state normalization and clearance. BeadA/B
// may therefore name current amendment IDs rather than the stale IDs stored
// in the projection row.
type accessiblePatientLink struct {
	row   index.PatientLinkRow
	beadA string
	beadB string
}

// verticalCandidateIDs returns every Bead the ordinary anchor/ancestor/
// descendant traversal can consider, independent of token packing. Links
// attached to a vertically-relevant L2 reference must still be discoverable;
// using only already-packed Items would make expansion depend on token budget.
func verticalCandidateIDs(bd *graph.Bundle, anchors []string, depth int) []string {
	seen := make(map[string]bool)
	for _, id := range anchors {
		seen[id] = true
		for _, b := range bd.Ancestors(id, depth) {
			seen[b.ID] = true
		}
		for _, b := range bd.Descendants(id, depth) {
			seen[b.ID] = true
		}
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// loadAccessiblePatientLinks performs one patient-scoped clinical_links
// query, one batched status lookup and one batched clearance decision. This
// replaces retrieveClinicalLinks' former one-GetClinicalLinks-per-item path
// and makes the same approved edge set feed both expansion and the sidecar.
func (s *Server) loadAccessiblePatientLinks(
	bd *graph.Bundle,
	patientRoot string,
	seedIDs []string,
	includeUnattested bool,
) ([]accessiblePatientLink, []string, error) {
	rows, err := s.eng.Index().GetClinicalLinksForPatient(patientRoot)
	if err != nil {
		return nil, nil, fmt.Errorf("get patient clinical links: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil, nil
	}

	rawIDs := make([]string, 0, len(rows)*2+len(seedIDs))
	seenRaw := make(map[string]bool, cap(rawIDs))
	addRaw := func(id string) {
		if id != "" && !seenRaw[id] {
			seenRaw[id] = true
			rawIDs = append(rawIDs, id)
		}
	}
	for _, id := range seedIDs {
		addRaw(id)
	}
	for _, r := range rows {
		addRaw(r.BeadA)
		addRaw(r.BeadB)
	}

	statuses, err := s.resolveBeadStatuses(rawIDs)
	if err != nil {
		return nil, nil, fmt.Errorf("status normalize patient clinical links: %w", err)
	}
	type endpointDecision struct {
		id   string
		drop bool
	}
	decisions := make(map[string]endpointDecision, len(rawIDs))
	resolvedIDs := make([]string, 0, len(rawIDs))
	seenResolved := make(map[string]bool, len(rawIDs))
	for _, id := range rawIDs {
		st, ok := statuses[id]
		decision := resolveStatus(id, st, ok, includeUnattested)
		decisions[id] = endpointDecision{id: decision.resolvedID, drop: decision.drop}
		if !decision.drop && !seenResolved[decision.resolvedID] {
			seenResolved[decision.resolvedID] = true
			resolvedIDs = append(resolvedIDs, decision.resolvedID)
		}
	}

	beads := make([]bead.Bead, 0, len(resolvedIDs))
	for _, id := range resolvedIDs {
		b, ok := bd.Get(id)
		if !ok {
			return nil, nil, fmt.Errorf("clinical link endpoint %s is outside patient bundle %s", id, patientRoot)
		}
		beads = append(beads, b)
	}
	filtered, err := clearance.FilterByAccess(s.eng.Index(), beads, s.viewerRoles())
	if err != nil {
		return nil, nil, fmt.Errorf("filter patient clinical links: %w", err)
	}
	allowed := make(map[string]bool, len(beads))
	for i, b := range beads {
		allowed[b.ID] = accessible(filtered[i])
	}

	normalizedSeeds := make([]string, 0, len(seedIDs))
	seenSeed := make(map[string]bool, len(seedIDs))
	for _, rawID := range seedIDs {
		d := decisions[rawID]
		if d.drop || !allowed[d.id] || seenSeed[d.id] {
			continue
		}
		seenSeed[d.id] = true
		normalizedSeeds = append(normalizedSeeds, d.id)
	}
	sort.Strings(normalizedSeeds)

	links := make([]accessiblePatientLink, 0, len(rows))
	for _, r := range rows {
		a := decisions[r.BeadA]
		b := decisions[r.BeadB]
		if a.drop || b.drop || a.id == b.id || !allowed[a.id] || !allowed[b.id] {
			continue
		}
		links = append(links, accessiblePatientLink{row: r, beadA: a.id, beadB: b.id})
	}
	sort.Slice(links, func(i, j int) bool { return clinicalLinkLess(links[i], links[j]) })
	return links, normalizedSeeds, nil
}

func severityPriority(severity string) int {
	switch severity {
	case "critical":
		return 0
	case "alert":
		return 1
	case "warning":
		return 2
	case "info":
		return 3
	default:
		return 4
	}
}

func evidencePriority(basis string) int {
	switch basis {
	case "guideline":
		return 0
	case "curated_knowledge":
		return 1
	case "cooccurrence":
		return 2
	default:
		return 3
	}
}

// clinicalLinkLess is the deterministic clinical priority used before the
// token packer: severe and knowledge-backed links first, then newer links,
// with content-derived link_id as the final stable tie-breaker.
func clinicalLinkLess(a, b accessiblePatientLink) bool {
	if ar, br := severityPriority(a.row.Severity), severityPriority(b.row.Severity); ar != br {
		return ar < br
	}
	if ar, br := evidencePriority(a.row.EvidenceBasis), evidencePriority(b.row.EvidenceBasis); ar != br {
		return ar < br
	}
	if a.row.CreatedAt != b.row.CreatedAt {
		return a.row.CreatedAt > b.row.CreatedAt
	}
	return a.row.LinkID < b.row.LinkID
}

type linkProposal struct {
	id    string
	viaID string
	depth int
	link  accessiblePatientLink
}

// expandClinicalLinks runs bounded patient-local BFS. It explores the full
// requested depth to report the true candidate count, then applies the
// max-linked-beads policy cap; token-budget truncation happens later and is
// reported separately through Items/TruncatedRefs.
func expandClinicalLinks(seedIDs []string, links []accessiblePatientLink, depth, maxBeads int) ([]graph.LinkedAnchor, int) {
	if len(seedIDs) == 0 || len(links) == 0 || depth <= 0 || maxBeads <= 0 {
		return nil, 0
	}
	adj := make(map[string][]accessiblePatientLink)
	for _, l := range links {
		adj[l.beadA] = append(adj[l.beadA], l)
		adj[l.beadB] = append(adj[l.beadB], l)
	}

	seen := make(map[string]bool, len(seedIDs))
	frontier := append([]string(nil), seedIDs...)
	for _, id := range seedIDs {
		seen[id] = true
	}
	var discovered []graph.LinkedAnchor
	for hop := 1; hop <= depth && len(frontier) > 0; hop++ {
		var proposals []linkProposal
		for _, from := range frontier {
			for _, l := range adj[from] {
				other := l.beadA
				if other == from {
					other = l.beadB
				}
				if seen[other] {
					continue
				}
				proposals = append(proposals, linkProposal{id: other, viaID: l.row.LinkID, depth: hop, link: l})
			}
		}
		sort.Slice(proposals, func(i, j int) bool {
			if clinicalLinkLess(proposals[i].link, proposals[j].link) {
				return true
			}
			if clinicalLinkLess(proposals[j].link, proposals[i].link) {
				return false
			}
			return proposals[i].id < proposals[j].id
		})

		next := make([]string, 0, len(proposals))
		for _, p := range proposals {
			if seen[p.id] {
				continue
			}
			seen[p.id] = true
			next = append(next, p.id)
			discovered = append(discovered, graph.LinkedAnchor{ID: p.id, ViaLinkID: p.viaID, Depth: p.depth})
		}
		frontier = next
	}

	candidateCount := len(discovered)
	if len(discovered) > maxBeads {
		discovered = discovered[:maxBeads]
	}
	return discovered, candidateCount
}

// retrieveClinicalLinksFromPatient builds the sidecar from the same
// status/clearance-normalized edge snapshot used for expansion. A link is
// returned when at least one endpoint made it into Items; if both did, the
// earlier context item is the attributed BeadID.
func retrieveClinicalLinksFromPatient(items []provenanceView, links []accessiblePatientLink) ([]retrievedLinkView, error) {
	itemRank := make(map[string]int, len(items))
	for i, item := range items {
		id, err := bead.ParseID(item.ID)
		if err != nil {
			return nil, fmt.Errorf("parse item id %s: %w", item.ID, err)
		}
		itemRank[id] = i
	}

	var out []retrievedLinkView
	for _, l := range links {
		rankA, hasA := itemRank[l.beadA]
		rankB, hasB := itemRank[l.beadB]
		if !hasA && !hasB {
			continue
		}
		attached, other := l.beadA, l.beadB
		if !hasA || (hasB && rankB < rankA) {
			attached, other = l.beadB, l.beadA
		}
		evidenceIDs, err := decodeEvidenceBeadIDs(l.row.EvidenceBeadIDs)
		if err != nil {
			return nil, fmt.Errorf("decode evidence_bead_ids for %s: %w", l.row.LinkID, err)
		}
		out = append(out, retrievedLinkView{
			BeadID: bead.FormatID(attached),
			clinicalLinkView: clinicalLinkView{
				LinkID:          bead.FormatID(l.row.LinkID),
				OtherBeadID:     bead.FormatID(other),
				Relation:        l.row.Relation,
				MatchedTag:      l.row.MatchedTag,
				Severity:        l.row.Severity,
				EvidenceBasis:   l.row.EvidenceBasis,
				EvidenceBeadIDs: evidenceIDs,
				RuleID:          l.row.RuleID,
				RuleVersion:     l.row.RuleVersion,
				ProjectionRunID: l.row.ProjectionRunID,
				CreatedAt:       l.row.CreatedAt,
			},
		})
	}
	return out, nil
}
