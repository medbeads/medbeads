package projector

import (
	"fmt"
	"sort"

	"github.com/medbeads/medbeads/internal/engine/bead"
	"github.com/medbeads/medbeads/internal/engine/index"
)

// linkRuleSchema is the content.schema value every link_rule Bead this
// package writes or reads must carry (specs/U3_link_projector.md's U3 前の
// 仕様修正 section, "link_rule Bead content スキーマ確定").
const linkRuleSchema = "medbeads.link_rule.v1"

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
)

// triggerNamespaces is the sorted set of bead_tags namespace prefixes (see
// antigen/extract.go's prefix constants) whose shared occurrence between two
// Beads in the same patient can trigger a cooccurrence link. It deliberately
// excludes "loinc:" and "temporal:" — dropping same-LOINC-code and
// temporal-only cooccurrence is the whole point of U3b (specs/
// U3_link_projector.md: "LOINC 同一コード・temporal 単独をトリガー除外(87%
// ノイズ根絶)"). Kept sorted (not just conceptually but as this literal's own
// order) so linkRuleContent's canonical JSON encoding is stable regardless of
// how this slice is ever edited.
var triggerNamespaces = []string{"atc:", "risk:", "rxnorm:"}

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
		"rule_family":    "cooccurrence",
		"trigger":        trigger,
		"relation":       relationClinicalCorrelation,
		"severity":       severityInfo,
		"evidence_basis": evidenceBasisCooccurrence,
		"score_model":    scoreModel,
	}
	return bead.Bead{
		Type:      linkRuleType,
		Timestamp: timestamp,
		Author:    "projector_seed",
		Content:   content,
	}
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
		if schema != linkRuleSchema {
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

	var excludedSameCode []string
	if excludes, ok := trigger["excludes"].(map[string]any); ok {
		excludedSameCode, err = decodeStringSlice(excludes["same_code_namespaces"])
		if err != nil {
			return LinkRule{}, fmt.Errorf("projector: decode link_rule %s: trigger.excludes.same_code_namespaces: %w", ruleBeadID, err)
		}
	}

	sort.Strings(namespaces)
	sort.Strings(excludedSameCode)

	return LinkRule{
		RuleVersion:                ruleBeadID,
		RuleID:                     ruleID,
		TriggerNamespaces:          namespaces,
		MinShared:                  minShared,
		ExcludedSameCodeNamespaces: excludedSameCode,
		Relation:                   relation,
		Severity:                   severity,
		EvidenceBasis:              evidenceBasis,
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
		return int(v), nil
	default:
		return 0, fmt.Errorf("unsupported shape %T", raw)
	}
}
