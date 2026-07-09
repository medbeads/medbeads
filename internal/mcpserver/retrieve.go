package mcpserver

import (
	"context"
	"fmt"
	"sort"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/medbeads/medbeads/internal/engine/bead"
	"github.com/medbeads/medbeads/internal/engine/clearance"
	"github.com/medbeads/medbeads/internal/engine/graph"
)

// defaultTokenBudget/defaultChainDepth are retrieve's own defaults when the
// caller omits token_budget/chain_depth: specs/DESIGN_v3.md §8 does not
// mandate a specific number, so these pick values consistent with the L0/L1/
// L2 size estimates documented there (a handful of L0 anchors plus a good
// deal of L1/L2 context comfortably fits in ~4000 tokens; chain_depth=3
// reaches grandparents, matching a typical encounter -> observation/
// medication -> patient_registration chain depth in the FHIR ingest shape).
const (
	defaultTokenBudget = 4000
	defaultChainDepth  = 3
)

// retrieveIn is the MCP-facing shape of R6.2's unified retrieve tool:
// retrieve(query, patient_id, antigens, types, date_range, semantic,
// chain_depth, token_budget).
type retrieveIn struct {
	Query       string       `json:"query,omitempty" jsonschema:"FTS5 trigram anchor query text"`
	PatientID   string       `json:"patient_id,omitempty" jsonschema:"restrict the anchor search to this patient (sha256: prefix optional)"`
	Antigens    []string     `json:"antigens,omitempty" jsonschema:"require anchors to carry at least one of these antigens"`
	Types       []string     `json:"types,omitempty" jsonschema:"restrict anchors to these Bead types"`
	DateRange   *dateRangeIn `json:"date_range,omitempty" jsonschema:"restrict anchors to this [from, to) timestamp window"`
	Semantic    bool         `json:"semantic,omitempty" jsonschema:"L2 vector search (NOT YET AVAILABLE in this build; true is an error)"`
	ChainDepth  int          `json:"chain_depth,omitempty" jsonschema:"ancestor/descendant BFS depth from each anchor (default 3)"`
	TokenBudget int          `json:"token_budget,omitempty" jsonschema:"greedy packing budget in estimated tokens (default 4000)"`
}

type dateRangeIn struct {
	From string `json:"from,omitempty" jsonschema:"inclusive RFC3339 lower bound"`
	To   string `json:"to,omitempty" jsonschema:"exclusive RFC3339 upper bound"`
}

// provenanceView augments graph's own ContextItem provenance with the
// search-layer provenance DESIGN §8 asks retrieve specifically to carry
// (FTS score / matched_antigen) that graph.BuildContext itself does not
// model (graph/context.go's own doc comment: "FTS score / vector
// similarity / matched_antigen provenance is the search layer's
// responsibility... and is deliberately not modeled here").
type provenanceView struct {
	contextItemView
	MatchedAntigens []string `json:"matched_antigens,omitempty"`
}

type retrieveOut struct {
	AnchorIDs     []string          `json:"anchor_ids"`
	Items         []provenanceView  `json:"items"`
	TruncatedRefs []contextItemView `json:"truncated_refs"`
	BudgetTokens  int               `json:"budget_tokens"`
	UsedTokens    int               `json:"used_tokens"`
}

// retrieve implements R6.2 / DESIGN §8: one round trip from a query (plus
// optional structured filters) to a token-budgeted, provenance-annotated
// context bundle. Anchor selection is FTS5 (index.Search) intersected with
// the structured filters (antigens/types/date_range), scoped to patient_id
// if given; every anchor must resolve to the same patient_root (retrieve
// operates on one patient bundle per call, per DESIGN §8's "engine の
// /context-bundle 1往復"), which — absent an explicit patient_id — is
// resolved from the first anchor and every other anchor outside that
// patient is dropped (documented via TruncatedRefs would overstate it: they
// are simply not eligible anchors for this call, since Bundle can only ever
// hold one patient's Pod contents — see graph.LoadBundle's doc comment).
func (s *Server) retrieve(_ context.Context, _ *mcp.CallToolRequest, in retrieveIn) (*mcp.CallToolResult, retrieveOut, error) {
	if in.Semantic {
		res, jerr := toolError("retrieve", fmt.Errorf(
			"semantic=true is not yet available: L2 semantic search (sqlite-vec + embedder) is out of scope for this build (see docs/requirements.md R4.2)"))
		return res, retrieveOut{}, jerr
	}
	if in.Query == "" && len(in.Antigens) == 0 {
		res, jerr := toolError("retrieve", fmt.Errorf("at least one of query or antigens must be set"))
		return res, retrieveOut{}, jerr
	}

	chainDepth := in.ChainDepth
	if chainDepth <= 0 {
		chainDepth = defaultChainDepth
	}
	tokenBudget := in.TokenBudget
	if tokenBudget <= 0 {
		tokenBudget = defaultTokenBudget
	}

	var patientRoot string
	if in.PatientID != "" {
		root, err := bead.ParseID(in.PatientID)
		if err != nil {
			res, jerr := toolError("retrieve: parse patient_id", err)
			return res, retrieveOut{}, jerr
		}
		patientRoot = root
	}

	anchors, provenance, err := s.retrieveAnchors(in, patientRoot)
	if err != nil {
		res, jerr := toolError("retrieve: anchor search", err)
		return res, retrieveOut{}, jerr
	}
	if len(anchors) == 0 {
		return nil, retrieveOut{BudgetTokens: tokenBudget}, nil
	}

	// Every anchor must share one patient_root: retrieve loads exactly one
	// graph.Bundle per call (DESIGN §8's single round trip). The first
	// anchor (in the deterministic order retrieveAnchors already applied)
	// decides which patient; anchors resolving to any other patient_root are
	// simply not usable in this bundle and are dropped from the anchor set
	// entirely, not truncated (they were never loaded).
	root := anchors[0].patientRoot
	var scopedIDs []string
	scopedProvenance := make(map[string][]string, len(provenance))
	for _, a := range anchors {
		if a.patientRoot != root {
			continue
		}
		scopedIDs = append(scopedIDs, a.id)
		scopedProvenance[a.id] = provenance[a.id]
	}

	bd, err := graph.LoadBundle(s.store, root)
	if err != nil {
		res, jerr := toolError("retrieve: load bundle", err)
		return res, retrieveOut{}, jerr
	}
	if err := loadExplicitSiblingEdges(s, bd); err != nil {
		res, jerr := toolError("retrieve: load sibling edges", err)
		return res, retrieveOut{}, jerr
	}

	bundle := graph.BuildContext(bd, scopedIDs, tokenBudget, chainDepth, chainDepth)

	items, truncated, err := s.filterContextBundle(bundle, scopedProvenance)
	if err != nil {
		res, jerr := toolError("retrieve: filter", err)
		return res, retrieveOut{}, jerr
	}

	// AnchorIDs must reflect the same clearance decision as Items/
	// TruncatedRefs: an anchor the viewer may not access is dropped here too
	// (not just masked in Items), so its ID — which is itself information
	// ("a Bead matching this query exists") — never appears in the response
	// at all. scopedIDs is pre-filter; accessibleAnchorIDs re-runs
	// clearance.FilterByAccess over exactly that set rather than reusing
	// filterContextBundle's per-tier results, since an anchor at tier 0 that
	// BuildContext had to truncate to TruncatedRefs (rather than Items) is
	// still a real anchor for AnchorIDs' purposes — TruncatedRefs already
	// carries its own accessible() filtering (filterRefs), so this is a
	// third, independent pass specifically over the anchor set.
	anchorViews, err := s.accessibleAnchorIDs(scopedIDs)
	if err != nil {
		res, jerr := toolError("retrieve: filter anchors", err)
		return res, retrieveOut{}, jerr
	}

	return nil, retrieveOut{
		AnchorIDs:     anchorViews,
		Items:         items,
		TruncatedRefs: truncated,
		BudgetTokens:  bundle.BudgetTokens,
		UsedTokens:    bundle.UsedTokens,
	}, nil
}

// anchorRef is one candidate anchor Bead surfaced by retrieveAnchors, tagged
// with the patient_root it belongs to (so retrieve can scope the eventual
// Bundle to a single patient) and which antigen(s) — if any — matched the
// caller's Antigens filter (retrieve's own provenance beyond what
// graph.ContextItem tracks, per DESIGN §8).
type anchorRef struct {
	id          string
	patientRoot string
}

// retrieveAnchors resolves retrieve's anchor set: an FTS query (if given)
// intersected with antigens/types/date_range filters, all scoped to
// patientRoot if non-empty. If Query is empty but Antigens is set, anchors
// come from the antigen inverted index alone (bead_antigens), matching
// DESIGN §8's expectation that antigens can drive anchor selection on their
// own. Results are deduplicated and returned in a deterministic order
// (timestamp, then ID) mirroring graph.BuildContext's own tie-breaking.
func (s *Server) retrieveAnchors(in retrieveIn, patientRoot string) ([]anchorRef, map[string][]string, error) {
	db := s.eng.Index()

	type candidateInfo struct {
		id, patientRoot, typ, timestamp string
	}
	var candidates []candidateInfo

	if in.Query != "" {
		hits, err := db.Search(in.Query, 0)
		if err != nil {
			return nil, nil, fmt.Errorf("fts search: %w", err)
		}
		for _, h := range hits {
			candidates = append(candidates, candidateInfo{id: h.BeadID, patientRoot: h.PatientRoot, typ: h.Type, timestamp: h.Timestamp})
		}
	} else {
		// Antigens-only anchor selection: union every Bead carrying any one
		// of the requested antigens (deduplicated below).
		seen := make(map[string]bool)
		for _, ag := range in.Antigens {
			rows, err := db.SQLDB().Query(`
				SELECT b.id, COALESCE(b.patient_root, ''), b.type, b.timestamp
				FROM bead_antigens ba JOIN beads b ON b.id = ba.bead_id
				WHERE ba.antigen = ?`, ag)
			if err != nil {
				return nil, nil, fmt.Errorf("antigen anchor search %s: %w", ag, err)
			}
			for rows.Next() {
				var c candidateInfo
				if err := rows.Scan(&c.id, &c.patientRoot, &c.typ, &c.timestamp); err != nil {
					rows.Close()
					return nil, nil, fmt.Errorf("antigen anchor search %s: scan: %w", ag, err)
				}
				if !seen[c.id] {
					seen[c.id] = true
					candidates = append(candidates, c)
				}
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return nil, nil, fmt.Errorf("antigen anchor search %s: %w", ag, err)
			}
			rows.Close()
		}
	}

	typeSet := make(map[string]bool, len(in.Types))
	for _, t := range in.Types {
		typeSet[t] = true
	}

	provenance := make(map[string][]string)
	var out []anchorRef
	seenID := make(map[string]bool)
	for _, c := range candidates {
		if seenID[c.id] {
			continue
		}
		if patientRoot != "" && c.patientRoot != patientRoot {
			continue
		}
		if len(typeSet) > 0 && !typeSet[c.typ] {
			continue
		}
		if in.DateRange != nil {
			if in.DateRange.From != "" && c.timestamp < in.DateRange.From {
				continue
			}
			if in.DateRange.To != "" && c.timestamp >= in.DateRange.To {
				continue
			}
		}
		if len(in.Antigens) > 0 && in.Query != "" {
			// Query drove anchor selection but antigens were also given:
			// require the anchor to carry at least one requested antigen too
			// (an AND, not an OR, when both filters are present alongside a
			// query — antigens-only selection above already is the OR case).
			matched, err := matchingAntigens(db, c.id, in.Antigens)
			if err != nil {
				return nil, nil, err
			}
			if len(matched) == 0 {
				continue
			}
			provenance[c.id] = matched
		} else if len(in.Antigens) > 0 {
			matched, err := matchingAntigens(db, c.id, in.Antigens)
			if err != nil {
				return nil, nil, err
			}
			provenance[c.id] = matched
		}

		seenID[c.id] = true
		out = append(out, anchorRef{id: c.id, patientRoot: c.patientRoot})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out, provenance, nil
}

// matchingAntigens returns the subset of want that beadID actually carries
// (bead_antigens), for retrieve's matched_antigen provenance.
func matchingAntigens(db interface {
	GetAntigens(string) ([]string, error)
}, beadID string, want []string) ([]string, error) {
	have, err := db.GetAntigens(beadID)
	if err != nil {
		return nil, fmt.Errorf("get antigens %s: %w", beadID, err)
	}
	haveSet := make(map[string]bool, len(have))
	for _, a := range have {
		haveSet[a] = true
	}
	var matched []string
	for _, w := range want {
		if haveSet[w] {
			matched = append(matched, w)
		}
	}
	return matched, nil
}

// filterContextBundle applies clearance.FilterByAccess to a graph.
// ContextBundle's Items + TruncatedRefs (resolving each item's full Bead via
// engine.GetBead so FilterByAccess has real Clearance/embedded-rule data to
// evaluate — graph.ContextItem itself only carries rendered text, not the
// Bead), converting the surviving items to provenanceView/contextItemView.
// An item the viewer may not access is dropped entirely from the response
// (masking it in place, as FilterByAccess would for a get_bead-style single
// Bead, would leak that a restricted Bead exists in this patient's context
// at all — retrieve's job is to hand an agent a usable, budget-fit context,
// not a a redacted placeholder it cannot act on).
func (s *Server) filterContextBundle(bundle graph.ContextBundle, provenance map[string][]string) ([]provenanceView, []contextItemView, error) {
	items, err := s.filterItems(bundle.Items, provenance)
	if err != nil {
		return nil, nil, err
	}
	truncated, err := s.filterRefs(bundle.TruncatedRefs)
	if err != nil {
		return nil, nil, err
	}
	return items, truncated, nil
}

func (s *Server) filterItems(items []graph.ContextItem, provenance map[string][]string) ([]provenanceView, error) {
	if len(items) == 0 {
		return nil, nil
	}
	beads := make([]bead.Bead, len(items))
	for i, item := range items {
		b, err := s.eng.GetBead(item.ID)
		if err != nil {
			return nil, fmt.Errorf("get bead %s: %w", item.ID, err)
		}
		beads[i] = b
	}
	filtered, err := clearance.FilterByAccess(s.eng.Index(), beads, s.viewerRoles())
	if err != nil {
		return nil, fmt.Errorf("filter: %w", err)
	}

	var out []provenanceView
	for i, item := range items {
		if !accessible(filtered[i]) {
			continue
		}
		out = append(out, provenanceView{
			contextItemView: newContextItemView(item),
			MatchedAntigens: provenance[item.ID],
		})
	}
	return out, nil
}

func (s *Server) filterRefs(refs []graph.ContextItem) ([]contextItemView, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	beads := make([]bead.Bead, len(refs))
	for i, item := range refs {
		b, err := s.eng.GetBead(item.ID)
		if err != nil {
			return nil, fmt.Errorf("get bead %s: %w", item.ID, err)
		}
		beads[i] = b
	}
	filtered, err := clearance.FilterByAccess(s.eng.Index(), beads, s.viewerRoles())
	if err != nil {
		return nil, fmt.Errorf("filter: %w", err)
	}

	var out []contextItemView
	for i, item := range refs {
		if !accessible(filtered[i]) {
			continue
		}
		out = append(out, newContextItemView(item))
	}
	return out, nil
}

// accessibleAnchorIDs resolves and clearance-filters ids (retrieve's
// post-patient-scoping anchor set), returning the sha256:-prefixed IDs of
// only the anchors this session's role may access — see retrieve's own doc
// comment at its call site for why AnchorIDs needs its own filtering pass
// independent of filterItems/filterRefs.
func (s *Server) accessibleAnchorIDs(ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	beads := make([]bead.Bead, len(ids))
	for i, id := range ids {
		b, err := s.eng.GetBead(id)
		if err != nil {
			return nil, fmt.Errorf("get bead %s: %w", id, err)
		}
		beads[i] = b
	}
	filtered, err := clearance.FilterByAccess(s.eng.Index(), beads, s.viewerRoles())
	if err != nil {
		return nil, fmt.Errorf("filter: %w", err)
	}

	var out []string
	for _, b := range filtered {
		if !accessible(b) {
			continue
		}
		out = append(out, bead.FormatID(b.ID))
	}
	return out, nil
}
