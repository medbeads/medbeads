package projector

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/gowebpki/jcs"
)

// maxLinksPerBead is the v1-compatible default for how many clinical_links rows a single Bead may appear
// in (as either bead_a or bead_b) within one patient's projection pass — the
// runaway-prevention guard specs/U3_link_projector.md's U3b section calls
// for carrying forward ("走らないガード…は投影でも意味があるものは残す"),
// mirroring package apc's Config.MaxSiblingsPerBead (apc/config.go) at
// projection time instead of scan time. There is no per-run Config plumbed
// through Reproject yet (U3b's scope is the projector + Reproject, not a
// tunable-knobs API) — this is a fixed, generous constant for the same
// reason apc.Default()'s own constants are fixed defaults: large enough not
// to bind an ordinary clinically-dense patient, small enough to bound one
// pass's write volume if the frequency filter somehow still lets a
// combinatorial blow-up through.
const maxLinksPerBead = 50

// frequencyThreshold is the v1-compatible IDF-style exclusion threshold: a trigger tag
// whose patient-local frequency (distinct tag-bearing Beads carrying it,
// over the patient's total distinct tag-bearing Beads) is >= this fraction
// is dropped from the candidate-pairing tag set entirely — the same runaway
// prevention d package apc's Config.AntigenFrequencyThreshold implements
// (apc/scanner.go's frequentAntigens), ported to the projector's own
// bead_tags-driven pairing (not reused from apc directly, since apc's
// frequentAntigens also excludes type='sibling_link' Beads, a concept this
// package's Reproject-only, non-generational world does not have).
const frequencyThreshold = 0.3

// patientTag is one bead_tags row scoped to a single patient's projection
// pass: the tag, the Bead carrying it, and that Bead's timestamp (needed for
// clinical_links.created_at's deterministic max(a,b) derivation — see
// linkCreatedAt).
type patientTag struct {
	Tag       string
	BeadID    string
	Timestamp string
}

// clinicalLink is one fully-computed, ready-to-INSERT clinical_links row —
// every column this package ever writes, in Go form, before JSON-encoding
// evidence_bead_ids/score_breakdown for storage.
type clinicalLink struct {
	LinkID          string
	BeadA           string
	BeadB           string
	PatientRoot     string
	Relation        string
	MatchedTag      string
	Severity        string
	EvidenceBasis   string
	EvidenceBeadIDs []string
	ScoreBreakdown  map[string]any
	RuleID          string
	RuleVersion     string
	CreatedAt       string
}

// projectPatientLinks computes every clinical_links row rule triggers for
// one patient's already-indexed bead_tags, given that patient's full
// (tag, bead_id, timestamp) rows (patientTags) and the total distinct
// tag-bearing Bead count needed for the frequency filter (via
// tagBeadCounts/totalTagBeads, both derived from patientTags itself so this
// function has no direct SQL dependency — see queryPatientTags for the
// actual query shape).
//
// The output slice's own ORDER is sorted (by bead_a, then bead_b, then
// matched_tag) purely for caller convenience — but that final sort.Slice
// alone does NOT make two runs' output the same SET of links, and must not
// be read as a determinism guarantee. maxLinksPerBead caps how many links a
// single Bead may appear in per pass; once that cap actually binds on a
// Bead that is a candidate for links across more than one triggering tag,
// WHICH tag's pairs get to claim that Bead's remaining slots depends on the
// order triggering tags are visited in the pairing loop below — sorting the
// *output* after the fact cannot undo an already-nondeterministic selection
// made during pair generation. What actually makes this function
// deterministic is that the pairing loop itself visits triggering tags
// grouped by namespace in rule.TriggerNamespaces' own declared order (ties
// within one namespace broken by sort.Strings), never beadsByTag's raw map
// iteration order (which Go randomizes per process) and never a namespace
// priority hard-coded in this package: see the pairing loop's own comment.
// Given that, two runs over byte-identical (rule, patientTags) always
// select the same set of links, and the final sort.Slice then makes that
// set's slice representation byte-identical too. A consequence worth
// stating explicitly: when the cap binds, namespaces earlier in
// rule.TriggerNamespaces win contested slots over namespaces later in it —
// so that priority is knowledge carried by the rule Bead's own content
// (and therefore its content-addressed rule_version), not a fact about this
// function. Revising the priority means publishing a new rule Bead and
// re-projecting; it does not mean editing this file.
//
// # Trigger rule (specs/U3_link_projector.md's U3b section)
//
// Two Beads in the same patient trigger a link if and only if they share at
// least one tag whose namespace is in rule.TriggerNamespaces (risk:/atc:/
// rxnorm:, in that priority order, for the built-in cooccurrence rule) —
// NOT because they merely share *any* tag. A shared loinc: or temporal: tag
// alone never triggers a link (dropping "LOINC 同一コード・temporal 単独"
// cooccurrence is U3b's entire noise-reduction point): those namespaces are
// excluded here at the tag-filtering step, before pairs are even formed,
// rather than filtered out
// after generating a link — so a loinc-only or temporal-only shared tag
// produces literally zero candidate pairs, not a link that is then
// discarded.
func projectPatientLinks(rule LinkRule, patientRoot string, tags []patientTag) []clinicalLink {
	threshold := rule.FrequencyThreshold
	if threshold <= 0 {
		threshold = frequencyThreshold
	}
	linkCap := rule.MaxLinksPerBead
	if linkCap <= 0 {
		linkCap = maxLinksPerBead
	}
	minShared := rule.MinShared
	if minShared <= 0 {
		minShared = 1
	}
	sharedTagWeight := rule.SharedTagWeight
	if sharedTagWeight <= 0 {
		sharedTagWeight = 1
	}

	trigger := make(map[string]bool, len(rule.TriggerNamespaces))
	for _, ns := range rule.TriggerNamespaces {
		trigger[ns] = true
	}

	// beadsByTag / beadTimestamp / tagBeadCount: three views over the same
	// patientTag rows, each built with one pass, so the trigger-tag
	// frequency filter and the actual pairing loop below share identical
	// underlying data (no risk of the filter and the pairing seeing two
	// different snapshots).
	beadsByTag := make(map[string][]string)        // triggering tag -> bead IDs carrying it
	beadTimestamp := make(map[string]string)       // bead ID -> its timestamp
	tagBeadSet := make(map[string]map[string]bool) // every tag (any namespace) -> set of bead IDs carrying it

	for _, pt := range tags {
		beadTimestamp[pt.BeadID] = pt.Timestamp
		if tagBeadSet[pt.Tag] == nil {
			tagBeadSet[pt.Tag] = make(map[string]bool)
		}
		tagBeadSet[pt.Tag][pt.BeadID] = true
	}

	totalTagBeads := make(map[string]bool)
	for _, pt := range tags {
		totalTagBeads[pt.BeadID] = true
	}
	total := len(totalTagBeads)

	frequent := make(map[string]bool)
	if total > 0 {
		for tag, beadSet := range tagBeadSet {
			if float64(len(beadSet))/float64(total) >= threshold {
				frequent[tag] = true
			}
		}
	}

	for tag, beadSet := range tagBeadSet {
		if !hasTriggerNamespace(tag, trigger) {
			continue
		}
		if frequent[tag] {
			continue // runaway prevention d, ported from apc's IDF filter
		}
		ids := make([]string, 0, len(beadSet))
		for id := range beadSet {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		beadsByTag[tag] = ids
	}

	// Pair generation: for every triggering tag, every pair of Beads sharing
	// it. A pair sharing more than one triggering tag yields one
	// clinical_links row per distinct matched_tag (mirrors sibling_pairs'
	// one-row-per-antigen convention — see clinical_links' UNIQUE(bead_a,
	// bead_b, relation, matched_tag), which is exactly this granularity).
	//
	// Triggering tags are visited grouped by namespace, in the order
	// rule.TriggerNamespaces itself declares those namespaces — NOT
	// beadsByTag's own map iteration order (which Go randomizes per run),
	// and NOT alphabetical order either. linkCount below is a per-Bead cap
	// shared across every tag in this loop, so which tag's pairs get to
	// claim a capped Bead's remaining slots depends on visitation order:
	// making that order equal to the rule Bead's own declared namespace
	// priority means cap-consumption priority is knowledge (published in
	// the rule Bead, content-addressed, revisable by publishing a new rule
	// Bead and re-projecting) rather than a policy hard-coded here. Within
	// one namespace, tags are visited in sorted order — every triggering
	// tag has a well-defined namespace (hasTriggerNamespace already
	// filtered out anything that does not), so this ordering is total and
	// leaves no tag's position ambiguous.
	tagsByNamespace := make(map[string][]string, len(rule.TriggerNamespaces))
	for tag := range beadsByTag {
		ns := tagNamespace(tag)
		tagsByNamespace[ns] = append(tagsByNamespace[ns], tag)
	}
	triggerTags := make([]string, 0, len(beadsByTag))
	visitedNS := make(map[string]bool, len(rule.TriggerNamespaces))
	for _, ns := range rule.TriggerNamespaces {
		// Defensive dedup: a rule Bead listing the same namespace twice in
		// TriggerNamespaces (malformed content, not something
		// BuildCooccurrenceRuleBead itself ever produces) must not expand
		// that namespace's tags into triggerTags twice — the pairing loop's
		// own seen[pairKey] map already makes a duplicate expansion
		// harmless for correctness (no duplicate clinical_links row would
		// result), but visiting the same tags twice is still wasted work
		// and an unclear signal to a reader of this order, so skip a
		// namespace already visited rather than relying on seen to paper
		// over it.
		if visitedNS[ns] {
			continue
		}
		visitedNS[ns] = true
		nsTags := tagsByNamespace[ns]
		sort.Strings(nsTags)
		triggerTags = append(triggerTags, nsTags...)
	}

	// min_shared belongs to the rule Bead and therefore must influence the
	// selected set, not merely be decoded for display. Count distinct eligible
	// triggering tags per unordered pair before the capped deterministic pass.
	// A rule with min_shared=2 emits that pair's per-tag rows only when at least
	// two different tags support the relationship.
	type beadPair struct{ a, b string }
	sharedCounts := make(map[beadPair]int)
	for _, tag := range triggerTags {
		if excludedBySameCodeOnly(tag, rule.ExcludedSameCodeNamespaces) {
			continue
		}
		ids := beadsByTag[tag]
		for i := 0; i < len(ids); i++ {
			for j := i + 1; j < len(ids); j++ {
				a, b := ids[i], ids[j]
				if b < a {
					a, b = b, a
				}
				sharedCounts[beadPair{a: a, b: b}]++
			}
		}
	}

	type pairKey struct{ a, b, tag string }
	seen := make(map[pairKey]bool)
	linkCount := make(map[string]int) // bead ID -> how many links it already appears in this pass

	var out []clinicalLink
	for _, tag := range triggerTags {
		ids := beadsByTag[tag]
		for i := 0; i < len(ids); i++ {
			for j := i + 1; j < len(ids); j++ {
				a, b := ids[i], ids[j]
				if b < a {
					a, b = b, a
				}
				if excludedBySameCodeOnly(tag, rule.ExcludedSameCodeNamespaces) {
					continue
				}
				key := pairKey{a, b, tag}
				if seen[key] {
					continue
				}
				if sharedCounts[beadPair{a: a, b: b}] < minShared {
					continue
				}
				if linkCount[a] >= linkCap || linkCount[b] >= linkCap {
					continue // runaway prevention: per-Bead link cap
				}
				seen[key] = true

				link := clinicalLink{
					BeadA:           a,
					BeadB:           b,
					PatientRoot:     patientRoot,
					Relation:        rule.Relation,
					MatchedTag:      tag,
					Severity:        rule.Severity,
					EvidenceBasis:   rule.EvidenceBasis,
					EvidenceBeadIDs: nil,
					ScoreBreakdown: map[string]any{
						"matched_tag":      tag,
						"namespace":        tagNamespace(tag),
						"shared_tag_count": sharedCounts[beadPair{a: a, b: b}],
						"weighted_score":   float64(sharedCounts[beadPair{a: a, b: b}]) * sharedTagWeight,
					},
					RuleID:      rule.RuleID,
					RuleVersion: rule.RuleVersion,
					CreatedAt:   linkCreatedAt(beadTimestamp[a], beadTimestamp[b]),
				}
				link.LinkID = computeLinkID(a, b, rule.Relation, tag, rule.RuleVersion)
				out = append(out, link)
				linkCount[a]++
				linkCount[b]++
			}
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].BeadA != out[j].BeadA {
			return out[i].BeadA < out[j].BeadA
		}
		if out[i].BeadB != out[j].BeadB {
			return out[i].BeadB < out[j].BeadB
		}
		return out[i].MatchedTag < out[j].MatchedTag
	})
	return out
}

// projectCuratedPairLinks computes the clinical_links rows one CuratedPairRule
// triggers for one patient: for each declared tag pair {tagA, tagB}, link every
// Bead carrying tagA to every Bead carrying tagB.
//
// This is what makes a severity above `info` reachable at all. Every row it
// produces carries evidence_basis=curated_knowledge, the rule Bead's ID as
// rule_version, AND that same ID inside evidence_bead_ids — precisely the triple
// clinical_links' CHECK constraint demands before it will accept a warning
// (migrations/0006_projection_v31.sql). A clinical warning that cannot name its
// own justification is not merely discouraged: the database refuses to store it.
//
// Determinism comes from the same discipline projectPatientLinks learned the
// hard way — never let map iteration order reach the *selection*. rule.TagPairs
// is already normalized and sorted (decodeCuratedPairRule), and each tag's Bead
// list is sorted before pairing. The final sort orders the slice for the
// caller; it does not, and must not be relied on to, make the selected set
// stable.
//
// No per-Bead cap applies here, deliberately. The cooccurrence rule needs one
// because it generates combinatorially many statistical links; a curated rule
// fires only on hand-declared pairs, and silently dropping a clinical warning
// because some Bead already had "too many" links would be exactly the wrong
// failure mode.
func projectCuratedPairLinks(rule CuratedPairRule, patientRoot string, tags []patientTag) []clinicalLink {
	beadsByTag := make(map[string]map[string]bool)
	beadTimestamp := make(map[string]string)
	for _, pt := range tags {
		if beadsByTag[pt.Tag] == nil {
			beadsByTag[pt.Tag] = make(map[string]bool)
		}
		beadsByTag[pt.Tag][pt.BeadID] = true
		beadTimestamp[pt.BeadID] = pt.Timestamp
	}

	sortedBeads := func(tag string) []string {
		set := beadsByTag[tag]
		if len(set) == 0 {
			return nil
		}
		ids := make([]string, 0, len(set))
		for id := range set {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		return ids
	}

	type pairKey struct{ a, b, tag string }
	seen := make(map[pairKey]bool)

	var out []clinicalLink
	for _, pair := range rule.TagPairs {
		tagA, tagB := pair[0], pair[1]
		beadsA := sortedBeads(tagA)
		beadsB := sortedBeads(tagB)
		if len(beadsA) == 0 || len(beadsB) == 0 {
			continue // this patient does not exhibit the pair
		}

		// matched_tag names BOTH sides of the curated claim, so a reader of the
		// row can see the actual clinical assertion ("these two concepts
		// interact"), not just one half of it.
		matchedTag := tagA + "+" + tagB

		for _, ba := range beadsA {
			for _, bb := range beadsB {
				if ba == bb {
					continue // a Bead carrying both tags does not interact with itself
				}
				a, b := ba, bb
				if b < a {
					a, b = b, a // clinical_links CHECK: bead_a < bead_b
				}
				key := pairKey{a, b, matchedTag}
				if seen[key] {
					continue
				}
				seen[key] = true

				evidenceIDs := append([]string{rule.RuleVersion}, rule.EvidenceBeadIDs...)
				sort.Strings(evidenceIDs)
				evidenceIDs = deduplicateSortedStrings(evidenceIDs)
				link := clinicalLink{
					BeadA:         a,
					BeadB:         b,
					PatientRoot:   patientRoot,
					Relation:      rule.Relation,
					MatchedTag:    matchedTag,
					Severity:      rule.Severity,
					EvidenceBasis: rule.EvidenceBasis,
					// The rule Bead IS the evidence. This is the field the CHECK
					// constraint tests for non-emptiness before allowing any
					// severity above info.
					EvidenceBeadIDs: evidenceIDs,
					ScoreBreakdown: map[string]any{
						"matched_pair": []any{tagA, tagB},
						"rule_family":  ruleFamilyCuratedPair,
					},
					RuleID:      rule.RuleID,
					RuleVersion: rule.RuleVersion,
					CreatedAt:   linkCreatedAt(beadTimestamp[a], beadTimestamp[b]),
				}
				link.LinkID = computeLinkID(a, b, rule.Relation, matchedTag, rule.RuleVersion)
				out = append(out, link)
			}
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].BeadA != out[j].BeadA {
			return out[i].BeadA < out[j].BeadA
		}
		if out[i].BeadB != out[j].BeadB {
			return out[i].BeadB < out[j].BeadB
		}
		return out[i].MatchedTag < out[j].MatchedTag
	})

	return out
}

// hasTriggerNamespace reports whether tag's namespace prefix ("ns:" up to
// and including the first ':') is one of the rule's trigger namespaces.
func hasTriggerNamespace(tag string, trigger map[string]bool) bool {
	return trigger[tagNamespace(tag)]
}

// tagNamespace returns tag's namespace prefix including the trailing colon
// (e.g. "risk:nephrotoxic" -> "risk:"), or "" if tag has no colon at all.
func tagNamespace(tag string) string {
	i := strings.IndexByte(tag, ':')
	if i < 0 {
		return ""
	}
	return tag[:i+1]
}

// excludedBySameCodeOnly reports whether tag's own namespace is one of the
// rule's excluded same-code namespaces (e.g. "loinc:"). This is checked per
// matched tag, not per pair, because the trigger namespace set and the
// excluded-same-code set are disjoint by construction for the built-in
// cooccurrence rule (loinc:/temporal: never appear in TriggerNamespaces in
// the first place, so beadsByTag never even contains a loinc:-namespaced
// tag) — this check is a defense-in-depth belt-and-suspenders guard for a
// future rule Bead that might list an excluded namespace inside its own
// trigger set by mistake, not the primary exclusion mechanism (which is
// hasTriggerNamespace's trigger-set membership test above).
func excludedBySameCodeOnly(tag string, excluded []string) bool {
	ns := tagNamespace(tag)
	for _, e := range excluded {
		if ns == e {
			return true
		}
	}
	return false
}

// linkCreatedAt is clinical_links.created_at's deterministic derivation:
// max(a, b) — the later of the two Beads' own event timestamps, identical
// reasoning to apc/link.go's buildSiblingLinkBead (see its doc comment):
// reproducibility (a projection run must not depend on wall-clock time) and
// clinical honesty (the earliest instant both underlying facts existed).
func linkCreatedAt(aTimestamp, bTimestamp string) string {
	if bTimestamp > aTimestamp {
		return bTimestamp
	}
	return aTimestamp
}

// linkIDPayload is the exact JSON shape sha256'd (after JCS canonicalization)
// to derive a clinical_links row's link_id — a content-derived ID, never a
// random/uuid value, per specs/U3_link_projector.md's U3b determinism
// requirement ("link_id = sha256(canonical(bead_a,bead_b,relation,
// matched_tag,rule_version)) の内容導出(乱数/uuid 禁止)").
type linkIDPayload struct {
	BeadA       string `json:"bead_a"`
	BeadB       string `json:"bead_b"`
	Relation    string `json:"relation"`
	MatchedTag  string `json:"matched_tag"`
	RuleVersion string `json:"rule_version"`
}

// computeLinkID derives link_id deterministically from the row's own
// natural-key fields plus rule_version (the knowledge generation that
// produced it), via the project's existing JCS canonicalization helper
// (github.com/gowebpki/jcs — the same library bead.Canonicalize uses), so
// re-running the projector against identical inputs always yields the
// byte-identical link_id.
func computeLinkID(beadA, beadB, relation, matchedTag, ruleVersion string) string {
	payload := linkIDPayload{
		BeadA:       beadA,
		BeadB:       beadB,
		Relation:    relation,
		MatchedTag:  matchedTag,
		RuleVersion: ruleVersion,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		// payload is a fixed struct of plain strings: json.Marshal cannot
		// fail on it (no cyclic types, no unsupported field types), so this
		// is unreachable in practice. Panic rather than silently returning a
		// wrong/empty link_id, which would violate the UNIQUE constraint
		// silently or produce an undetected duplicate.
		panic(fmt.Sprintf("projector: compute link id: marshal: %v", err))
	}
	canonical, err := jcs.Transform(raw)
	if err != nil {
		panic(fmt.Sprintf("projector: compute link id: jcs transform: %v", err))
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

// canonicalJSON returns v's JCS-canonical JSON encoding as a string, used for
// clinical_links.score_breakdown (and any other canonical-JSON column this
// package writes) — consistent, order-independent encoding regardless of
// Go map iteration order, matching migrations/0006's "score_breakdown…正準
// JSON" schema comment.
func canonicalJSON(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("marshal: %w", err)
	}
	canonical, err := jcs.Transform(raw)
	if err != nil {
		return "", fmt.Errorf("jcs transform: %w", err)
	}
	return string(canonical), nil
}

// queryPatientTags reads every bead_tags row for patientRoot, joined against
// beads for each Bead's own timestamp (needed for created_at derivation) —
// the direct-SQL read this package's own read API does not expose (mirrors
// package apc's candidateRows/frequentAntigens direct-SQL convention,
// apc/scanner.go).
func queryPatientTags(q sqlQueryer, patientRoot string) ([]patientTag, error) {
	rows, err := q.Query(`
		SELECT bt.tag, bt.bead_id, b.timestamp
		FROM bead_tags bt
		JOIN beads b ON b.id = bt.bead_id
		WHERE bt.patient_root = ?
		ORDER BY bt.tag, bt.bead_id`,
		patientRoot)
	if err != nil {
		return nil, fmt.Errorf("projector: query patient tags %s: %w", patientRoot, err)
	}
	defer rows.Close()

	var out []patientTag
	for rows.Next() {
		var pt patientTag
		if err := rows.Scan(&pt.Tag, &pt.BeadID, &pt.Timestamp); err != nil {
			return nil, fmt.Errorf("projector: query patient tags %s: scan: %w", patientRoot, err)
		}
		out = append(out, pt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("projector: query patient tags %s: %w", patientRoot, err)
	}
	return out, nil
}
