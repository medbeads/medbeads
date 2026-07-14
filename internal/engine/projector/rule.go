package projector

import (
	"fmt"
	"math"
	"sort"

	"github.com/medbeads/medbeads/internal/engine/bead"
	"github.com/medbeads/medbeads/internal/engine/index"
)

// New rules are written as v2. The v1 decoder remains supported because old
// knowledge Beads are immutable and must stay reproducible after an upgrade.
const (
	linkRuleSchemaV1 = "medbeads.link_rule.v1"
	linkRuleSchema   = "medbeads.link_rule.v2"
)

func supportedLinkRuleSchema(schema string) bool {
	return schema == linkRuleSchemaV1 || schema == linkRuleSchema
}

// linkRuleType is the Bead.Type value a link_rule Bead is ingested as. It is
// ordinary Bead content (not hash-excluded), content-addressed like any
// other Bead — the rule's own Bead ID becomes clinical_links.rule_version
// (the "知識世代" — specs/U2_projection_schema.md's schema comment on that
// column).
const linkRuleType = "link_rule"

// CooccurrenceRuleID is the human-readable, stable rule_id (content.rule_id)
// of this package's one built-in cooccurrence rule — distinct from the
// rule's Bead ID (which is content-derived and therefore changes if the
// rule's content ever changes; rule_id is the stable key across such
// revisions, per clinical_links.rule_id's schema comment).
const CooccurrenceRuleID = "cooccurrence-risk-atc-v1"

// relationClinicalCorrelation mirrors package apc's identical constant
// (apc/link.go): the only relation this generic cooccurrence rule can
// honestly claim, per specs/MEDBEADS_SIBLING_SPEC.md §4.3's "臨床的関連"
// general-purpose bucket.
const relationClinicalCorrelation = "clinical_correlation"

// severityInfo / evidenceBasisCooccurrence are the two fixed values every
// row this package's cooccurrence rule produces must carry — clinical_links'
// own CHECK constraint (migrations/0006_projection_v31.sql) enforces this at
// the database level; these constants exist so every write site in this
// package spells the same literal, not to duplicate that enforcement.
const (
	severityInfo              = "info"
	evidenceBasisCooccurrence = "cooccurrence"

	// evidenceBasisCuratedKnowledge is what a curated_pair rule stamps on the
	// links it produces. clinical_links' CHECK constraint accepts a severity
	// above `info` only for this basis (or 'guideline'), and only when the row
	// also names a rule_version and a non-empty evidence_bead_ids — so a
	// clinical warning structurally cannot exist without naming the knowledge
	// that justifies it.
	evidenceBasisCuratedKnowledge = "curated_knowledge"
)

// rule_family values. The family decides how a link_rule Bead's trigger is
// interpreted, and therefore which projector runs it:
//
//   - cooccurrence: two Beads share a tag in a trigger namespace. Statistical,
//     capped at severity=info.
//   - curated_pair: two Beads carry the two tags of a hand-curated pair. This
//     is a clinical claim, and may carry severity above info precisely because
//     it names the knowledge Bead that asserts it.
const (
	ruleFamilyCooccurrence = "cooccurrence"
	ruleFamilyCuratedPair  = "curated_pair"
)

// triggerNamespaces is the ordered set of bead_tags namespace prefixes (see
// antigen/extract.go's prefix constants) whose shared occurrence between two
// Beads in the same patient can trigger a cooccurrence link. It deliberately
// excludes "loinc:" and "temporal:" — dropping same-LOINC-code and
// temporal-only cooccurrence is the whole point of U3b (specs/
// U3_link_projector.md: "LOINC 同一コード・temporal 単独をトリガー除外(87%
// ノイズ根絶)").
//
// This slice's ORDER is itself knowledge, not incidental: it is
// projectPatientLinks' cap-consumption priority (see project.go's pairing
// loop) — when a Bead's maxLinksPerBead cap binds across more than one
// trigger namespace, the namespace listed earlier here wins the contested
// slots. risk: is listed first (ahead of atc: and rxnorm:) because it is
// this rule's clinically load-bearing namespace: the v2 post-mortem
// (docs/reviews/2026-07-10_scheme_critique_A_internal.md) found v2's whole
// failure was clinically valuable risk: links drowning in bulk rxnorm:
// noise, and letting cap consumption default to alphabetical order (atc: <
// risk: < rxnorm:) reproduced that exact failure, just deterministically
// instead of randomly. Because array element order survives JCS
// canonicalization unchanged (only object member order is normalized),
// this slice's order is part of BuildCooccurrenceRuleBead's content and
// therefore part of the rule Bead's own content-addressed ID
// (rule_version): revising this priority means editing this literal,
// which mints a new rule Bead ID, which a caller must re-seed and
// re-project against — never a live rewrite of an existing rule_version's
// meaning.
var triggerNamespaces = []string{"risk:", "atc:", "rxnorm:"}

// excludedSameCodeNamespaces mirrors the rule content's
// trigger.excludes.same_code_namespaces: a cooccurrence trigger is also
// excluded when the *only* thing two Beads share is the same code within
// one of these namespaces (loinc: same-code cooccurrence, e.g. two lab
// results both coded loinc:12345, is not itself clinically informative
// enough to link on — the classic "same test measured twice" noise pattern).
var excludedSameCodeNamespaces = []string{"loinc:"}

// LinkRule is the decoded, ready-to-use form of a link_rule Bead's content:
// everything the projector needs to decide which tag-sharing pairs trigger
// a link and what relation/severity/evidence_basis to stamp on the result.
// RuleVersion is the link_rule Bead's own ID (content hash) — clinical_links.
// rule_version's value (specs/U2_projection_schema.md's "= ルール Bead ID
// (知識世代)").
type LinkRule struct {
	RuleVersion                string
	RuleID                     string
	TriggerNamespaces          []string
	MinShared                  int
	ExcludedSameCodeNamespaces []string
	Relation                   string
	Severity                   string
	EvidenceBasis              string
	FrequencyThreshold         float64
	MaxLinksPerBead            int
	SharedTagWeight            float64
}

// CuratedPairRule is the decoded form of a rule_family="curated_pair"
// link_rule Bead: a hand-curated statement that two specific clinical concepts,
// each named by a tag, are related — a drug-drug interaction, a
// contraindication — with a severity above `info`.
//
// This is the other half of the severity story. A cooccurrence rule can only
// ever assert `info`, because "these two records appeared together" is a
// statistical observation, not a clinical claim. clinical_links' CHECK
// constraint (migrations/0006_projection_v31.sql) enforces exactly that: any
// severity above info REQUIRES evidence_basis IN
// ('curated_knowledge','guideline') AND a non-null rule_version AND a non-empty
// evidence_bead_ids. So a warning cannot be written unless it can name the
// knowledge Bead that justifies it — escalation must be earned. A CuratedPairRule
// is how that knowledge is expressed, and because it is itself a
// content-addressed Bead, the warning it produces is auditable back to the exact
// text of the rule that asserted it.
//
// TagPairs holds ordered [tagA, tagB] couples. The projector links two Beads in
// the same patient when one carries tagA and the other tagB (in either
// direction — the pair is a set, not a direction).
type CuratedPairRule struct {
	RuleVersion     string
	RuleID          string
	RevisionLabel   string
	TagPairs        [][2]string
	Relation        string
	Severity        string
	EvidenceBasis   string
	EvidenceBeadIDs []string
	EffectiveFrom   string
	EffectiveTo     string
}

// BuildCooccurrenceRuleBead returns the unsaved, ID-less link_rule Bead for
// this package's one built-in cooccurrence rule (specs/U3_link_projector.md's
// worked example content). Content is fully canonicalized (sorted
// slices/maps only, no map iteration order ever reaching the hash payload)
// so bead.ComputeID is deterministic across repeated calls and across
// processes — the same discipline apc/link.go's joinAntigens/buildSiblingLinkBead
// already follow for their own hash-target content.
//
// Timestamp is a caller-supplied deterministic value (not time.Now()), for
// the identical reason apc/link.go's buildSiblingLinkBead uses max(a,b)
// timestamp rather than wall-clock: a knowledge Bead's ID must not depend on
// when it happened to be minted, only on its content, so that seeding the
// same rule twice (e.g. two independent bootstraps of a fresh store)
// produces the byte-identical Bead ID and rule_version. Callers that want
// "now" should pass a literal RFC3339 string derived once, at the call site
// that decides it, not compute it fresh on every retry.
func BuildCooccurrenceRuleBead(timestamp string) bead.Bead {
	trigger := map[string]any{
		"tag_namespaces": append([]string(nil), triggerNamespaces...),
		"min_shared":     1,
		"excludes": map[string]any{
			"same_code_namespaces": append([]string(nil), excludedSameCodeNamespaces...),
		},
	}
	scoreModel := map[string]any{
		"weights": map[string]any{
			"shared_tag": 1,
		},
	}
	content := map[string]any{
		"schema":         linkRuleSchema,
		"rule_id":        CooccurrenceRuleID,
		"rule_family":    ruleFamilyCooccurrence,
		"trigger":        trigger,
		"relation":       relationClinicalCorrelation,
		"severity":       severityInfo,
		"evidence_basis": evidenceBasisCooccurrence,
		"score_model":    scoreModel,
		"execution": map[string]any{
			"frequency_threshold": frequencyThreshold,
			"max_links_per_bead":  maxLinksPerBead,
		},
	}
	return bead.Bead{
		Type:      linkRuleType,
		Timestamp: timestamp,
		Author:    "projector_seed",
		Content:   content,
	}
}

// BuildCuratedPairRuleBead returns the unsaved, ID-less link_rule Bead for a
// curated pair rule. Like BuildCooccurrenceRuleBead, content is fully
// canonical (sorted, no map iteration order reaching the hash payload) and the
// timestamp is caller-supplied, so the same rule content always mints the same
// Bead ID — publishing the same knowledge twice is idempotent, and the
// resulting rule_version is reproducible from the content alone.
//
// tagPairs is normalized (each pair sorted, then the list sorted) so that
// declaring {A,B} and {B,A} produce the byte-identical Bead. The pair is a set:
// the projector matches it in either direction.
func BuildCuratedPairRuleBead(ruleID, relation, severity string, tagPairs [][2]string, timestamp string) bead.Bead {
	normalized := make([][2]string, 0, len(tagPairs))
	for _, p := range tagPairs {
		a, b := p[0], p[1]
		if b < a {
			a, b = b, a
		}
		normalized = append(normalized, [2]string{a, b})
	}
	sort.Slice(normalized, func(i, j int) bool {
		if normalized[i][0] != normalized[j][0] {
			return normalized[i][0] < normalized[j][0]
		}
		return normalized[i][1] < normalized[j][1]
	})

	pairs := make([]any, 0, len(normalized))
	for _, p := range normalized {
		pairs = append(pairs, []any{p[0], p[1]})
	}

	content := map[string]any{
		"schema":      linkRuleSchema,
		"rule_id":     ruleID,
		"rule_family": ruleFamilyCuratedPair,
		"trigger": map[string]any{
			"tag_pairs": pairs,
		},
		"relation": relation,
		"severity": severity,
		// A curated rule is, by definition, curated knowledge. This value is
		// what lets clinical_links' CHECK constraint accept a severity above
		// info at all — and the rule Bead's own ID lands in evidence_bead_ids,
		// so the warning names its justification.
		"evidence_basis": evidenceBasisCuratedKnowledge,
	}
	return bead.Bead{
		Type:      linkRuleType,
		Timestamp: timestamp,
		Author:    "projector_seed",
		Content:   content,
	}
}

// LoadCuratedPairRules returns every rule_family="curated_pair" link_rule Bead
// in idx's shared Pod, decoded, in deterministic (Bead ID) order.
//
// knowledgeBeadIDs, when non-empty, restricts the set exactly as it does for
// LoadActiveCooccurrenceRule: only Beads the caller explicitly named are
// considered. This is what makes a projection's inputs a closed, declared set
// rather than "whatever happens to be in the store" — the projection_manifest
// records those IDs, so a projection generation can always be reproduced from
// the same facts plus the same named knowledge.
//
// Unlike the cooccurrence rule (of which exactly one is active), curated rules
// are additive: a store may carry many, and every one of them contributes links.
// Returning an empty slice is not an error — a store with no curated knowledge
// simply produces no links above `info`.
func LoadCuratedPairRules(idx *index.DB, getContent func(id string) (map[string]any, error), knowledgeBeadIDs ...string) ([]CuratedPairRule, error) {
	refs, err := idx.ListSharedBeads()
	if err != nil {
		return nil, fmt.Errorf("projector: load curated_pair rules: %w", err)
	}

	var allowed map[string]bool
	if len(knowledgeBeadIDs) > 0 {
		allowed = make(map[string]bool, len(knowledgeBeadIDs))
		for _, id := range knowledgeBeadIDs {
			allowed[id] = true
		}
	}

	var ids []string
	contents := make(map[string]map[string]any)
	for _, ref := range refs {
		if ref.Type != linkRuleType {
			continue
		}
		if allowed != nil && !allowed[ref.ID] {
			continue
		}
		content, err := getContent(ref.ID)
		if err != nil {
			return nil, fmt.Errorf("projector: load curated_pair rule %s: %w", ref.ID, err)
		}
		if schema, _ := content["schema"].(string); !supportedLinkRuleSchema(schema) {
			continue
		}
		if family, _ := content["rule_family"].(string); family != ruleFamilyCuratedPair {
			continue
		}
		ids = append(ids, ref.ID)
		contents[ref.ID] = content
	}

	// Deterministic order: the projector's output must not depend on the order
	// ListSharedBeads happened to return.
	sort.Strings(ids)

	out := make([]CuratedPairRule, 0, len(ids))
	for _, id := range ids {
		rule, err := decodeCuratedPairRule(id, contents[id])
		if err != nil {
			return nil, err
		}
		for _, evidenceID := range rule.EvidenceBeadIDs {
			if _, err := getContent(evidenceID); err != nil {
				return nil, fmt.Errorf("projector: curated_pair rule %s evidence Bead %s is unavailable: %w", id, evidenceID, err)
			}
		}
		out = append(out, rule)
	}
	return out, nil
}

// decodeCuratedPairRule extracts a CuratedPairRule from a link_rule Bead's
// content, tolerating both the in-process shape ([][2]string built here) and
// the []any-of-[]any shape a Bead round-tripped through a Pod frame carries —
// the same duality decodeLinkRule handles for tag_namespaces.
func decodeCuratedPairRule(ruleBeadID string, content map[string]any) (CuratedPairRule, error) {
	ruleID, _ := content["rule_id"].(string)
	revisionLabel, _ := content["revision_label"].(string)
	relation, _ := content["relation"].(string)
	severity, _ := content["severity"].(string)
	evidenceBasis, _ := content["evidence_basis"].(string)

	trigger, ok := content["trigger"].(map[string]any)
	if !ok {
		return CuratedPairRule{}, fmt.Errorf("projector: decode curated_pair rule %s: content.trigger missing or malformed", ruleBeadID)
	}

	pairs, err := decodeTagPairs(trigger["tag_pairs"])
	if err != nil {
		return CuratedPairRule{}, fmt.Errorf("projector: decode curated_pair rule %s: trigger.tag_pairs: %w", ruleBeadID, err)
	}
	if len(pairs) == 0 {
		return CuratedPairRule{}, fmt.Errorf("projector: decode curated_pair rule %s: trigger.tag_pairs is empty", ruleBeadID)
	}
	if ruleID == "" {
		return CuratedPairRule{}, fmt.Errorf("projector: decode curated_pair rule %s: content.rule_id is required", ruleBeadID)
	}
	if relation == "" {
		return CuratedPairRule{}, fmt.Errorf("projector: decode curated_pair rule %s: content.relation is required", ruleBeadID)
	}
	if severity != "info" && severity != "warning" && severity != "alert" && severity != "critical" {
		return CuratedPairRule{}, fmt.Errorf("projector: decode curated_pair rule %s: unsupported severity %q", ruleBeadID, severity)
	}
	if evidenceBasis != evidenceBasisCuratedKnowledge && evidenceBasis != "guideline" {
		return CuratedPairRule{}, fmt.Errorf("projector: decode curated_pair rule %s: unsupported evidence_basis %q", ruleBeadID, evidenceBasis)
	}
	for _, pair := range pairs {
		if pair[0] == "" || pair[1] == "" || pair[0] == pair[1] {
			return CuratedPairRule{}, fmt.Errorf("projector: decode curated_pair rule %s: invalid tag pair %q/%q", ruleBeadID, pair[0], pair[1])
		}
	}
	evidenceIDs, err := decodeStringSlice(content["evidence_bead_ids"])
	if err != nil {
		return CuratedPairRule{}, fmt.Errorf("projector: decode curated_pair rule %s: evidence_bead_ids: %w", ruleBeadID, err)
	}
	sort.Strings(evidenceIDs)
	evidenceIDs = deduplicateSortedStrings(evidenceIDs)
	var effectiveFrom, effectiveTo string
	if period, ok := content["effective_period"].(map[string]any); ok {
		effectiveFrom, _ = period["from"].(string)
		effectiveTo, _ = period["to"].(string)
		if effectiveFrom != "" && effectiveTo != "" && effectiveTo < effectiveFrom {
			return CuratedPairRule{}, fmt.Errorf("projector: decode curated_pair rule %s: effective_period.to precedes from", ruleBeadID)
		}
	}

	return CuratedPairRule{
		RuleVersion:     ruleBeadID,
		RuleID:          ruleID,
		RevisionLabel:   revisionLabel,
		TagPairs:        pairs,
		Relation:        relation,
		Severity:        severity,
		EvidenceBasis:   evidenceBasis,
		EvidenceBeadIDs: evidenceIDs,
		EffectiveFrom:   effectiveFrom,
		EffectiveTo:     effectiveTo,
	}, nil
}

func deduplicateSortedStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := values[:1]
	for _, value := range values[1:] {
		if value != out[len(out)-1] {
			out = append(out, value)
		}
	}
	return out
}

// decodeTagPairs accepts [][2]string (in-process) or []any of 2-element []any
// (Pod round-trip). Each pair is normalized to sorted order so a rule declaring
// {B,A} behaves identically to one declaring {A,B}.
func decodeTagPairs(raw any) ([][2]string, error) {
	var out [][2]string

	appendPair := func(a, b string) {
		if b < a {
			a, b = b, a
		}
		out = append(out, [2]string{a, b})
	}

	switch v := raw.(type) {
	case nil:
		return nil, nil
	case [][2]string:
		for _, p := range v {
			appendPair(p[0], p[1])
		}
	case []any:
		for _, elem := range v {
			pair, ok := elem.([]any)
			if !ok || len(pair) != 2 {
				return nil, fmt.Errorf("each pair must be a 2-element array, got %v", elem)
			}
			a, aok := pair[0].(string)
			b, bok := pair[1].(string)
			if !aok || !bok {
				return nil, fmt.Errorf("each pair must hold two strings, got %v", pair)
			}
			appendPair(a, b)
		}
	default:
		return nil, fmt.Errorf("unsupported shape %T", raw)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i][0] != out[j][0] {
			return out[i][0] < out[j][0]
		}
		return out[i][1] < out[j][1]
	})
	return out, nil
}

// LoadActiveCooccurrenceRule finds the link_rule Bead (type="link_rule",
// content.schema=linkRuleSchema, content.rule_id=CooccurrenceRuleID) among
// idx's shared-Pod Beads and decodes it into a LinkRule. It returns
// index.ErrNotFound if no such Bead has been seeded yet — a projector caller
// (Reproject) must treat that as "nothing to project" rather than crashing,
// since a fresh store legitimately has no link_rule Bead until one is
// explicitly seeded (BuildCooccurrenceRuleBead + engine.Ingest).
//
// knowledgeBeadIDs, when non-empty, restricts which candidate Bead IDs are
// even considered: a candidate whose ID is not in this set is skipped
// before the "greatest ID wins" comparison below, so the manifest/caller's
// explicitly-consulted set always wins over any other same-rule_id revision
// that happens to exist in the shared Pod but was not named (specs/
// U4_state_derivation.md's U3 follow-up — this is the exact fix for
// "辞書順最大 ID 勝ち" silently ignoring which rule the manifest declared).
// An empty knowledgeBeadIDs disables this filter entirely (every candidate
// is considered), preserving this function's original "greatest ID among
// every matching Bead wins" behavior for callers that have not adopted
// explicit rule selection yet.
//
// Among the (possibly filtered) remaining candidates, the lexicographically
// greatest Bead ID wins deterministically (ties are impossible in practice
// since two byte-identical rule contents collapse to the same
// content-addressed ID).
func LoadActiveCooccurrenceRule(idx *index.DB, getContent func(id string) (map[string]any, error), knowledgeBeadIDs ...string) (LinkRule, error) {
	refs, err := idx.ListSharedBeads()
	if err != nil {
		return LinkRule{}, fmt.Errorf("projector: load link_rule: %w", err)
	}

	var allowed map[string]bool
	if len(knowledgeBeadIDs) > 0 {
		allowed = make(map[string]bool, len(knowledgeBeadIDs))
		for _, id := range knowledgeBeadIDs {
			allowed[id] = true
		}
	}

	var bestID string
	var bestContent map[string]any
	for _, ref := range refs {
		if ref.Type != linkRuleType {
			continue
		}
		if allowed != nil && !allowed[ref.ID] {
			continue
		}
		content, err := getContent(ref.ID)
		if err != nil {
			return LinkRule{}, fmt.Errorf("projector: load link_rule %s: %w", ref.ID, err)
		}
		schema, _ := content["schema"].(string)
		if !supportedLinkRuleSchema(schema) {
			continue
		}
		ruleID, _ := content["rule_id"].(string)
		if ruleID != CooccurrenceRuleID {
			continue
		}
		if bestID == "" || ref.ID > bestID {
			bestID = ref.ID
			bestContent = content
		}
	}
	if bestID == "" {
		return LinkRule{}, fmt.Errorf("projector: load link_rule: no %s Bead with rule_id=%s: %w",
			linkRuleType, CooccurrenceRuleID, index.ErrNotFound)
	}

	return decodeLinkRule(bestID, bestContent)
}

// decodeLinkRule extracts the fields LoadActiveCooccurrenceRule needs from a
// link_rule Bead's content, tolerating the []any-of-strings shape a Bead
// decoded back from a Pod frame carries (json.Unmarshal into
// map[string]any) rather than the []string a freshly-built
// BuildCooccurrenceRuleBead value holds in-process — the same duality
// index/write.go's siblingLinkMatchedAntigens already handles for
// content.matched_antigens.
func decodeLinkRule(ruleBeadID string, content map[string]any) (LinkRule, error) {
	ruleID, _ := content["rule_id"].(string)
	relation, _ := content["relation"].(string)
	severity, _ := content["severity"].(string)
	evidenceBasis, _ := content["evidence_basis"].(string)

	trigger, ok := content["trigger"].(map[string]any)
	if !ok {
		return LinkRule{}, fmt.Errorf("projector: decode link_rule %s: content.trigger missing or malformed", ruleBeadID)
	}
	namespaces, err := decodeStringSlice(trigger["tag_namespaces"])
	if err != nil {
		return LinkRule{}, fmt.Errorf("projector: decode link_rule %s: trigger.tag_namespaces: %w", ruleBeadID, err)
	}
	minShared, err := decodeInt(trigger["min_shared"])
	if err != nil {
		return LinkRule{}, fmt.Errorf("projector: decode link_rule %s: trigger.min_shared: %w", ruleBeadID, err)
	}
	if minShared == 0 {
		minShared = 1 // v1 compatibility
	}

	var excludedSameCode []string
	if excludes, ok := trigger["excludes"].(map[string]any); ok {
		excludedSameCode, err = decodeStringSlice(excludes["same_code_namespaces"])
		if err != nil {
			return LinkRule{}, fmt.Errorf("projector: decode link_rule %s: trigger.excludes.same_code_namespaces: %w", ruleBeadID, err)
		}
	}

	// namespaces is intentionally NOT sorted here: its declared order is
	// itself knowledge — projectPatientLinks' cap-consumption priority
	// follows rule.TriggerNamespaces' order exactly (see project.go's
	// pairing-loop comment), so this decode step must preserve whatever
	// order the rule Bead's own content.trigger.tag_namespaces declared,
	// not silently normalize it away. ExcludedSameCodeNamespaces, in
	// contrast, is only ever tested for set membership
	// (excludedBySameCodeOnly), so its order carries no meaning and sorting
	// it is harmless (kept for readability/reproducible logging only).
	sort.Strings(excludedSameCode)

	frequency := frequencyThreshold
	maxLinks := maxLinksPerBead
	if execution, ok := content["execution"].(map[string]any); ok {
		if raw, exists := execution["frequency_threshold"]; exists {
			frequency, err = decodeFloat(raw)
			if err != nil {
				return LinkRule{}, fmt.Errorf("projector: decode link_rule %s: execution.frequency_threshold: %w", ruleBeadID, err)
			}
		}
		if raw, exists := execution["max_links_per_bead"]; exists {
			maxLinks, err = decodeInt(raw)
			if err != nil {
				return LinkRule{}, fmt.Errorf("projector: decode link_rule %s: execution.max_links_per_bead: %w", ruleBeadID, err)
			}
		}
	}

	sharedTagWeight := 1.0
	if scoreModel, ok := content["score_model"].(map[string]any); ok {
		if weights, ok := scoreModel["weights"].(map[string]any); ok {
			if raw, exists := weights["shared_tag"]; exists {
				sharedTagWeight, err = decodeFloat(raw)
				if err != nil {
					return LinkRule{}, fmt.Errorf("projector: decode link_rule %s: score_model.weights.shared_tag: %w", ruleBeadID, err)
				}
			}
		}
	}

	if ruleID == "" || relation == "" || len(namespaces) == 0 {
		return LinkRule{}, fmt.Errorf("projector: decode link_rule %s: rule_id, relation and trigger.tag_namespaces are required", ruleBeadID)
	}
	if minShared < 1 {
		return LinkRule{}, fmt.Errorf("projector: decode link_rule %s: trigger.min_shared must be >= 1", ruleBeadID)
	}
	if severity != severityInfo || evidenceBasis != evidenceBasisCooccurrence {
		return LinkRule{}, fmt.Errorf("projector: decode link_rule %s: cooccurrence rules require severity=info and evidence_basis=cooccurrence", ruleBeadID)
	}
	if frequency <= 0 || frequency > 1 {
		return LinkRule{}, fmt.Errorf("projector: decode link_rule %s: execution.frequency_threshold must be in (0,1]", ruleBeadID)
	}
	if maxLinks < 1 || maxLinks > 10000 {
		return LinkRule{}, fmt.Errorf("projector: decode link_rule %s: execution.max_links_per_bead must be in [1,10000]", ruleBeadID)
	}
	if sharedTagWeight <= 0 {
		return LinkRule{}, fmt.Errorf("projector: decode link_rule %s: shared_tag weight must be > 0", ruleBeadID)
	}

	return LinkRule{
		RuleVersion:                ruleBeadID,
		RuleID:                     ruleID,
		TriggerNamespaces:          namespaces,
		MinShared:                  minShared,
		ExcludedSameCodeNamespaces: excludedSameCode,
		Relation:                   relation,
		Severity:                   severity,
		EvidenceBasis:              evidenceBasis,
		FrequencyThreshold:         frequency,
		MaxLinksPerBead:            maxLinks,
		SharedTagWeight:            sharedTagWeight,
	}, nil
}

// decodeStringSlice accepts either a []string (a freshly-built, not-yet-
// round-tripped Bead value) or a []any of strings (the shape
// json.Unmarshal-into-map[string]any produces after a Pod round-trip).
func decodeStringSlice(raw any) ([]string, error) {
	switch v := raw.(type) {
	case nil:
		return nil, nil
	case []string:
		return append([]string(nil), v...), nil
	case []any:
		out := make([]string, 0, len(v))
		for _, elem := range v {
			s, ok := elem.(string)
			if !ok {
				return nil, fmt.Errorf("non-string element %v", elem)
			}
			out = append(out, s)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unsupported shape %T", raw)
	}
}

// decodeInt accepts either a Go int (in-process value) or a float64 (the
// shape encoding/json always decodes a JSON number into, for a Bead
// round-tripped through a Pod frame).
func decodeInt(raw any) (int, error) {
	switch v := raw.(type) {
	case nil:
		return 0, nil
	case int:
		return v, nil
	case float64:
		if math.Trunc(v) != v {
			return 0, fmt.Errorf("must be an integer, got %v", v)
		}
		return int(v), nil
	default:
		return 0, fmt.Errorf("unsupported shape %T", raw)
	}
}

func decodeFloat(raw any) (float64, error) {
	switch v := raw.(type) {
	case int:
		return float64(v), nil
	case float64:
		return v, nil
	default:
		return 0, fmt.Errorf("unsupported shape %T", raw)
	}
}
