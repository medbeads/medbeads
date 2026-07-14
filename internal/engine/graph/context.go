package graph

import (
	"encoding/json"
	"sort"

	"github.com/medbeads/medbeads/internal/engine/bead"
)

// Granularity is how much of a Bead's content BuildContext includes in a
// ContextItem, per specs/DESIGN_v3.md §8's 3-tier token budget.
type Granularity string

const (
	// GranularityL0 is full content: the anchor tier only (~500 tok per
	// DESIGN §8's estimate).
	GranularityL0 Granularity = "L0"
	// GranularityL1 is a 1-line machine-generated summary (beads.summary,
	// ~40 tok).
	GranularityL1 Granularity = "L1"
	// GranularityL2 is just ID + type + timestamp (~15 tok) — a reference an
	// agent can resolve via get_bead if it needs more.
	GranularityL2 Granularity = "L2"
)

// Provenance records which traversal edge brought a Bead into a
// ContextBundle, per specs/DESIGN_v3.md §8 ("provenance... を含め監査と実験
// ログを兼ねる"). This unit only fills in the graph-edge provenance kinds;
// FTS score / vector similarity / matched_antigen provenance is the search
// layer's responsibility (a later unit) and is deliberately not modeled
// here.
type Provenance string

const (
	ProvenanceAnchor       Provenance = "anchor"
	ProvenanceClinicalLink Provenance = "clinical_link"
	ProvenanceAncestor     Provenance = "ancestor"
	ProvenanceDescendant   Provenance = "descendant"
)

// LinkedAnchor is a clinical_links endpoint selected outside package graph.
// The caller owns link policy (status, clearance, traversal limits and
// severity/evidence ordering); this package preserves the approved path in
// the context bundle.
type LinkedAnchor struct {
	ID        string
	ViaLinkID string
	Depth     int
}

// ContextItem is one Bead's contribution to a ContextBundle: how it was
// reached (Provenance), at what granularity it was ultimately included, and
// the rendered text for that granularity.
type ContextItem struct {
	ID          string
	Type        string
	Timestamp   string
	Provenance  Provenance
	Granularity Granularity
	// Text is the rendered content at Granularity: full JSON-ish content
	// summary for L0 (see renderL0), the Bead's one-line summary for L1, or
	// empty for L2 (L2 carries no content text at all — ID/Type/Timestamp
	// above are the entire reference).
	Text string
	// EstimatedTokens is EstimateTokens(Text) plus this item's fixed
	// ID/Type/Timestamp overhead, i.e. what this item actually cost against
	// budget.
	EstimatedTokens int
	// ViaLinkID and LinkDepth are populated only for clinical_link items.
	ViaLinkID string
	LinkDepth int
}

// ContextBundle is BuildContext's result: the Beads that fit inside the
// token budget (Items, priority order preserved) plus L2 references for
// everything that did not (TruncatedRefs) — "切り捨て分も必ず L2 参照で列挙"
// per specs/DESIGN_v3.md §8, so an agent can still get_bead anything that
// was cut.
type ContextBundle struct {
	AnchorIDs     []string
	Items         []ContextItem
	TruncatedRefs []ContextItem
	BudgetTokens  int
	UsedTokens    int
}

// candidate is one Bead under consideration for inclusion, before a
// granularity/budget decision has been made for it.
type candidate struct {
	b          bead.Bead
	provenance Provenance
	// tier is the priority bucket index (lower = higher priority), per the
	// order documented on BuildContext.
	tier int
	// linkOrder preserves the policy order supplied by the caller for the
	// clinical-link tier. Other tiers retain timestamp/ID ordering.
	linkOrder int
	viaLinkID string
	linkDepth int
}

// tierGranularity is the granularity BuildContext attempts first for each
// priority tier: anchor L0 -> clinical-link endpoint L0 -> ancestor L1 ->
// descendant L2. The horizontal tier is explicit and bounded by the caller;
// it is not the removed sibling/APC inference machinery.
var tierGranularity = []Granularity{
	GranularityL0, // tier 0: anchors
	GranularityL0, // tier 1: clinical_links endpoints
	GranularityL1, // tier 2: ancestors
	GranularityL2, // tier 3: descendants
}

const (
	tierAnchor = iota
	tierClinicalLink
	tierAncestor
	tierDescendant
)

// BuildContext assembles a token-budgeted ContextBundle for anchors within
// bd, per specs/DESIGN_v3.md §8: starting from anchors (full content, L0),
// then ancestors (L1 summary), then descendants (L2 reference) — each
// tier's Beads greedily packed in tier order until budget (a token count,
// see EstimateTokens) is exhausted. A Bead reachable through more than one
// tier/anchor is only ever included once, at the highest-priority tier that
// reaches it (deduplication by first-seen tier).
//
// Every anchor is guaranteed at least an L2 reference somewhere in the
// result (either Items or TruncatedRefs) even if budget is 0 or otherwise
// too small for its L0 content — BuildContext never silently drops an
// anchor.
//
// ancestorDepth/descendantDepth bound how far BuildContext walks via
// bd.Ancestors/bd.Descendants from each anchor before ranking candidates;
// they are independent of the token budget itself (a large depth just means
// more low-priority candidates competing for whatever budget remains).
func BuildContext(bd *Bundle, anchors []string, budget int, ancestorDepth, descendantDepth int) ContextBundle {
	return BuildContextWithClinicalLinks(bd, anchors, nil, budget, ancestorDepth, descendantDepth)
}

// BuildContextWithClinicalLinks extends BuildContext with an explicit,
// auditable horizontal tier. Original search anchors remain highest priority;
// linked endpoints follow at L0 in caller-supplied order. Vertical context is
// then walked from both sets, allowing a linked result to bring the encounter
// chain needed to interpret it without reviving the old sibling/APC model.
func BuildContextWithClinicalLinks(bd *Bundle, anchors []string, linked []LinkedAnchor, budget int, ancestorDepth, descendantDepth int) ContextBundle {
	out := ContextBundle{
		AnchorIDs:    append([]string(nil), anchors...),
		BudgetTokens: budget,
	}

	// Two-pass claim resolution (fixes a duplicate-ContextItem bug found via
	// bench/'s dag_full/dag_nosib comparison, R8.2): pass 1 below only
	// resolves, per Bead ID, the single *best* (lowest-index) tier/
	// provenance it is ever reachable at across every anchor — nothing is
	// appended to tiers[] yet, so there is no stale-entry-from-an-earlier-
	// tier-assignment to leave behind when a later anchor reaches the same
	// Bead at a higher-priority tier. tiers[] is materialized once, after
	// resolution, entirely from claims map (see below) — structurally
	// impossible for the same Bead ID to land in two different tiers[]
	// slices, unlike the prior single-pass "claim = seen-check + immediate
	// append" design, whose seen[id] guard only blocked a re-claim into an
	// equal-or-*worse* tier: a later claim into a *better* tier updated
	// seen[id] but never removed the earlier, now-stale entry already
	// sitting in tiers[oldTier], so the same Bead could appear twice in
	// Items/TruncatedRefs (VERIFIED against real scratch data via a
	// multi-anchor semantic=true retrieve call, see
	// TestBuildContext_MultiAnchor_CrossAnchorTierPromotion_NoDuplicate).
	claims := make(map[string]candidate, len(bd.beads)) // id -> its resolved (best-tier) candidate

	resolve := func(id string, tier int, provenance Provenance, linkOrder int, viaLinkID string, linkDepth int) {
		if id == "" {
			return
		}
		b, ok := bd.beads[id]
		if !ok {
			return
		}
		if prior, ok := claims[id]; ok && prior.tier <= tier {
			return
		}
		claims[id] = candidate{
			b: b, provenance: provenance, tier: tier, linkOrder: linkOrder,
			viaLinkID: viaLinkID, linkDepth: linkDepth,
		}
	}

	// Anchors first, so they always win the highest tier regardless of
	// whether some other anchor's ancestor/descendant walk also reaches
	// them.
	for _, id := range anchors {
		resolve(id, tierAnchor, ProvenanceAnchor, 0, "", 0)
	}
	for i, l := range linked {
		resolve(l.ID, tierClinicalLink, ProvenanceClinicalLink, i, l.ViaLinkID, l.Depth)
	}

	seeds := make([]string, 0, len(anchors)+len(linked))
	seeds = append(seeds, anchors...)
	for _, l := range linked {
		seeds = append(seeds, l.ID)
	}
	for _, id := range seeds {
		for _, a := range bd.Ancestors(id, ancestorDepth) {
			if a.ID == id {
				continue // Ancestors includes the anchor itself at depth 0
			}
			resolve(a.ID, tierAncestor, ProvenanceAncestor, 0, "", 0)
		}
		for _, d := range bd.Descendants(id, descendantDepth) {
			if d.ID == id {
				continue // Descendants includes the anchor itself at depth 0
			}
			resolve(d.ID, tierDescendant, ProvenanceDescendant, 0, "", 0)
		}
	}

	// Pass 2: materialize tiers[] once, from each Bead's single resolved
	// candidate — every Bead ID appears in exactly one tiers[] slice.
	tiers := make([][]candidate, len(tierGranularity))
	for _, c := range claims {
		tiers[c.tier] = append(tiers[c.tier], c)
	}

	// Deterministic ordering within a tier (map iteration over claims is
	// not stable) so BuildContext's output — and therefore which items get
	// truncated first under a tight budget — does not vary run to run.
	for tierIdx, t := range tiers {
		sort.Slice(t, func(i, j int) bool {
			if tierIdx == tierClinicalLink && t[i].linkOrder != t[j].linkOrder {
				return t[i].linkOrder < t[j].linkOrder
			}
			if t[i].b.Timestamp != t[j].b.Timestamp {
				return t[i].b.Timestamp < t[j].b.Timestamp
			}
			return t[i].b.ID < t[j].b.ID
		})
	}

	remaining := budget
	for tierIdx, t := range tiers {
		granularity := tierGranularity[tierIdx]
		for _, c := range t {
			item := renderItem(c, granularity)
			if item.EstimatedTokens <= remaining {
				out.Items = append(out.Items, item)
				remaining -= item.EstimatedTokens
				continue
			}
			// Doesn't fit at this tier's preferred granularity: fall back to
			// L2 (a bare reference) before giving up on it entirely, per
			// DESIGN §8's "切り捨て分も必ず L2 参照で列挙".
			ref := renderItem(c, GranularityL2)
			if granularity != GranularityL2 && ref.EstimatedTokens <= remaining {
				out.Items = append(out.Items, ref)
				remaining -= ref.EstimatedTokens
				continue
			}
			out.TruncatedRefs = append(out.TruncatedRefs, ref)
		}
	}

	out.UsedTokens = budget - remaining
	return out
}

// renderItem builds the ContextItem for b at granularity, including its
// token cost.
func renderItem(c candidate, granularity Granularity) ContextItem {
	b := c.b
	item := ContextItem{
		ID:          b.ID,
		Type:        b.Type,
		Timestamp:   b.Timestamp,
		Provenance:  c.provenance,
		Granularity: granularity,
		ViaLinkID:   c.viaLinkID,
		LinkDepth:   c.linkDepth,
	}
	switch granularity {
	case GranularityL0:
		item.Text = renderL0(b)
	case GranularityL1:
		item.Text = renderL1(b)
	case GranularityL2:
		item.Text = ""
	}
	// Every item pays a fixed ID/type/timestamp overhead (the L2 reference
	// is never free, even for L0/L1 items) plus the token cost of its Text.
	item.EstimatedTokens = referenceOverheadTokens + EstimateTokens(item.Text)
	return item
}

// RefreshContextItem re-renders an existing context slot from a replacement
// Bead while preserving traversal provenance and granularity. MCP status
// normalization uses this when an amended Bead is substituted with its
// current version; changing only the ID would otherwise label stale text as
// the corrected record.
func RefreshContextItem(item ContextItem, replacement bead.Bead) ContextItem {
	return renderItem(candidate{
		b:          replacement,
		provenance: item.Provenance,
		viaLinkID:  item.ViaLinkID,
		linkDepth:  item.LinkDepth,
	}, item.Granularity)
}

// AsL2Reference demotes an already-rendered item to the fixed-cost reference
// shape while preserving ID and traversal provenance.
func AsL2Reference(item ContextItem) ContextItem {
	item.Granularity = GranularityL2
	item.Text = ""
	item.EstimatedTokens = referenceOverheadTokens
	return item
}

// referenceOverheadTokens is a fixed per-item cost approximating what
// specs/DESIGN_v3.md §8 calls the ~15-token L2 shape (ID + type +
// timestamp) that every item — L0, L1, or L2 — carries regardless of how
// much Text it also includes.
const referenceOverheadTokens = 15

// renderL0 renders complete Content as deterministic JSON. The former
// string-only flattening silently dropped JSON numbers and booleans, most
// importantly valueQuantity.value for labs and vitals, so an L0 item could
// omit the clinical result it claimed to carry. Bead Content is JSON by
// contract and encoding/json sorts map keys, preserving both completeness
// and deterministic token accounting.
func renderL0(b bead.Bead) string {
	raw, err := json.Marshal(b.Content)
	if err == nil {
		return b.Type + ": " + string(raw)
	}

	// Defensive fallback for an in-memory non-JSON value. Persisted Beads
	// have already passed canonical JSON encoding and cannot reach this path.
	var parts []string
	collectContentStrings(b.Content, &parts)
	// collectContentStrings walks a map, whose key iteration order Go
	// randomizes; sort so renderL0's output (and therefore its token cost
	// and the audit-facing text an agent sees) is deterministic across
	// repeated calls for the same Bead, mirroring
	// index.DefaultFlattener.Flatten's identical reasoning.
	sort.Strings(parts)

	text := b.Type
	if len(parts) > 0 {
		text = b.Type + ": "
		for i, p := range parts {
			if i > 0 {
				text += " "
			}
			text += p
		}
	}
	return text
}

// renderL1 renders a Bead's 1-line summary: beads.summary if the caller
// populated it (see index.Flattener), falling back to renderL0's text
// truncated to keep L1 meaningfully shorter than L0 when no summary is
// available (e.g. a Bead ingested before any Flattener ran). Bundle does not
// carry beads.summary today (LoadBundle reads only the Pod, not the index —
// see LoadBundle's doc comment), so this always takes the fallback path
// currently; the summary-aware branch is kept because BuildContext's
// contract (an L1 tier distinct from L0) should not silently regress once a
// caller wires beads.summary through.
func renderL1(b bead.Bead) string {
	// Keep L1 compact and prose-like rather than taking an arbitrary prefix
	// of the L0 JSON object. Direct clinical-link endpoints are L0, so their
	// numeric facts are still complete.
	var parts []string
	collectContentStrings(b.Content, &parts)
	sort.Strings(parts)
	full := b.Type
	if len(parts) > 0 {
		full += ": " + parts[0]
	}
	const l1MaxLen = 160 // keeps L1 well under L0 (see EstimateTokens' ~40 tok target)
	if len(full) <= l1MaxLen {
		return full
	}
	return full[:l1MaxLen]
}

// collectContentStrings recursively appends every string value reachable
// from v (mirrors index.collectStrings; duplicated here since that helper is
// unexported in a different package and graph does not otherwise depend on
// index for rendering).
func collectContentStrings(v any, out *[]string) {
	switch val := v.(type) {
	case string:
		if val != "" {
			*out = append(*out, val)
		}
	case map[string]any:
		for _, elem := range val {
			collectContentStrings(elem, out)
		}
	case []any:
		for _, elem := range val {
			collectContentStrings(elem, out)
		}
	default:
	}
}

// EstimateTokens returns a crude token-count estimate for s: UTF-8 byte
// length divided by 3. This is a budget-control heuristic only — per
// specs/DESIGN_v3.md §8's L0/L1/L2 byte-budget tiers (~500/~40/~15 tok), the
// exact tokenizer a given LLM uses is not this package's concern. Real
// measurement (actual tokenizer, actual model) is a benchmarking job (see
// bench/), not something BuildContext should depend on; this estimate only
// needs to be a stable, monotonic-in-length proxy so greedy packing makes
// consistent decisions.
func EstimateTokens(s string) int {
	return len(s) / 3
}
