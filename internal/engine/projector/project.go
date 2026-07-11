package projector

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/gowebpki/jcs"
)

// maxLinksPerBead caps how many clinical_links rows a single Bead may appear
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

// frequencyThreshold is the IDF-style exclusion threshold: a trigger tag
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
// actual query shape). Output is deterministically ordered (sorted by
// bead_a, then bead_b, then matched_tag) so a caller comparing two runs'
// output for byte-identical determinism does not need its own sort.
//
// # Trigger rule (specs/U3_link_projector.md's U3b section)
//
// Two Beads in the same patient trigger a link if and only if they share at
// least one tag whose namespace is in rule.TriggerNamespaces (atc:/risk:/
// rxnorm: for the built-in cooccurrence rule) — NOT because they merely
// share *any* tag. A shared loinc: or temporal: tag alone never triggers a
// link (dropping "LOINC 同一コード・temporal 単独" cooccurrence is U3b's
// entire noise-reduction point): those namespaces are excluded here at the
// tag-filtering step, before pairs are even formed, rather than filtered out
// after generating a link — so a loinc-only or temporal-only shared tag
// produces literally zero candidate pairs, not a link that is then
// discarded.
func projectPatientLinks(rule LinkRule, patientRoot string, tags []patientTag) []clinicalLink {
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
			if float64(len(beadSet))/float64(total) >= frequencyThreshold {
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
	type pairKey struct{ a, b, tag string }
	seen := make(map[pairKey]bool)
	linkCount := make(map[string]int) // bead ID -> how many links it already appears in this pass

	var out []clinicalLink
	for tag, ids := range beadsByTag {
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
				if linkCount[a] >= maxLinksPerBead || linkCount[b] >= maxLinksPerBead {
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
					ScoreBreakdown:  map[string]any{"matched_tag": tag, "namespace": tagNamespace(tag)},
					RuleID:          rule.RuleID,
					RuleVersion:     rule.RuleVersion,
					CreatedAt:       linkCreatedAt(beadTimestamp[a], beadTimestamp[b]),
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
func queryPatientTags(sqlDB *sql.DB, patientRoot string) ([]patientTag, error) {
	rows, err := sqlDB.Query(`
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
