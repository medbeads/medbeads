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
