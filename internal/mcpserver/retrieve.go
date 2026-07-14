package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/medbeads/medbeads/internal/engine/bead"
	"github.com/medbeads/medbeads/internal/engine/clearance"
	"github.com/medbeads/medbeads/internal/engine/graph"
	"github.com/medbeads/medbeads/internal/engine/index"
)

// semanticAnchorK bounds how many nearest-neighbor hits retrieve's semantic
// anchor pass pulls from index.DB.SemanticSearch before the usual
// types/date_range/antigens filters narrow them down further (mirroring
// index.Search's own "unbounded-ish, filters narrow it" shape — FTS's own
// db.Search(in.Query, 0) call a few lines below defaults internally to 50).
// 50 keeps this consistent with that FTS default rather than inventing a
// different magic number for the semantic path.
const semanticAnchorK = 50

// defaultTokenBudget/defaultChainDepth are retrieve's own defaults when the
// caller omits token_budget/chain_depth: specs/DESIGN_v3.md §8 does not
// mandate a specific number, so these pick values consistent with the L0/L1/
// L2 size estimates documented there (a handful of L0 anchors plus a good
// deal of L1/L2 context comfortably fits in ~4000 tokens; chain_depth=3
// reaches grandparents, matching a typical encounter -> observation/
// medication -> patient_registration chain depth in the FHIR ingest shape).
const (
	defaultTokenBudget    = 4000
	defaultChainDepth     = 3
	defaultLinkDepth      = 1
	defaultMaxLinkedBeads = 20
	maxLinkDepth          = 3
	maxLinkedBeadsLimit   = 100
)

// retrieveIn is the MCP-facing shape of R6.2's unified retrieve tool:
// retrieve(query, patient_id, tags, types, date_range, semantic, chain_depth,
// token_budget). U5b (specs/U5_api_retrieve.md) renamed antigens -> tags and
// include_siblings -> include_links as a clean cut (no deprecation alias: the
// old json keys are gone, not merely accepted-and-ignored).
type retrieveIn struct {
	Query       string       `json:"query,omitempty" jsonschema:"FTS5 trigram anchor query text"`
	PatientID   string       `json:"patient_id,omitempty" jsonschema:"restrict the anchor search to this patient (sha256: prefix optional)"`
	Tags        []string     `json:"tags,omitempty" jsonschema:"require anchors to carry at least one of these tags"`
	Types       []string     `json:"types,omitempty" jsonschema:"restrict anchors to these Bead types"`
	DateRange   *dateRangeIn `json:"date_range,omitempty" jsonschema:"restrict anchors to this [from, to) timestamp window"`
	Semantic    bool         `json:"semantic,omitempty" jsonschema:"also run L2 vector search (sqlite-vec) over query and merge hits into the anchor set; requires this server to have an embedder configured, or it is a tool-level error"`
	ChainDepth  int          `json:"chain_depth,omitempty" jsonschema:"ancestor/descendant BFS depth from each anchor (default 3)"`
	TokenBudget int          `json:"token_budget,omitempty" jsonschema:"greedy packing budget in estimated tokens (default 4000)"`
	// IncludeLinks enables both the clinical_links sidecar and bounded
	// projection-link expansion into Items. false is the explicit opt-out.
	IncludeLinks   *bool `json:"include_links,omitempty" jsonschema:"include clinical_links and expand linked Beads into the context bundle (default true); set false to disable both"`
	LinkDepth      int   `json:"link_depth,omitempty" jsonschema:"clinical_links BFS depth (default 1, maximum 3); include_links=false disables expansion"`
	MaxLinkedBeads int   `json:"max_linked_beads,omitempty" jsonschema:"maximum linked Beads promoted into context (default 20, maximum 100); truncation is reported"`
	// IncludeUnattested opts in to seeing an unattested Bead (one whose
	// required attestation is missing or was rejected — resolve.go's §2
	// attestation gate) in Items/TruncatedRefs/AnchorIDs, which are excluded
	// by default (specs/U5_api_retrieve.md's U5b status-normalization
	// rules). An unattested Bead surfaced this way still carries
	// not_for_clinical_action=true on its provenanceView (see
	// statusNormalizeItems) and is still clearance-filtered exactly like any
	// other item — this flag only overrides the status-based exclusion, not
	// the clearance decision.
	IncludeUnattested *bool `json:"include_unattested,omitempty" jsonschema:"also return unattested Beads (default false), each marked not_for_clinical_action=true; they are still clearance-filtered"`
}

// retrieveIncludeLinks resolves retrieveIn.IncludeLinks' documented default
// (true) — a *bool rather than bool specifically so "omitted" and
// "explicitly false" are distinguishable at the JSON layer (a plain bool
// field would make both cases decode to the Go zero value, false, making the
// R6.2 default impossible to express without breaking every existing caller
// that never sets the field). It gates both projection-link expansion and
// retrieveOut.ClinicalLinks, preserving one explicit opt-out.
func retrieveIncludeLinks(in retrieveIn) bool {
	if in.IncludeLinks == nil {
		return true
	}
	return *in.IncludeLinks
}

func retrieveLinkLimits(in retrieveIn) (depth, maxBeads int, err error) {
	if !retrieveIncludeLinks(in) {
		return 0, 0, nil
	}
	depth = in.LinkDepth
	if depth == 0 {
		depth = defaultLinkDepth
	}
	if depth < 0 || depth > maxLinkDepth {
		return 0, 0, fmt.Errorf("link_depth must be between 1 and %d", maxLinkDepth)
	}
	maxBeads = in.MaxLinkedBeads
	if maxBeads == 0 {
		maxBeads = defaultMaxLinkedBeads
	}
	if maxBeads < 1 || maxBeads > maxLinkedBeadsLimit {
		return 0, 0, fmt.Errorf("max_linked_beads must be between 1 and %d", maxLinkedBeadsLimit)
	}
	return depth, maxBeads, nil
}

// retrieveIncludeUnattested resolves retrieveIn.IncludeUnattested's documented
// default (false) — see IncludeUnattested's own doc comment for why this is a
// *bool.
func retrieveIncludeUnattested(in retrieveIn) bool {
	if in.IncludeUnattested == nil {
		return false
	}
	return *in.IncludeUnattested
}

type dateRangeIn struct {
	From string `json:"from,omitempty" jsonschema:"inclusive RFC3339 lower bound"`
	To   string `json:"to,omitempty" jsonschema:"exclusive RFC3339 upper bound"`
}

// anchorProvenance is retrieve's own search-layer provenance for one anchor
// Bead — matched_antigen and/or vector distance — kept separate from
// graph.ContextItem's traversal provenance (Provenance/Granularity) per
// graph/context.go's own doc comment ("FTS score / vector similarity /
// matched_antigen provenance is the search layer's responsibility... and is
// deliberately not modeled here").
type anchorProvenance struct {
	MatchedTags []string
	// VectorDistance is non-nil only for an anchor that matched via
	// SemanticSearch (retrieve(semantic=true) — R4.2): vec0's distance metric
	// (lower = more similar) between the embedded query and this Bead's
	// stored embedding. A FTS- or tag-only anchor has this nil (not zero — a
	// real distance of exactly 0.0 for an identical-vector match is a valid,
	// meaningful value that must round-trip distinctly from "no vector match
	// happened at all").
	VectorDistance *float64
}

// provenanceView augments graph's own ContextItem provenance with the
// search-layer provenance DESIGN §8 asks retrieve specifically to carry
// (FTS score / vector similarity / matched_tag) that graph.BuildContext
// itself does not model (see anchorProvenance's doc comment), plus U5b's
// status-normalization marker (NotForClinicalAction).
type provenanceView struct {
	contextItemView
	MatchedTags    []string `json:"matched_tags,omitempty"`
	VectorDistance *float64 `json:"vector_distance,omitempty"`
	// NotForClinicalAction is true only for a Bead surfaced because the
	// caller explicitly set include_unattested=true (retrieveIn's own doc
	// comment): bead_status.status=="unattested" means this Bead's required
	// attestation is missing or was rejected (resolve.go's §2 attestation
	// gate), so a consumer must not treat it as a validated clinical fact
	// even though it passed clearance. Omitted (false) for every other item.
	NotForClinicalAction bool `json:"not_for_clinical_action,omitempty"`
}

type retrieveOut struct {
	AnchorIDs     []string          `json:"anchor_ids"`
	Items         []provenanceView  `json:"items"`
	TruncatedRefs []contextItemView `json:"truncated_refs"`
	BudgetTokens  int               `json:"budget_tokens"`
	UsedTokens    int               `json:"used_tokens"`
	// ClinicalLinks surfaces the same patient-scoped, status/clearance-filtered
	// projection snapshot used to expand linked endpoints into Items. Keeping
	// the edge sidecar lets consumers audit relation, severity, evidence and
	// projection generation instead of trusting expanded content alone.
	ClinicalLinks []retrievedLinkView `json:"clinical_links,omitempty"`
	LinkExpansion *linkExpansionView  `json:"link_expansion,omitempty"`
}

// linkExpansionView makes policy truncation distinct from token truncation.
// ExpandedBeadIDs are context candidates; any that do not fit the token
// budget still appear in TruncatedRefs with provenance=clinical_link.
type linkExpansionView struct {
	RequestedDepth  int      `json:"requested_depth"`
	MaxLinkedBeads  int      `json:"max_linked_beads"`
	CandidateCount  int      `json:"candidate_count"`
	ExpandedBeadIDs []string `json:"expanded_bead_ids"`
	Truncated       bool     `json:"truncated"`
}

// retrievedLinkView is retrieve's own clinical_links provenance entry: which
// Bead in Items the link is attached to (BeadID), plus get_links' own
// clinicalLinkView shape for the link itself. Kept as its own named type
// (rather than reusing clinicalLinkView bare) so retrieve's response makes
// explicit which of the two linked Beads is "the one already in this
// context bundle" versus OtherBeadID ("the one the link points at" — which
// may or may not itself be in Items, exactly as get_links never guaranteed
// the other endpoint itself is in the caller's already-loaded set).
type retrievedLinkView struct {
	BeadID string `json:"bead_id"`
	clinicalLinkView
}

// retrieve implements R6.2 / DESIGN §8: one round trip from a query (plus
// optional structured filters) to a token-budgeted, provenance-annotated
// context bundle. Anchor selection is FTS5 (index.Search) intersected with
// the structured filters (tags/types/date_range), scoped to patient_id if
// given; every anchor must resolve to the same patient_root (retrieve
// operates on one patient bundle per call, per DESIGN §8's "engine の
// /context-bundle 1往復"), which — absent an explicit patient_id — is
// resolved from the first anchor and every other anchor outside that
// patient is dropped (documented via TruncatedRefs would overstate it: they
// are simply not eligible anchors for this call, since Bundle can only ever
// hold one patient's Pod contents — see graph.LoadBundle's doc comment).
//
// # U5b status normalization (specs/U5_api_retrieve.md)
//
// bead_status (migrations/0006+0007, populated by
// projector.StatusReproject/"record_state_v31") is consulted at TWO batch
// points — once for the anchor set (right after patient scope is fixed,
// before graph.LoadBundle/BuildContext ever runs) and once for the resulting
// Items+TruncatedRefs set (after BuildContext, before clearance) — so every
// one of the four return paths (AnchorIDs, Items, TruncatedRefs, and
// accessibleAnchorIDs, which reuses the already-normalized scopedIDs) applies
// the identical retracted-exclude / amended-substitute / unattested-exclude
// rule, mirroring clearance.FilterByAccess's own 3 independent passes (see
// specs/U5_api_retrieve.md's "合意点" #2). Fixed order per that spec: status
// normalization -> GetBead -> clearance (an amended-substituted or an
// explicitly-surfaced unattested Bead still runs through clearance
// afterward — status normalization only ever narrows/redirects which Bead ID
// clearance is asked about, it never substitutes for clearance itself).
//
// The anchor-stage substitution MUST happen after patientRoot is fixed (see
// statusNormalizeAnchors' own doc comment): substituting an amended anchor to
// its current_bead_id before anchors[0].patientRoot decides the bundle's
// patient could change which patient "wins" retrieve's single-bundle-per-call
// rule.
func (s *Server) retrieve(ctx context.Context, _ *mcp.CallToolRequest, in retrieveIn) (*mcp.CallToolResult, retrieveOut, error) {
	if in.Semantic && s.embedder == nil {
		res, jerr := toolError("retrieve", fmt.Errorf(
			"semantic=true requires an embedder, but this server has none configured (see serve's -embedder flag; docs/requirements.md R4.2)"))
		return res, retrieveOut{}, jerr
	}
	if in.Semantic && in.Query == "" {
		res, jerr := toolError("retrieve", fmt.Errorf("semantic=true requires a non-empty query to embed"))
		return res, retrieveOut{}, jerr
	}
	if in.Query == "" && len(in.Tags) == 0 {
		res, jerr := toolError("retrieve", fmt.Errorf("at least one of query or tags must be set"))
		return res, retrieveOut{}, jerr
	}
	linkDepth, maxLinkedBeads, err := retrieveLinkLimits(in)
	if err != nil {
		res, jerr := toolError("retrieve", err)
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

	anchors, provenance, err := s.retrieveAnchors(ctx, in, patientRoot)
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
	scopedProvenance := make(map[string]anchorProvenance, len(provenance))
	for _, a := range anchors {
		if a.patientRoot != root {
			continue
		}
		scopedIDs = append(scopedIDs, a.id)
		scopedProvenance[a.id] = provenance[a.id]
	}

	// U5b anchor-stage status normalization: MUST run after patient scope
	// (root) is fixed above (see retrieve's own doc comment) — this is the
	// must-fix #1 ordering constraint. This is a single BeadStatusFor batch
	// over scopedIDs (not per-anchor), so it costs one query regardless of
	// anchor count.
	scopedIDs, scopedProvenance, err = s.statusNormalizeAnchors(scopedIDs, scopedProvenance, retrieveIncludeUnattested(in))
	if err != nil {
		res, jerr := toolError("retrieve: status normalize anchors", err)
		return res, retrieveOut{}, jerr
	}
	if len(scopedIDs) == 0 {
		return nil, retrieveOut{BudgetTokens: tokenBudget}, nil
	}

	bd, err := graph.LoadBundle(s.store, root)
	if err != nil {
		res, jerr := toolError("retrieve: load bundle", err)
		return res, retrieveOut{}, jerr
	}

	var (
		patientLinks  []accessiblePatientLink
		linkedAnchors []graph.LinkedAnchor
		linkExpansion *linkExpansionView
	)
	if retrieveIncludeLinks(in) {
		seedIDs := verticalCandidateIDs(bd, scopedIDs, chainDepth)
		var normalizedSeeds []string
		patientLinks, normalizedSeeds, err = s.loadAccessiblePatientLinks(
			bd, root, seedIDs, retrieveIncludeUnattested(in),
		)
		if err != nil {
			res, jerr := toolError("retrieve: load clinical links", err)
			return res, retrieveOut{}, jerr
		}
		candidateCount := 0
		linkedAnchors, candidateCount = expandClinicalLinks(normalizedSeeds, patientLinks, linkDepth, maxLinkedBeads)
		expandedIDs := make([]string, len(linkedAnchors))
		for i, linked := range linkedAnchors {
			expandedIDs[i] = bead.FormatID(linked.ID)
		}
		linkExpansion = &linkExpansionView{
			RequestedDepth:  linkDepth,
			MaxLinkedBeads:  maxLinkedBeads,
			CandidateCount:  candidateCount,
			ExpandedBeadIDs: expandedIDs,
			Truncated:       candidateCount > len(linkedAnchors),
		}
	}

	bundle := graph.BuildContextWithClinicalLinks(
		bd, scopedIDs, linkedAnchors, tokenBudget, chainDepth, chainDepth,
	)

	items, truncated, usedTokens, err := s.filterContextBundle(bundle, scopedProvenance, retrieveIncludeUnattested(in))
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

	var clinicalLinks []retrievedLinkView
	if retrieveIncludeLinks(in) {
		clinicalLinks, err = retrieveClinicalLinksFromPatient(items, patientLinks)
		if err != nil {
			res, jerr := toolError("retrieve: clinical links", err)
			return res, retrieveOut{}, jerr
		}
	}

	return nil, retrieveOut{
		AnchorIDs:     anchorViews,
		Items:         items,
		TruncatedRefs: truncated,
		BudgetTokens:  bundle.BudgetTokens,
		UsedTokens:    usedTokens,
		ClinicalLinks: clinicalLinks,
		LinkExpansion: linkExpansion,
	}, nil
}

// anchorRef is one candidate anchor Bead surfaced by retrieveAnchors, tagged
// with the patient_root it belongs to (so retrieve can scope the eventual
// Bundle to a single patient) and which tag(s) — if any — matched the
// caller's Tags filter (retrieve's own provenance beyond what
// graph.ContextItem tracks, per DESIGN §8).
type anchorRef struct {
	id          string
	patientRoot string
}

// candidateInfo is one anchor candidate before filtering/dedup, shared by
// every anchor-selection source (FTS query, tag-only, semantic) so
// retrieveAnchors' filter/dedup loop treats all three uniformly.
type candidateInfo struct {
	id, patientRoot, typ, timestamp string
	// vectorDistance is non-nil only for a candidate surfaced by
	// SemanticSearch (see retrieveSemanticCandidates) — see anchorProvenance's
	// identical-shape field for why this is a pointer, not a bare float64.
	vectorDistance *float64
}

// retrieveAnchors resolves retrieve's anchor set: an FTS query (if given)
// and/or a semantic (vector) query (if in.Semantic), intersected with
// tags/types/date_range filters, all scoped to patientRoot if non-empty.
// If Query is empty but Tags is set, anchors come from the tag inverted
// index alone (bead_tags), matching DESIGN §8's expectation that tags can
// drive anchor selection on their own. FTS and semantic hits are unioned
// (deduplicated by ID, keeping the first-seen candidate's provenance — see
// the merge loop below) rather than intersected: R4.2's "FTS anchor とマージ
// (重複除去)して既存パイプラインへ" calls for a merge, not a second, stricter
// filter — a Bead either FTS or vector search surfaces (and that also clears
// the structured filters) is eligible. Results are deduplicated and returned
// in a deterministic order (ID) mirroring graph.BuildContext's own
// tie-breaking.
func (s *Server) retrieveAnchors(ctx context.Context, in retrieveIn, patientRoot string) ([]anchorRef, map[string]anchorProvenance, error) {
	db := s.eng.Index()

	var candidates []candidateInfo

	if in.Query != "" {
		hits, err := db.Search(in.Query, 0)
		if err != nil {
			return nil, nil, fmt.Errorf("fts search: %w", err)
		}
		for _, h := range hits {
			candidates = append(candidates, candidateInfo{id: h.BeadID, patientRoot: h.PatientRoot, typ: h.Type, timestamp: h.Timestamp})
		}
	} else if len(in.Tags) > 0 {
		// Tags-only anchor selection: union every Bead carrying any one of
		// the requested tags (deduplicated below).
		seen := make(map[string]bool)
		for _, tag := range in.Tags {
			rows, err := db.SQLDB().Query(`
				SELECT b.id, COALESCE(b.patient_root, ''), b.type, b.timestamp
				FROM bead_tags ba JOIN beads b ON b.id = ba.bead_id
				WHERE ba.tag = ?`, tag)
			if err != nil {
				return nil, nil, fmt.Errorf("tag anchor search %s: %w", tag, err)
			}
			for rows.Next() {
				var c candidateInfo
				if err := rows.Scan(&c.id, &c.patientRoot, &c.typ, &c.timestamp); err != nil {
					rows.Close()
					return nil, nil, fmt.Errorf("tag anchor search %s: scan: %w", tag, err)
				}
				if !seen[c.id] {
					seen[c.id] = true
					candidates = append(candidates, c)
				}
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return nil, nil, fmt.Errorf("tag anchor search %s: %w", tag, err)
			}
			rows.Close()
		}
	}

	if in.Semantic {
		// retrieve's own validation already requires s.embedder != nil and
		// in.Query != "" whenever in.Semantic is true (see retrieve's own
		// early checks), so both are safe to use unconditionally here.
		semanticCandidates, err := s.retrieveSemanticCandidates(ctx, in.Query, patientRoot)
		if err != nil {
			return nil, nil, err
		}
		candidates = append(candidates, semanticCandidates...)
	}

	typeSet := make(map[string]bool, len(in.Types))
	for _, t := range in.Types {
		typeSet[t] = true
	}

	provenance := make(map[string]anchorProvenance)
	var out []anchorRef
	seenID := make(map[string]bool)
	for _, c := range candidates {
		if seenID[c.id] {
			// A later duplicate (e.g. a semantic hit for a Bead the FTS pass
			// already surfaced) still contributes its own provenance (vector
			// distance) even though it does not get a second anchorRef —
			// merge, don't just skip, so a Bead found by both FTS and
			// semantic search reports both matched_tags and vector_distance
			// in one response entry.
			if c.vectorDistance != nil {
				p := provenance[c.id]
				p.VectorDistance = c.vectorDistance
				provenance[c.id] = p
			}
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

		var p anchorProvenance
		if len(in.Tags) > 0 && (in.Query != "" || in.Semantic) {
			// Query/semantic search drove anchor selection but tags were
			// also given: require the anchor to carry at least one requested
			// tag too (an AND, not an OR, when both filters are present
			// alongside a query — tags-only selection above already is the
			// OR case).
			matched, err := matchingTags(db, c.id, in.Tags)
			if err != nil {
				return nil, nil, err
			}
			if len(matched) == 0 {
				continue
			}
			p.MatchedTags = matched
		} else if len(in.Tags) > 0 {
			matched, err := matchingTags(db, c.id, in.Tags)
			if err != nil {
				return nil, nil, err
			}
			p.MatchedTags = matched
		}
		p.VectorDistance = c.vectorDistance

		seenID[c.id] = true
		provenance[c.id] = p
		out = append(out, anchorRef{id: c.id, patientRoot: c.patientRoot})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out, provenance, nil
}

// retrieveSemanticCandidates embeds query via s.embedder and runs
// index.DB.SemanticSearch, scoped to patientRoot via vec0's native
// PARTITION KEY pre-filter when patientRoot is non-empty (R4.2, DESIGN §6).
// Every hit becomes a candidateInfo with vectorDistance set; typ/timestamp
// are resolved via a GetBead lookup per hit (SemanticSearch itself only
// returns bead_id/patient_root/distance — see index.SemanticResult) so the
// merge loop above can apply the same types/date_range filters to a semantic
// hit as to an FTS/tag one.
func (s *Server) retrieveSemanticCandidates(ctx context.Context, query, patientRoot string) ([]candidateInfo, error) {
	vectors, err := s.embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	if len(vectors) != 1 {
		return nil, fmt.Errorf("embed query: embedder returned %d vector(s), want 1", len(vectors))
	}
	queryBlob, err := index.SerializeEmbedding(vectors[0])
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}

	hits, err := s.eng.Index().SemanticSearch(queryBlob, semanticAnchorK, patientRoot)
	if err != nil {
		return nil, fmt.Errorf("semantic search: %w", err)
	}

	out := make([]candidateInfo, 0, len(hits))
	for _, h := range hits {
		ref, err := s.eng.Index().GetBead(h.BeadID)
		if err != nil {
			return nil, fmt.Errorf("resolve semantic hit %s: %w", h.BeadID, err)
		}
		distance := h.Distance
		out = append(out, candidateInfo{
			id:             h.BeadID,
			patientRoot:    h.PatientRoot,
			typ:            ref.Type,
			timestamp:      ref.Timestamp,
			vectorDistance: &distance,
		})
	}
	return out, nil
}

// matchingTags returns the subset of want that beadID actually carries
// (bead_tags — bead_antigens' successor, specs/U2_projection_schema.md /
// U3a), for retrieve's matched_tags provenance.
func matchingTags(db interface {
	GetTags(string) ([]string, error)
}, beadID string, want []string) ([]string, error) {
	have, err := db.GetTags(beadID)
	if err != nil {
		return nil, fmt.Errorf("get tags %s: %w", beadID, err)
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

// filterContextBundle applies U5b status normalization then
// clearance.FilterByAccess to a graph.ContextBundle's Items + TruncatedRefs
// (resolving each item's full Bead via engine.GetBead so FilterByAccess has
// real Clearance/embedded-rule data to evaluate — graph.ContextItem itself
// only carries rendered text, not the Bead), converting the surviving items
// to provenanceView/contextItemView. An item the viewer may not access is
// dropped entirely from the response (masking it in place, as
// FilterByAccess would for a get_bead-style single Bead, would leak that a
// restricted Bead exists in this patient's context at all — retrieve's job
// is to hand an agent a usable, budget-fit context, not a redacted
// placeholder it cannot act on).
//
// Status and TruncatedRefs are normalized together as ONE BeadStatusFor
// batch (both tiers' IDs combined into a single query) — the "one batch at
// the item stage" specs/U5_api_retrieve.md's U5b section calls for,
// distinct from the anchor-stage batch retrieve's own doc comment
// describes.
func (s *Server) filterContextBundle(bundle graph.ContextBundle, provenance map[string]anchorProvenance, includeUnattested bool) ([]provenanceView, []contextItemView, int, error) {
	allIDs := make([]string, 0, len(bundle.Items)+len(bundle.TruncatedRefs))
	for _, item := range bundle.Items {
		allIDs = append(allIDs, item.ID)
	}
	for _, item := range bundle.TruncatedRefs {
		allIDs = append(allIDs, item.ID)
	}
	statuses, err := s.resolveBeadStatuses(allIDs)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("status normalize context bundle: %w", err)
	}

	normalizedItems, unattestedItems, err := s.statusNormalizeContextItems(bundle.Items, statuses, includeUnattested)
	if err != nil {
		return nil, nil, 0, err
	}
	// contextItemView (TruncatedRefs' own view type) carries no
	// not_for_clinical_action field — a truncated ref is already just a bare
	// L2 reference (id/type/timestamp), and the marker's purpose (an agent
	// must not treat this returned content as a validated clinical fact) is
	// only meaningful for an item whose actual Text made it into the
	// response — so the second return value here is intentionally discarded
	// rather than plumbed further.
	normalizedRefs, _, err := s.statusNormalizeContextItems(bundle.TruncatedRefs, statuses, includeUnattested)
	if err != nil {
		return nil, nil, 0, err
	}
	// Amendment substitution can collapse an Item and a TruncatedRef that
	// originally had different IDs onto the same current_bead_id. Items win;
	// preserve BuildContext's global no-duplicate invariant after status.
	seenNormalized := make(map[string]bool, len(normalizedItems)+len(normalizedRefs))
	for _, item := range normalizedItems {
		seenNormalized[item.ID] = true
	}
	dedupedRefs := normalizedRefs[:0]
	for _, ref := range normalizedRefs {
		if seenNormalized[ref.ID] {
			continue
		}
		seenNormalized[ref.ID] = true
		dedupedRefs = append(dedupedRefs, ref)
	}
	normalizedRefs = dedupedRefs
	normalizedItems, normalizedRefs = fitNormalizedContext(normalizedItems, normalizedRefs, bundle.BudgetTokens)

	items, err := s.filterItems(normalizedItems, provenance, unattestedItems)
	if err != nil {
		return nil, nil, 0, err
	}
	truncated, err := s.filterRefs(normalizedRefs)
	if err != nil {
		return nil, nil, 0, err
	}
	usedTokens := 0
	for _, item := range items {
		usedTokens += item.EstimatedTokens
	}
	return items, truncated, usedTokens, nil
}

// fitNormalizedContext re-applies the budget after amendment substitution.
// A corrected Bead can be larger than the stale version BuildContext packed;
// without this pass UsedTokens could exceed BudgetTokens. Existing priority
// order is preserved and an oversized item is demoted to L2 before becoming
// a truncated reference.
func fitNormalizedContext(items, refs []graph.ContextItem, budget int) ([]graph.ContextItem, []graph.ContextItem) {
	remaining := budget
	fitted := make([]graph.ContextItem, 0, len(items))
	outRefs := make([]graph.ContextItem, 0, len(refs))
	for _, item := range items {
		if item.EstimatedTokens <= remaining {
			fitted = append(fitted, item)
			remaining -= item.EstimatedTokens
			continue
		}
		ref := graph.AsL2Reference(item)
		if ref.EstimatedTokens <= remaining {
			fitted = append(fitted, ref)
			remaining -= ref.EstimatedTokens
			continue
		}
		outRefs = append(outRefs, ref)
	}
	outRefs = append(outRefs, refs...)
	return fitted, outRefs
}

func (s *Server) filterItems(items []graph.ContextItem, provenance map[string]anchorProvenance, unattested map[string]bool) ([]provenanceView, error) {
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
		p := provenance[item.ID]
		out = append(out, provenanceView{
			contextItemView:      newContextItemView(item),
			MatchedTags:          p.MatchedTags,
			VectorDistance:       p.VectorDistance,
			NotForClinicalAction: unattested[item.ID],
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
// post-patient-scoping, already status-normalized anchor set — see
// statusNormalizeAnchors), returning the sha256:-prefixed IDs of only the
// anchors this session's role may access — see retrieve's own doc comment at
// its call site for why AnchorIDs needs its own filtering pass independent
// of filterItems/filterRefs.
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

// --- U5b status normalization (specs/U5_api_retrieve.md) ------------------
//
// bead_status resolution is centralized here so retrieve's anchor stage,
// item stage, and get_links/retrieveClinicalLinks' link-endpoint stage all
// apply the identical retracted/amended/unattested rule via one shared
// low-level batch call (resolveBeadStatuses), never a per-Bead status query.

// loggedEmptyBeadStatusOnce guards the "record_state projection missing"
// note (see resolveBeadStatuses) so a busy server does not spam this line on
// every single retrieve/get_links call against a store that has genuinely
// never run StatusReproject — one note per process lifetime is enough to
// tell an operator the projector has not been run yet.
var loggedEmptyBeadStatusOnce sync.Once

// resolveBeadStatuses batches index.DB.BeadStatusFor over ids and implements
// specs/U5_api_retrieve.md's crux 2 ruling (empty-store fallback, decided by
// the user): if bead_status is entirely empty (StatusReproject has never run
// against this store — a dev/fresh store), every requested id is absent from
// BeadStatusFor's result map, which callers already treat as "active" (see
// BeadStatusFor's own doc comment) — so no special-case substitution is
// needed here at all, only a one-time diagnostic note so an operator can
// tell "no bead_status rows" apart from "every Bead happens to be active".
// If the table has any rows but one requested ID is absent, the projection is
// partially stale/inconsistent and this function returns a controlled error.
// Treating such a new amendment as active before record-state reprojection
// could surface an unapproved correction, so only the entirely-empty
// development-store case is allowed to fail open.
func (s *Server) resolveBeadStatuses(ids []string) (map[string]index.BeadStatusRow, error) {
	if len(ids) == 0 {
		return map[string]index.BeadStatusRow{}, nil
	}
	uniqueIDs := make([]string, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id != "" && !seen[id] {
			seen[id] = true
			uniqueIDs = append(uniqueIDs, id)
		}
	}
	statuses, err := s.eng.Index().BeadStatusFor(uniqueIDs)
	if err != nil {
		return nil, fmt.Errorf("bead status for %d ids: %w", len(uniqueIDs), err)
	}
	if len(statuses) == len(uniqueIDs) {
		return statuses, nil
	}

	empty, emptyErr := s.eng.Index().BeadStatusTableEmpty()
	if emptyErr != nil {
		return nil, fmt.Errorf("bead status table empty check: %w", emptyErr)
	}
	if empty {
		loggedEmptyBeadStatusOnce.Do(func() {
			log.Printf("retrieve: record_state projection missing (bead_status is empty) — treating every Bead as active; run reproject -record-state to populate it")
		})
		return statuses, nil
	}

	missing := make([]string, 0, len(uniqueIDs)-len(statuses))
	for _, id := range uniqueIDs {
		if _, ok := statuses[id]; !ok {
			missing = append(missing, id)
		}
	}
	sort.Strings(missing)
	return nil, fmt.Errorf("record_state projection is incomplete: %d of %d requested Beads are missing status (first missing: %s); run reproject -record-state",
		len(missing), len(uniqueIDs), missing[0])
}

// resolveStatusDecision is one Bead ID's §2 outcome as retrieve/get_links'
// status-normalization pass acts on it: whether to drop it entirely, and if
// not, which Bead ID to actually use (self for active/unattested-surfaced,
// current_bead_id for a validly-amended Bead).
type resolveStatusDecision struct {
	drop                 bool
	resolvedID           string
	notForClinicalAction bool
}

// resolveStatus applies specs/U5_api_retrieve.md's U5b status-normalization
// rule to one Bead ID given its (possibly absent — meaning "active",
// BeadStatusFor's own contract) bead_status row:
//   - retracted -> drop.
//   - unattested -> drop by default; if includeUnattested, keep (self-id,
//     not_for_clinical_action=true).
//   - amended -> substitute to current_bead_id; if current_bead_id is empty
//     (NULL — the retracted-chain-leaf case, resolve.go's beadState doc
//     comment), treat it the same as retracted and drop rather than
//     substituting to nothing (must-fix #1's second half).
//   - active, or absent from the map entirely -> keep (self-id).
func resolveStatus(id string, st index.BeadStatusRow, hasStatus bool, includeUnattested bool) resolveStatusDecision {
	if !hasStatus {
		return resolveStatusDecision{resolvedID: id}
	}
	switch st.Status {
	case "retracted":
		return resolveStatusDecision{drop: true}
	case "unattested":
		if !includeUnattested {
			return resolveStatusDecision{drop: true}
		}
		return resolveStatusDecision{resolvedID: id, notForClinicalAction: true}
	case "amended":
		if st.CurrentBeadID == "" {
			// current_bead_id NULL: this amended Bead's correction chain
			// terminates at a retracted leaf (resolve.go's chainLeaf) —
			// there is no valid "current" version to substitute to, so drop
			// it exactly like the plain retracted case rather than
			// substituting to an empty ID.
			return resolveStatusDecision{drop: true}
		}
		return resolveStatusDecision{resolvedID: st.CurrentBeadID}
	default: // "active", or any future status this pass does not special-case
		return resolveStatusDecision{resolvedID: id}
	}
}

// statusNormalizeAnchors applies resolveStatus to retrieve's post-patient-
// scoping anchor set (scopedIDs) — the anchor-stage batch retrieve's own doc
// comment describes. This MUST run after patient scope (root) is already
// fixed by the caller (retrieve): substituting an amended anchor to its
// current_bead_id before root is decided could change which patient
// anchors[0] resolves to, per the must-fix ordering constraint.
//
// Substitution can collapse two distinct pre-normalization anchor IDs onto
// the same resolvedID (e.g. two different amendments of the same original
// fact, both still present as separate anchors before normalization) —
// dedup happens here so the returned scopedIDs/provenance never contains a
// duplicate ID afterward; the first-seen anchor's provenance (matched tags/
// vector distance) wins for a collided ID, mirroring retrieveAnchors' own
// first-seen-wins dedup convention.
func (s *Server) statusNormalizeAnchors(ids []string, provenance map[string]anchorProvenance, includeUnattested bool) ([]string, map[string]anchorProvenance, error) {
	if len(ids) == 0 {
		return nil, provenance, nil
	}
	statuses, err := s.resolveBeadStatuses(ids)
	if err != nil {
		return nil, nil, fmt.Errorf("status normalize anchors: %w", err)
	}

	out := make([]string, 0, len(ids))
	outProvenance := make(map[string]anchorProvenance, len(provenance))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		st, hasStatus := statuses[id]
		decision := resolveStatus(id, st, hasStatus, includeUnattested)
		if decision.drop {
			continue
		}
		if seen[decision.resolvedID] {
			continue
		}
		seen[decision.resolvedID] = true
		out = append(out, decision.resolvedID)
		outProvenance[decision.resolvedID] = provenance[id]
	}
	return out, outProvenance, nil
}

// statusNormalizeContextItems applies resolveStatus to a batch of
// graph.ContextItem (retrieve's Items or TruncatedRefs tier), given an
// already-resolved statuses map (filterContextBundle batches Items +
// TruncatedRefs into one shared BeadStatusFor call, so this function itself
// makes no DB call). An amended item is rewritten in place to reference
// current_bead_id (Provenance/Granularity/tier bookkeeping is preserved from
// the original item — ID/Type/Timestamp/Text/EstimatedTokens are re-rendered
// immediately from the current Bead, preventing stale original text from
// being mislabeled with current_bead_id). The returned map flags which
// (post-substitution) IDs are
// unattested-and-explicitly-surfaced, for provenanceView's
// NotForClinicalAction field.
func (s *Server) statusNormalizeContextItems(items []graph.ContextItem, statuses map[string]index.BeadStatusRow, includeUnattested bool) ([]graph.ContextItem, map[string]bool, error) {
	if len(items) == 0 {
		return nil, nil, nil
	}
	out := make([]graph.ContextItem, 0, len(items))
	unattested := make(map[string]bool)
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		st, hasStatus := statuses[item.ID]
		decision := resolveStatus(item.ID, st, hasStatus, includeUnattested)
		if decision.drop {
			continue
		}
		if seen[decision.resolvedID] {
			continue
		}
		seen[decision.resolvedID] = true
		normalized := item
		if decision.resolvedID != item.ID {
			replacement, err := s.eng.GetBead(decision.resolvedID)
			if err != nil {
				return nil, nil, fmt.Errorf("get current amendment %s for %s: %w", decision.resolvedID, item.ID, err)
			}
			normalized = graph.RefreshContextItem(item, replacement)
		}
		out = append(out, normalized)
		if decision.notForClinicalAction {
			unattested[decision.resolvedID] = true
		}
	}
	return out, unattested, nil
}

// resolvedLinkEndpoint is one clinical_links row after U5b status
// normalization has been applied to its OTHER endpoint (never to the row's
// own attached Bead, which is the caller's already-normalized item) — see
// statusNormalizeLinkEndpoints.
type resolvedLinkEndpoint struct {
	row         index.ClinicalLinkRow
	otherBeadID string
}

// statusNormalizeLinkEndpoints applies specs/U5_api_retrieve.md's "合意点"
// #10 to a batch of clinical_links rows: get_links/retrieveClinicalLinks
// must not surface a link whose OTHER endpoint is retracted or (by default)
// unattested, and must substitute OtherBeadID to current_bead_id for an
// amended other endpoint — mirroring statusNormalizeContextItems' rule but
// applied to OtherBeadID rather than a context item's own ID. One
// BeadStatusFor batch covers every row's other-endpoint ID regardless of row
// count (no N+1). Rows that resolve to the identical (deduplicated)
// otherBeadID as another row in the same batch are NOT deduplicated here
// (unlike the anchor/item passes) — get_links/retrieveClinicalLinks' own
// LinkID-based dedup already handles the case that matters (the same
// clinical_links row reached twice), and two DIFFERENT link rows that
// happen to both resolve to the same current_bead_id after amendment are
// legitimately two separate relations worth keeping.
func (s *Server) statusNormalizeLinkEndpoints(rows []index.ClinicalLinkRow, includeUnattested bool) ([]resolvedLinkEndpoint, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.OtherBeadID
	}
	statuses, err := s.resolveBeadStatuses(ids)
	if err != nil {
		return nil, fmt.Errorf("status normalize link endpoints: %w", err)
	}

	out := make([]resolvedLinkEndpoint, 0, len(rows))
	for _, r := range rows {
		st, hasStatus := statuses[r.OtherBeadID]
		decision := resolveStatus(r.OtherBeadID, st, hasStatus, includeUnattested)
		if decision.drop {
			continue
		}
		out = append(out, resolvedLinkEndpoint{row: r, otherBeadID: decision.resolvedID})
	}
	return out, nil
}

// decodeEvidenceBeadIDs decodes clinical_links.evidence_bead_ids' canonical-
// JSON-array-of-plain-hex-IDs column (migrations/0006's own comment on this
// column's storage convention) into sha256:-prefixed display IDs, shared by
// get_links and retrieveClinicalLinks so both apply the identical decode +
// display-prefix conversion.
func decodeEvidenceBeadIDs(raw string) ([]string, error) {
	var evidenceIDs []string
	if raw != "" && raw != "[]" {
		if err := json.Unmarshal([]byte(raw), &evidenceIDs); err != nil {
			return nil, fmt.Errorf("decode evidence_bead_ids: %w", err)
		}
	}
	formatted := make([]string, len(evidenceIDs))
	for i, id := range evidenceIDs {
		formatted[i] = bead.FormatID(id)
	}
	return formatted, nil
}
