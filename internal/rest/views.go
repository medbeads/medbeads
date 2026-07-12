package rest

import (
	"github.com/medbeads/medbeads/internal/engine/bead"
	"github.com/medbeads/medbeads/internal/engine/clearance"
)

// beadView is this package's wire shape for a Bead — v2.2.0's
// core/types.Bead JSON shape verbatim (id/type/content/parents/timestamp,
// plain-hex id, no sha256: prefix — see doc.go's "ID notation"). v2's
// types.Bead carried only these five fields (no author/evidence), and
// ui/src/lib/api.ts's Bead interface mirrors exactly that same five-field
// shape, so this package intentionally narrows internal/engine/bead.Bead
// (which additionally carries Author/Amends/Retracts/Evidence/Clearance/
// Signature) down to those five fields here rather than exposing its full v3
// shape — widening the frozen contract is itself a contract change.
type beadView struct {
	ID        string         `json:"id,omitempty"`
	Type      string         `json:"type"`
	Timestamp string         `json:"timestamp"`
	Parents   []string       `json:"parents"`
	Content   map[string]any `json:"content"`
}

// newBeadView narrows an engine-internal bead.Bead to the frozen v2 wire
// shape. Parents/Content pass through unchanged: bead.Bead already carries
// plain-hex parent IDs (no FormatID conversion — see doc.go) and
// map[string]any content identical in shape to v2's types.Bead.Content.
func newBeadView(b bead.Bead) beadView {
	parents := b.Parents
	if parents == nil {
		parents = []string{}
	}
	content := b.Content
	if content == nil {
		content = map[string]any{}
	}
	return beadView{
		ID:        b.ID,
		Type:      b.Type,
		Timestamp: b.Timestamp,
		Parents:   parents,
		Content:   content,
	}
}

// newBeadViews converts beads to their wire views, preserving nil-ness: a
// nil input yields a nil (not empty-non-nil) output, so writeJSON marshals a
// zero-result response as JSON `null` — matching v2.2.0's core/api handlers,
// whose `var patients []types.Bead` result variables were never
// initialized to a non-nil empty slice, and whose own
// json.NewEncoder(w).Encode therefore emitted literal `null` for zero
// results (see FilterByAccess's `if len(beads) == 0 { return beads, nil }`
// early return, which passes a nil slice straight through). ui/src/lib/api.ts's
// fetchAllPatients defends against this (`response.data || []`), so
// reproducing v2's `null` here rather than `[]` is required for byte-for-byte
// contract fidelity, not merely harmless.
func newBeadViews(beads []bead.Bead) []beadView {
	if beads == nil {
		return nil
	}
	out := make([]beadView, len(beads))
	for i, b := range beads {
		out[i] = newBeadView(b)
	}
	return out
}

// resourceTypeCount is v2.2.0's core/store.ResourceTypeCount JSON shape
// verbatim (resourceType/patientCount — the one place this contract uses
// camelCase rather than snake_case, because that is what v2 shipped and
// ui/src/lib/api.ts's ResourceTypeCount interface still expects).
type resourceTypeCount struct {
	ResourceType string `json:"resourceType"`
	PatientCount int    `json:"patientCount"`
}

// clearanceRuleView is v2.2.0's core/types.ClearanceRule JSON shape
// verbatim, matching ui/src/lib/api.ts's ClearanceRule interface field for
// field.
type clearanceRuleView struct {
	ID           string   `json:"id"`
	BeadID       string   `json:"bead_id"`
	DeniedRoles  []string `json:"denied_roles"`
	AllowedRoles []string `json:"allowed_roles,omitempty"`
	CreatedBy    string   `json:"created_by"`
	CreatedAt    string   `json:"created_at"`
	Reason       string   `json:"reason,omitempty"`
	ExpiresAt    *string  `json:"expires_at,omitempty"`
}

func newClearanceRuleView(r clearance.Rule) clearanceRuleView {
	denied := r.DeniedRoles
	if denied == nil {
		denied = []string{}
	}
	return clearanceRuleView{
		ID:           r.ID,
		BeadID:       r.BeadID,
		DeniedRoles:  denied,
		AllowedRoles: r.AllowedRoles,
		CreatedBy:    r.CreatedBy,
		CreatedAt:    r.CreatedAt,
		Reason:       r.Reason,
		ExpiresAt:    r.ExpiresAt,
	}
}

// newClearanceRuleViews preserves nil-ness like newBeadViews (see its doc
// comment): v2.2.0's GetClearanceRules also returns a nil slice for zero
// rows, so getClearanceHandler must emit `null`, not `[]`, in that case.
func newClearanceRuleViews(rules []clearance.Rule) []clearanceRuleView {
	if rules == nil {
		return nil
	}
	out := make([]clearanceRuleView, len(rules))
	for i, r := range rules {
		out[i] = newClearanceRuleView(r)
	}
	return out
}

// --- graph view (R7a, specs/R7_graph_view.md) ------------------------------
//
// These are a NEW, separate view family for GET /patients/{root}/graph — NOT
// an extension of beadView (see beadView's own doc comment: it is a frozen
// v2 contract, and widening it is itself a contract change). graphBeadView
// carries fields (recorded_at, status, current_bead_id, amends, retracts)
// beadView has never had and never will. Like every other view in this
// package, IDs here are plain hex, no "sha256:" prefix (see doc.go's "ID
// notation" — this package's convention, not v2.2.0's, since this is a new
// v3-only endpoint with no v2 precedent either way; consistency with every
// other ID this package already emits weighed more than adopting mcpserver's
// sha256: convention for one single new endpoint).

// graphBeadView is one beads[] entry in R7a's response: ListPatientBeads'
// identifying fields plus recorded_at (write-instant) and the bead_status
// fields (status/current_bead_id) the frozen beadView never carries, plus
// amends/retracts read directly off the Bead's own content (bead.Bead.Amends/
// Retracts are hash-target fields, not projection-derived — see
// specs/R7_graph_view.md: "amends/retracts は edge ではなく beads[].amends/
// retracts フィールドで表現"). Amends/Retracts are arrays (0..n), matching
// bead.Bead.Amends/Retracts' own []string shape verbatim — the contract was
// corrected to arrays (2026-07-12, lead ruling) specifically so a multi-
// target amend/retract does not lose information the way a single-string
// reduction would; a nil bead.Bead.Amends/Retracts is rendered as `[]`, never
// `null` (see newGraphBeadView).
type graphBeadView struct {
	ID            string   `json:"id"`
	Type          string   `json:"type"`
	Timestamp     string   `json:"timestamp"`
	RecordedAt    string   `json:"recorded_at"`
	Summary       string   `json:"summary"`
	Status        string   `json:"status"`
	CurrentBeadID string   `json:"current_bead_id"`
	Amends        []string `json:"amends"`
	Retracts      []string `json:"retracts"`
}

// graphEdgeView is one edges[] entry: a single 'parent' bead_edges row
// (specs/R7_graph_view.md: "bead_edges の edge_type='parent' のみ… sibling は
// 死文化なので出さない").
type graphEdgeView struct {
	ChildID  string `json:"child_id"`
	ParentID string `json:"parent_id"`
}

// graphLinkView is one links[] entry: a patient-scoped clinical_links row,
// naming both endpoints (bead_a < bead_b, undirected — the table's own CHECK
// constraint already normalizes this order, see migrations/
// 0006_projection_v31.sql) rather than get_links'
// caller-relative "other_bead_id" shape, per specs/R7_graph_view.md's
// contract JSON.
type graphLinkView struct {
	LinkID        string `json:"link_id"`
	BeadA         string `json:"bead_a"`
	BeadB         string `json:"bead_b"`
	Relation      string `json:"relation"`
	MatchedTag    string `json:"matched_tag"`
	Severity      string `json:"severity"`
	EvidenceBasis string `json:"evidence_basis"`
	RuleVersion   string `json:"rule_version"`
	// ProjectionRunID completes the provenance chain on the wire: a viewer
	// holding a rendered arc can resolve run -> projection_manifest -> the
	// knowledge Bead IDs that produced this generation of links. Widening this
	// view is safe — /patients/{root}/graph is the R7 endpoint, not part of the
	// frozen v2 beadView contract.
	ProjectionRunID string `json:"projection_run_id"`
}

// graphResponse is R7a's full GET /patients/{root}/graph response shape,
// verbatim per specs/R7_graph_view.md's contract. Unlike newBeadViews'
// nil-preserving convention (this package's frozen v2 endpoints emit JSON
// `null` for zero results), beads/edges/links here are always non-nil
// (empty `[]`, never `null`): this is a brand-new v3 endpoint with no v2
// wire-format precedent to reproduce byte-for-byte, and a graph-drawing UI
// consuming this response benefits from never having to special-case `null`
// vs `[]` for three different array fields in one object.
type graphResponse struct {
	PatientRoot string          `json:"patient_root"`
	Beads       []graphBeadView `json:"beads"`
	Edges       []graphEdgeView `json:"edges"`
	Links       []graphLinkView `json:"links"`
}
