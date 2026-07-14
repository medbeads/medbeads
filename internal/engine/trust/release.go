package trust

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/medbeads/medbeads/internal/engine/bead"
)

const (
	KnowledgeReleaseSchema = "medbeads.knowledge_release.v1"
	KnowledgeReleaseType   = "knowledge_release"
)

// EffectivePeriod uses policy terminology rather than the ambiguous Japanese
// word 施行日: From is the application start, To is the optional application
// end. Values may be RFC3339 timestamps or YYYY-MM-DD dates.
type EffectivePeriod struct {
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
}

type knowledgeReleaseContent struct {
	Schema           string          `json:"schema"`
	ReleaseID        string          `json:"release_id"`
	RevisionLabel    string          `json:"revision_label,omitempty"`
	OrganizationID   string          `json:"organization_id"`
	OrganizationName string          `json:"organization_name"`
	RuleBeadIDs      []string        `json:"rule_bead_ids"`
	EffectivePeriod  EffectivePeriod `json:"effective_period"`
	PublishedAt      string          `json:"published_at"`
}

// ReleaseSpec is the operator input used to mint an immutable release.
type ReleaseSpec struct {
	ReleaseID     string
	RevisionLabel string
	Organization  Organization
	RuleBeadIDs   []string
	EffectiveFrom string
	EffectiveTo   string
	PublishedAt   string
	AuthorActorID string
}

// KnowledgeReader is intentionally the same small shape Engine already
// provides. Keeping it here avoids coupling trust verification to Pod/SQLite.
type KnowledgeReader interface {
	GetBead(id string) (bead.Bead, error)
}

// ReleaseValidation is the closed set accepted for one projection generation.
type ReleaseValidation struct {
	ReleaseBeadID       string
	RuleBeadIDs         []string
	AttestationBeadIDs  []string
	OrganizationID      string
	EffectiveFrom       string
	EffectiveTo         string
	ValidApprovalActors []string
}

// InsufficientApprovalsError lets an authoring workflow distinguish a valid
// pending release from malformed or cryptographically invalid knowledge.
type InsufficientApprovalsError struct {
	ReleaseBeadID string
	Have          int
	Need          int
}

func (e *InsufficientApprovalsError) Error() string {
	return fmt.Sprintf("trust: release %s has %d independent approval actor(s), policy requires %d",
		e.ReleaseBeadID, e.Have, e.Need)
}

// BuildKnowledgeRelease returns an unsaved knowledge_release Bead. Rules are
// both Parents and signed content so the DAG and human-readable declaration
// cannot disagree.
func BuildKnowledgeRelease(spec ReleaseSpec) (bead.Bead, error) {
	if spec.ReleaseID == "" || spec.Organization.ID == "" || spec.Organization.Name == "" || spec.AuthorActorID == "" {
		return bead.Bead{}, errors.New("trust: release_id, organization id/name and author actor are required")
	}
	if _, err := time.Parse(time.RFC3339Nano, spec.PublishedAt); err != nil {
		return bead.Bead{}, fmt.Errorf("trust: release published_at must be RFC3339: %w", err)
	}
	ruleIDs, err := normalizedIDs(spec.RuleBeadIDs)
	if err != nil {
		return bead.Bead{}, fmt.Errorf("trust: release rule_bead_ids: %w", err)
	}
	if len(ruleIDs) == 0 {
		return bead.Bead{}, errors.New("trust: release requires at least one rule_bead_id")
	}
	if err := validateEffectivePeriod(spec.EffectiveFrom, spec.EffectiveTo); err != nil {
		return bead.Bead{}, err
	}
	content, err := structToMap(knowledgeReleaseContent{
		Schema:           KnowledgeReleaseSchema,
		ReleaseID:        spec.ReleaseID,
		RevisionLabel:    spec.RevisionLabel,
		OrganizationID:   spec.Organization.ID,
		OrganizationName: spec.Organization.Name,
		RuleBeadIDs:      ruleIDs,
		EffectivePeriod: EffectivePeriod{
			From: spec.EffectiveFrom,
			To:   spec.EffectiveTo,
		},
		PublishedAt: spec.PublishedAt,
	})
	if err != nil {
		return bead.Bead{}, fmt.Errorf("trust: build release content: %w", err)
	}
	return bead.Bead{
		Type:      KnowledgeReleaseType,
		Timestamp: spec.PublishedAt,
		Author:    spec.AuthorActorID,
		Parents:   ruleIDs,
		Content:   content,
	}, nil
}

// ValidateKnowledgeSet verifies exactly one release and its signature
// attestations, then returns the rule Beads it authorizes. An extra link_rule
// in the manifest that the release did not name is rejected.
func ValidateKnowledgeSet(reader KnowledgeReader, knowledgeBeadIDs []string, policy Policy, at time.Time) (ReleaseValidation, error) {
	if err := policy.Validate(); err != nil {
		return ReleaseValidation{}, err
	}
	ids, err := normalizedIDs(knowledgeBeadIDs)
	if err != nil {
		return ReleaseValidation{}, fmt.Errorf("trust: knowledge set: %w", err)
	}
	var release bead.Bead
	var attestations []bead.Bead
	var linkRuleIDs []string
	for _, id := range ids {
		b, err := reader.GetBead(id)
		if err != nil {
			return ReleaseValidation{}, fmt.Errorf("trust: load knowledge Bead %s: %w", id, err)
		}
		switch b.Type {
		case KnowledgeReleaseType:
			if release.ID != "" {
				return ReleaseValidation{}, errors.New("trust: a projection knowledge set must contain exactly one knowledge_release")
			}
			release = b
		case SignatureAttestationType:
			attestations = append(attestations, b)
		case "link_rule":
			linkRuleIDs = append(linkRuleIDs, b.ID)
		default:
			return ReleaseValidation{}, fmt.Errorf("trust: unsupported Bead type %q in closed knowledge set", b.Type)
		}
	}
	if release.ID == "" {
		return ReleaseValidation{}, errors.New("trust: knowledge set contains no knowledge_release")
	}
	return VerifyKnowledgeRelease(reader, release, attestations, linkRuleIDs, policy, at)
}

// VerifyKnowledgeRelease checks release shape/effective period, every declared
// link_rule, and the policy-required number of independent approval actors.
func VerifyKnowledgeRelease(reader KnowledgeReader, release bead.Bead, attestations []bead.Bead, suppliedRuleIDs []string, policy Policy, at time.Time) (ReleaseValidation, error) {
	if err := bead.Verify(release); err != nil {
		return ReleaseValidation{}, fmt.Errorf("trust: knowledge release content hash: %w", err)
	}
	if release.Type != KnowledgeReleaseType {
		return ReleaseValidation{}, fmt.Errorf("trust: release Bead %s has type %q", release.ID, release.Type)
	}
	raw, err := json.Marshal(release.Content)
	if err != nil {
		return ReleaseValidation{}, fmt.Errorf("trust: encode release %s: %w", release.ID, err)
	}
	var content knowledgeReleaseContent
	if err := decodeStrict(raw, &content); err != nil {
		return ReleaseValidation{}, fmt.Errorf("trust: decode release %s: %w", release.ID, err)
	}
	if content.Schema != KnowledgeReleaseSchema || content.ReleaseID == "" ||
		content.OrganizationID == "" || content.OrganizationName == "" || release.Author == "" {
		return ReleaseValidation{}, fmt.Errorf("trust: release %s has missing or unsupported identity fields", release.ID)
	}
	if release.Timestamp != content.PublishedAt {
		return ReleaseValidation{}, fmt.Errorf("trust: release %s timestamp does not match published_at", release.ID)
	}
	publishedAt, err := time.Parse(time.RFC3339Nano, content.PublishedAt)
	if err != nil || publishedAt.After(at) {
		return ReleaseValidation{}, fmt.Errorf("trust: release %s has invalid or future published_at", release.ID)
	}
	if err := validateEffectivePeriod(content.EffectivePeriod.From, content.EffectivePeriod.To); err != nil {
		return ReleaseValidation{}, fmt.Errorf("trust: release %s: %w", release.ID, err)
	}
	if err := requireEffectiveAt(content.EffectivePeriod.From, content.EffectivePeriod.To, at); err != nil {
		return ReleaseValidation{}, fmt.Errorf("trust: release %s: %w", release.ID, err)
	}

	ruleIDs, err := normalizedIDs(content.RuleBeadIDs)
	if err != nil || len(ruleIDs) == 0 {
		return ReleaseValidation{}, fmt.Errorf("trust: release %s has invalid or empty rule_bead_ids", release.ID)
	}
	parentIDs, err := normalizedIDs(release.Parents)
	if err != nil || !equalStrings(ruleIDs, parentIDs) {
		return ReleaseValidation{}, fmt.Errorf("trust: release %s parents must exactly equal rule_bead_ids", release.ID)
	}
	supplied, err := normalizedIDs(suppliedRuleIDs)
	if err != nil || !equalStrings(ruleIDs, supplied) {
		return ReleaseValidation{}, fmt.Errorf("trust: release %s does not authorize exactly the supplied link_rule set", release.ID)
	}
	for _, ruleID := range ruleIDs {
		rule, err := reader.GetBead(ruleID)
		if err != nil {
			return ReleaseValidation{}, fmt.Errorf("trust: release %s rule %s unavailable: %w", release.ID, ruleID, err)
		}
		if rule.Type != "link_rule" {
			return ReleaseValidation{}, fmt.Errorf("trust: release %s parent %s is %q, not link_rule", release.ID, ruleID, rule.Type)
		}
		if err := bead.Verify(rule); err != nil {
			return ReleaseValidation{}, fmt.Errorf("trust: release %s rule %s content hash: %w", release.ID, ruleID, err)
		}
	}

	knownOrg := false
	for _, org := range policy.Organizations {
		if org.ID == content.OrganizationID {
			knownOrg = true
			break
		}
	}
	if !knownOrg {
		return ReleaseValidation{}, fmt.Errorf("trust: release %s organization %q is not trusted", release.ID, content.OrganizationID)
	}

	seenActors := map[string]bool{}
	var validAttestationIDs, validActors []string
	for _, attestation := range attestations {
		verified, err := VerifySignatureAttestation(attestation, policy, at)
		if err != nil {
			return ReleaseValidation{}, fmt.Errorf("trust: release %s approval: %w", release.ID, err)
		}
		statement := verified.Statement
		if statement.Purpose != PurposeKnowledgeRelease || statement.SubjectBeadID != release.ID {
			return ReleaseValidation{}, fmt.Errorf("trust: attestation %s is not a knowledge_release approval for %s", attestation.ID, release.ID)
		}
		if !policy.AllowCrossOrganizationApprovals && statement.OrganizationID != content.OrganizationID {
			return ReleaseValidation{}, fmt.Errorf("trust: attestation %s is from organization %q, release belongs to %q", attestation.ID, statement.OrganizationID, content.OrganizationID)
		}
		actorKey := statement.OrganizationID + "\x00" + statement.Actor.ID
		if seenActors[actorKey] {
			continue
		}
		seenActors[actorKey] = true
		validAttestationIDs = append(validAttestationIDs, attestation.ID)
		validActors = append(validActors, statement.Actor.ID)
	}
	if len(validActors) < policy.MinimumKnowledgeApprovals {
		return ReleaseValidation{}, &InsufficientApprovalsError{
			ReleaseBeadID: release.ID,
			Have:          len(validActors),
			Need:          policy.MinimumKnowledgeApprovals,
		}
	}
	sort.Strings(validAttestationIDs)
	sort.Strings(validActors)
	return ReleaseValidation{
		ReleaseBeadID:       release.ID,
		RuleBeadIDs:         ruleIDs,
		AttestationBeadIDs:  validAttestationIDs,
		OrganizationID:      content.OrganizationID,
		EffectiveFrom:       content.EffectivePeriod.From,
		EffectiveTo:         content.EffectivePeriod.To,
		ValidApprovalActors: validActors,
	}, nil
}

func validateEffectivePeriod(fromRaw, toRaw string) error {
	from, err := parseEffectiveTime(fromRaw, false)
	if err != nil {
		return fmt.Errorf("effective_from: %w", err)
	}
	to, err := parseEffectiveTime(toRaw, true)
	if err != nil {
		return fmt.Errorf("effective_to: %w", err)
	}
	if !from.IsZero() && !to.IsZero() && to.Before(from) {
		return errors.New("effective_to precedes effective_from")
	}
	return nil
}

func requireEffectiveAt(fromRaw, toRaw string, at time.Time) error {
	from, _ := parseEffectiveTime(fromRaw, false)
	to, _ := parseEffectiveTime(toRaw, true)
	if !from.IsZero() && at.Before(from) {
		return fmt.Errorf("not applicable before %s", fromRaw)
	}
	if !to.IsZero() && at.After(to) {
		return fmt.Errorf("application ended at %s", toRaw)
	}
	return nil
}

func parseEffectiveTime(raw string, endOfDay bool) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}
	if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return parsed, nil
	}
	parsed, err := time.Parse("2006-01-02", raw)
	if err != nil {
		return time.Time{}, errors.New("must be RFC3339 or YYYY-MM-DD")
	}
	if endOfDay {
		return parsed.Add(24*time.Hour - time.Nanosecond), nil
	}
	return parsed, nil
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
