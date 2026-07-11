package mcpserver

import (
	"github.com/medbeads/medbeads/internal/engine/bead"
	"github.com/medbeads/medbeads/internal/engine/graph"
)

// beadView is the JSON shape every tool returns for one Bead: the display-
// layer "sha256:" ID prefix (bead.FormatID) is applied here, at the MCP
// boundary — never inside package engine/bead/graph/index, which all work in
// plain 64-hex per specs/DESIGN_v3.md §4 ("内部は素の 64 hex、API/表示層での
// み sha256: プレフィックス"). This is that one API-layer conversion point.
//
// No Antigens field: per specs/DESIGN_v3.1_draft.md §5 ("get_bead は正本のみ
// (タグを含まない)"), the正本 (Bead) view never carries tags — those are
// projection-only and belong on retrieve's/get_bead_with_projection's
// response shape instead (a later unit; not yet added).
type beadView struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Author    string          `json:"author,omitempty"`
	Parents   []string        `json:"parents"`
	Amends    []string        `json:"amends,omitempty"`
	Retracts  []string        `json:"retracts,omitempty"`
	Content   map[string]any  `json:"content"`
	Evidence  []bead.Evidence `json:"evidence,omitempty"`
}

// newBeadView converts an engine-internal bead.Bead (plain-hex ID/parents)
// into its API-layer JSON view (sha256:-prefixed ID/parents/amends/retracts).
func newBeadView(b bead.Bead) beadView {
	parents := make([]string, len(b.Parents))
	for i, p := range b.Parents {
		parents[i] = bead.FormatID(p)
	}
	amends := make([]string, len(b.Amends))
	for i, a := range b.Amends {
		amends[i] = bead.FormatID(a)
	}
	retracts := make([]string, len(b.Retracts))
	for i, r := range b.Retracts {
		retracts[i] = bead.FormatID(r)
	}
	return beadView{
		ID:        bead.FormatID(b.ID),
		Type:      b.Type,
		Timestamp: b.Timestamp,
		Author:    b.Author,
		Parents:   parents,
		Amends:    amends,
		Retracts:  retracts,
		Content:   b.Content,
		Evidence:  b.Evidence,
	}
}

// beadRefView is the API-layer JSON shape for an index.BeadRef /
// index.SearchResult-derived reference: enough to identify and rank a Bead
// without paying for its full content (used by search_beads, list_patients,
// get_timeline, search_antigens).
//
// # Clearance and Summary
//
// Summary is index-derived text (beads.summary — a machine-generated
// fragment of the Bead's own Content, per index.Flattener) that
// clearance.FilterByAccess never touches: FilterByAccess masks a Bead's
// returned Content field in place (see its own doc comment), it does not
// scrub any separately-fetched index metadata about that same Bead. A
// caller that resolves Summary from the *pre-filter* index row (hit.Summary,
// ref.Summary, ...) rather than from the post-filter Bead therefore leaks
// restricted content through a side channel even though the Bead's own
// Content looks masked. newBeadRefView is the single constructor for this
// view and only ever accepts a summary the caller has already confirmed
// belongs to an accessible (non-masked) Bead — see accessible() and every
// call site in tools_read.go, which all skip (drop, not mask) a beadRefView
// entirely for a restricted Bead rather than emit one with an empty-but-
// present Summary field (which would still leak the Bead's existence/type/
// timestamp inconsistently with get_bead/retrieve's own masking-then-drop
// convention for this same case).
type beadRefView struct {
	ID          string `json:"id"`
	PatientRoot string `json:"patient_root,omitempty"`
	Type        string `json:"type"`
	Timestamp   string `json:"timestamp"`
	Summary     string `json:"summary,omitempty"`
}

// newBeadRefView builds a beadRefView for b, an already clearance-filtered
// Bead (see accessible()), plus the index-derived patientRoot/summary a
// caller looked up alongside it. Callers must not call this for a masked
// Bead (accessible(b) == false) — every call site in tools_read.go checks
// accessible(b) first and drops the ref entirely rather than calling this.
func newBeadRefView(b bead.Bead, patientRoot, summary string) beadRefView {
	view := beadRefView{
		Type:      b.Type,
		Timestamp: b.Timestamp,
		Summary:   summary,
	}
	if b.ID != "" {
		view.ID = bead.FormatID(b.ID)
	}
	if patientRoot != "" {
		view.PatientRoot = bead.FormatID(patientRoot)
	}
	return view
}

// contextItemView is the API-layer JSON shape for a graph.ContextItem
// (retrieve/get_context's per-Bead entry): sha256:-prefixed ID, plus the
// provenance/granularity/token bookkeeping DESIGN §8 requires clients to be
// able to audit.
type contextItemView struct {
	ID              string `json:"id"`
	Type            string `json:"type"`
	Timestamp       string `json:"timestamp"`
	Provenance      string `json:"provenance"`
	Granularity     string `json:"granularity"`
	Text            string `json:"text,omitempty"`
	EstimatedTokens int    `json:"estimated_tokens"`
}

func newContextItemView(item graph.ContextItem) contextItemView {
	return contextItemView{
		ID:              bead.FormatID(item.ID),
		Type:            item.Type,
		Timestamp:       item.Timestamp,
		Provenance:      string(item.Provenance),
		Granularity:     string(item.Granularity),
		Text:            item.Text,
		EstimatedTokens: item.EstimatedTokens,
	}
}

func newContextItemViews(items []graph.ContextItem) []contextItemView {
	out := make([]contextItemView, len(items))
	for i, item := range items {
		out[i] = newContextItemView(item)
	}
	return out
}

// accessible reports whether b (as returned by clearance.FilterByAccess) is
// the real Bead rather than the masked {"_restricted": true} placeholder
// FilterByAccess substitutes in place for a Bead the viewer may not see (see
// FilterByAccess's own doc comment: it masks Content but does not shrink the
// slice it returns). Every tool in this package that calls FilterByAccess
// must check accessible() per-element and drop (not just mask-and-forward)
// any Bead it is false for — masking alone is not enough at this package's
// boundary, because several call sites (search_beads/list_patients/
// get_timeline/search_antigens's beadRefView, get_links) attach
// additional index-derived fields (Summary, a clinical-link relationship's
// own existence) alongside the Bead that FilterByAccess itself has no way to
// mask, so "masked Content but a beadRefView/clinicalLinkView still emitted"
// would leak exactly the information FilterByAccess was trying to hide.
// Dropping the element entirely is this package's uniform policy for every
// tool (see doc.go).
func accessible(b bead.Bead) bool {
	restricted, ok := b.Content["_restricted"].(bool)
	return !(ok && restricted)
}
