package graph

import (
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
	ProvenanceAnchor     Provenance = "anchor"
	ProvenanceAncestor   Provenance = "ancestor"
	ProvenanceSibling    Provenance = "sibling"
	ProvenanceDescendant Provenance = "descendant"
)

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
}

// tierGranularity is the granularity BuildContext attempts first for each
// priority tier, per specs/DESIGN_v3.md §8's "anchor L0 → 祖先 L1 →
// explicit siblings L1 → implicit(エッジ)siblings L2 → 子孫 L2" ordering
// (the sibling_link-description tier from DESIGN §8 is omitted: the APC
// daemon that would produce sibling_link Beads is not implemented yet —
// docs/requirements.md R5 — so there is no real data for it; explicit
// siblings here means Beads linked via Bundle.AddSiblingEdge /
// edge_type='sibling', not a description tier).
var tierGranularity = []Granularity{
	GranularityL0, // tier 0: anchors
	GranularityL1, // tier 1: ancestors
	GranularityL1, // tier 2: explicit siblings
	GranularityL2, // tier 3: implicit siblings
	GranularityL2, // tier 4: descendants
}

const (
	tierAnchor = iota
	tierAncestor
	tierExplicitSibling
	tierImplicitSibling
	tierDescendant
)

// ContextOption customizes a single BuildContext call. Options are additive
// (new callers opt in; existing positional callers that pass none keep
// today's behavior unchanged) — see WithSiblings.
type ContextOption func(*contextOptions)

// contextOptions holds BuildContext's optional knobs, defaulted so the zero
// value matches BuildContext's pre-existing (siblings-included) behavior.
type contextOptions struct {
	includeSiblings bool
}

func newContextOptions(opts []ContextOption) contextOptions {
	o := contextOptions{includeSiblings: true}
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// WithSiblings controls whether BuildContext's explicit-sibling and
// implicit-sibling tiers (tierExplicitSibling/tierImplicitSibling) are
// populated at all. Defaults to true (existing behavior). Pass
// WithSiblings(false) to skip both sibling tiers entirely — e.g. bench/'s
// dag_nosib retrieval arm (docs/requirements.md R8.2), which needs a DAG
// bundle that walks ancestors/descendants but never sibling_link edges, to
// isolate sibling_link's contribution to retrieval quality from the rest of
// DAG traversal.
func WithSiblings(include bool) ContextOption {
	return func(o *contextOptions) { o.includeSiblings = include }
}

// BuildContext assembles a token-budgeted ContextBundle for anchors within
// bd, per specs/DESIGN_v3.md §8: starting from anchors (full content, L0),
// then ancestors (L1 summary), explicit siblings (L1 summary), implicit
// siblings (L2 reference), and descendants (L2 reference) — each tier's
// Beads greedily packed in tier order until budget (a token count, see
// EstimateTokens) is exhausted. A Bead reachable through more than one
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
//
// opts customizes this call; see ContextOption/WithSiblings. Omitting opts
// keeps BuildContext's original (siblings-included) behavior, so every
// pre-existing call site is unaffected by this parameter's addition.
func BuildContext(bd *Bundle, anchors []string, budget int, ancestorDepth, descendantDepth int, opts ...ContextOption) ContextBundle {
	o := newContextOptions(opts)
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

	resolve := func(id string, tier int, provenance Provenance) {
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
		claims[id] = candidate{b: b, provenance: provenance, tier: tier}
	}

	// Anchors first, so they always win the highest tier regardless of
	// whether some other anchor's ancestor/descendant walk also reaches
	// them.
	for _, id := range anchors {
		resolve(id, tierAnchor, ProvenanceAnchor)
	}
	for _, id := range anchors {
		for _, a := range bd.Ancestors(id, ancestorDepth) {
			if a.ID == id {
				continue // Ancestors includes the anchor itself at depth 0
			}
			resolve(a.ID, tierAncestor, ProvenanceAncestor)
		}
		if o.includeSiblings {
			for _, sib := range bd.siblings[id] {
				resolve(sib, tierExplicitSibling, ProvenanceSibling)
			}
			for _, sib := range implicitSiblingsOnly(bd, id) {
				resolve(sib, tierImplicitSibling, ProvenanceSibling)
			}
		}
		for _, d := range bd.Descendants(id, descendantDepth) {
			if d.ID == id {
				continue // Descendants includes the anchor itself at depth 0
			}
			resolve(d.ID, tierDescendant, ProvenanceDescendant)
		}
	}

	// Pass 2: materialize tiers[] once, from each Bead's single resolved
	// candidate — every Bead ID appears in exactly one tiers[] slice.
	tiers := make([][]candidate, 5)
	for _, c := range claims {
		tiers[c.tier] = append(tiers[c.tier], c)
	}

	// Deterministic ordering within a tier (map iteration over claims is
	// not stable) so BuildContext's output — and therefore which items get
	// truncated first under a tight budget — does not vary run to run.
	for _, t := range tiers {
		sort.Slice(t, func(i, j int) bool {
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
			item := renderItem(c.b, c.provenance, granularity)
			if item.EstimatedTokens <= remaining {
				out.Items = append(out.Items, item)
				remaining -= item.EstimatedTokens
				continue
			}
			// Doesn't fit at this tier's preferred granularity: fall back to
			// L2 (a bare reference) before giving up on it entirely, per
			// DESIGN §8's "切り捨て分も必ず L2 参照で列挙".
			ref := renderItem(c.b, c.provenance, GranularityL2)
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

// implicitSiblingsOnly returns id's implicit siblings (same-parent
// children) without the explicit-sibling tier Bundle.Siblings also folds
// in, so BuildContext can rank the two tiers separately per DESIGN §8.
func implicitSiblingsOnly(bd *Bundle, id string) []string {
	seen := map[string]bool{id: true}
	var out []string
	for _, parentID := range bd.parents[id] {
		for _, childID := range bd.children[parentID] {
			if seen[childID] {
				continue
			}
			seen[childID] = true
			out = append(out, childID)
		}
	}
	return out
}

// renderItem builds the ContextItem for b at granularity, including its
// token cost.
func renderItem(b bead.Bead, provenance Provenance, granularity Granularity) ContextItem {
	item := ContextItem{
		ID:          b.ID,
		Type:        b.Type,
		Timestamp:   b.Timestamp,
		Provenance:  provenance,
		Granularity: granularity,
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

// referenceOverheadTokens is a fixed per-item cost approximating what
// specs/DESIGN_v3.md §8 calls the ~15-token L2 shape (ID + type +
// timestamp) that every item — L0, L1, or L2 — carries regardless of how
// much Text it also includes.
const referenceOverheadTokens = 15

// renderL0 renders a Bead's full content, per DESIGN §8's L0 tier: this
// deliberately reuses the same flattening approach as
// index.DefaultFlattener.Flatten's search_text side (recursively join every
// string value in Content) rather than a raw JSON dump, so L0 text is
// human/LLM-readable prose rather than punctuation-heavy JSON that would
// inflate the token estimate without adding clinical information. A
// type-specific flattener (a later, FHIR-aware unit) can replace this
// without changing BuildContext's shape.
func renderL0(b bead.Bead) string {
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
	full := renderL0(b)
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
