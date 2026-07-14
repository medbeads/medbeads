package trust

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/medbeads/medbeads/internal/engine/bead"
)

type memoryReader map[string]bead.Bead

func (r memoryReader) GetBead(id string) (bead.Bead, error) {
	if b, ok := r[id]; ok {
		return b, nil
	}
	return bead.Bead{}, &notFound{id: id}
}

type notFound struct{ id string }

func (e *notFound) Error() string { return "not found: " + e.id }

func testTrust(t *testing.T) (Policy, PrivateKeyFile, ed25519.PrivateKey) {
	t.Helper()
	org := Organization{ID: "org:hospital-a", Name: "A病院"}
	keyFile, publicKey, err := GenerateLocalKey(org, "org:hospital-a#key-1", []string{
		PurposeClinicalOrigin, PurposeKnowledgeRelease,
	})
	if err != nil {
		t.Fatal(err)
	}
	privateRaw, err := base64.StdEncoding.DecodeString(keyFile.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	policy := Policy{
		Schema:                    PolicySchema,
		TenantID:                  "tenant:hospital-a",
		RequireKnowledgeRelease:   true,
		MinimumKnowledgeApprovals: 1,
		Organizations:             []Organization{org},
		Keys:                      []TrustedKey{publicKey},
	}
	if err := policy.Validate(); err != nil {
		t.Fatal(err)
	}
	return policy, keyFile, ed25519.PrivateKey(privateRaw)
}

func subjectBead(t *testing.T, beadType string) bead.Bead {
	t.Helper()
	b, err := bead.WithID(bead.Bead{
		Type:      beadType,
		Timestamp: "2026-07-14T00:00:00Z",
		Author:    "ehr:user:1",
		Content:   map[string]any{"value": "test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func signedAttestation(t *testing.T, subjectID, purpose string, keyFile PrivateKeyFile, privateKey ed25519.PrivateKey, actor Actor, at string) bead.Bead {
	t.Helper()
	b, err := BuildSignatureAttestation(subjectID, purpose, "ehr:hospital-a", actor, keyFile, privateKey, at)
	if err != nil {
		t.Fatal(err)
	}
	b, err = bead.WithID(b)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSignatureAttestation_VerifiesHospitalAndEHRActor(t *testing.T) {
	policy, keyFile, privateKey := testTrust(t)
	subject := subjectBead(t, "fhir_observation")
	attestation := signedAttestation(t, subject.ID, PurposeClinicalOrigin, keyFile, privateKey, Actor{
		ID: "ehr:user:doctor-123", DisplayName: "山田医師", Role: "physician",
	}, "2026-07-14T01:00:00Z")

	verified, err := VerifySignatureAttestation(attestation, policy, time.Date(2026, 7, 14, 2, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("VerifySignatureAttestation: %v", err)
	}
	if verified.Statement.OrganizationID != "org:hospital-a" || verified.Statement.OrganizationName != "A病院" {
		t.Fatalf("verified organization = %q/%q", verified.Statement.OrganizationID, verified.Statement.OrganizationName)
	}
	if verified.Statement.Actor.ID != "ehr:user:doctor-123" || verified.Statement.Actor.Role != "physician" {
		t.Fatalf("verified actor = %+v", verified.Statement.Actor)
	}
}

func TestSignatureAttestation_TamperedSignedClaimFails(t *testing.T) {
	policy, keyFile, privateKey := testTrust(t)
	subject := subjectBead(t, "fhir_observation")
	attestation := signedAttestation(t, subject.ID, PurposeClinicalOrigin, keyFile, privateKey,
		Actor{ID: "ehr:user:doctor-123"}, "2026-07-14T01:00:00Z")

	statement := attestation.Content["statement"].(map[string]any)
	statement["source_system_id"] = "ehr:attacker"
	tampered, err := bead.WithID(attestation)
	if err != nil {
		t.Fatal(err)
	}
	_, err = VerifySignatureAttestation(tampered, policy, time.Date(2026, 7, 14, 2, 0, 0, 0, time.UTC))
	if err == nil || !strings.Contains(err.Error(), "signature verification failed") {
		t.Fatalf("tampered attestation error = %v, want signature failure", err)
	}
}

func TestSignatureAttestation_RevokedKeyFailsClosed(t *testing.T) {
	policy, keyFile, privateKey := testTrust(t)
	subject := subjectBead(t, "fhir_observation")
	attestation := signedAttestation(t, subject.ID, PurposeClinicalOrigin, keyFile, privateKey,
		Actor{ID: "ehr:user:doctor-123"}, "2026-07-14T01:00:00Z")
	policy.Keys[0].RevokedAt = "2026-07-14T01:30:00Z"

	_, err := VerifySignatureAttestation(attestation, policy, time.Date(2026, 7, 14, 2, 0, 0, 0, time.UTC))
	if err == nil || !strings.Contains(err.Error(), "revoked") {
		t.Fatalf("revoked key error = %v", err)
	}
}

func TestKnowledgeRelease_ClosedSignedRuleSet(t *testing.T) {
	policy, keyFile, privateKey := testTrust(t)
	rule := subjectBead(t, "link_rule")
	release, err := BuildKnowledgeRelease(ReleaseSpec{
		ReleaseID:     "hospital-a-rules-2026-07",
		RevisionLabel: "2026年7月版",
		Organization:  policy.Organizations[0],
		RuleBeadIDs:   []string{rule.ID},
		EffectiveFrom: "2026-07-14",
		PublishedAt:   "2026-07-14T01:00:00Z",
		AuthorActorID: "ehr:user:committee-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	release, err = bead.WithID(release)
	if err != nil {
		t.Fatal(err)
	}
	approval := signedAttestation(t, release.ID, PurposeKnowledgeRelease, keyFile, privateKey, Actor{
		ID: "ehr:user:committee-1", DisplayName: "医療安全委員", Role: "knowledge_approver",
	}, "2026-07-14T01:00:00Z")
	reader := memoryReader{rule.ID: rule, release.ID: release, approval.ID: approval}
	ids := []string{rule.ID, release.ID, approval.ID}

	validated, err := ValidateKnowledgeSet(reader, ids, policy, time.Date(2026, 7, 14, 2, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("ValidateKnowledgeSet: %v", err)
	}
	if len(validated.RuleBeadIDs) != 1 || validated.RuleBeadIDs[0] != rule.ID {
		t.Fatalf("validated rules = %v", validated.RuleBeadIDs)
	}
	if len(validated.ValidApprovalActors) != 1 || validated.ValidApprovalActors[0] != "ehr:user:committee-1" {
		t.Fatalf("validated actors = %v", validated.ValidApprovalActors)
	}
}

func TestKnowledgeRelease_UnsignedExtraRuleIsRejected(t *testing.T) {
	policy, keyFile, privateKey := testTrust(t)
	rule := subjectBead(t, "link_rule")
	extraRule := subjectBead(t, "link_rule_extra")
	extraRule.Type = "link_rule"
	extraRule.Content["value"] = "unsigned-extra-rule"
	extraRule, _ = bead.WithID(extraRule)
	release, err := BuildKnowledgeRelease(ReleaseSpec{
		ReleaseID: "release-1", Organization: policy.Organizations[0], RuleBeadIDs: []string{rule.ID},
		PublishedAt: "2026-07-14T01:00:00Z", AuthorActorID: "approver-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	release, _ = bead.WithID(release)
	approval := signedAttestation(t, release.ID, PurposeKnowledgeRelease, keyFile, privateKey,
		Actor{ID: "approver-1"}, "2026-07-14T01:00:00Z")
	reader := memoryReader{rule.ID: rule, extraRule.ID: extraRule, release.ID: release, approval.ID: approval}

	_, err = ValidateKnowledgeSet(reader, []string{rule.ID, extraRule.ID, release.ID, approval.ID}, policy,
		time.Date(2026, 7, 14, 2, 0, 0, 0, time.UTC))
	if err == nil || !strings.Contains(err.Error(), "does not authorize exactly") {
		t.Fatalf("extra rule error = %v", err)
	}
}

func TestKnowledgeRelease_ApplicationEndIsNotProcedureDate(t *testing.T) {
	policy, keyFile, privateKey := testTrust(t)
	rule := subjectBead(t, "link_rule")
	release, err := BuildKnowledgeRelease(ReleaseSpec{
		ReleaseID: "release-expired", Organization: policy.Organizations[0], RuleBeadIDs: []string{rule.ID},
		EffectiveFrom: "2026-01-01", EffectiveTo: "2026-06-30",
		PublishedAt: "2026-01-01T00:00:00Z", AuthorActorID: "approver-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	release, _ = bead.WithID(release)
	approval := signedAttestation(t, release.ID, PurposeKnowledgeRelease, keyFile, privateKey,
		Actor{ID: "approver-1"}, "2026-01-01T00:00:00Z")
	reader := memoryReader{rule.ID: rule, release.ID: release, approval.ID: approval}

	_, err = ValidateKnowledgeSet(reader, []string{rule.ID, release.ID, approval.ID}, policy,
		time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC))
	if err == nil || !strings.Contains(err.Error(), "application ended") {
		t.Fatalf("expired release error = %v", err)
	}
}

func TestKnowledgeRelease_MultipleApprovalPolicyStaysPending(t *testing.T) {
	policy, keyFile, privateKey := testTrust(t)
	policy.MinimumKnowledgeApprovals = 2
	rule := subjectBead(t, "link_rule")
	release, err := BuildKnowledgeRelease(ReleaseSpec{
		ReleaseID: "release-two-approvals", Organization: policy.Organizations[0], RuleBeadIDs: []string{rule.ID},
		PublishedAt: "2026-07-14T01:00:00Z", AuthorActorID: "approver-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	release, _ = bead.WithID(release)
	approval := signedAttestation(t, release.ID, PurposeKnowledgeRelease, keyFile, privateKey,
		Actor{ID: "approver-1"}, "2026-07-14T01:00:00Z")
	reader := memoryReader{rule.ID: rule, release.ID: release, approval.ID: approval}

	_, err = ValidateKnowledgeSet(reader, []string{rule.ID, release.ID, approval.ID}, policy,
		time.Date(2026, 7, 14, 2, 0, 0, 0, time.UTC))
	var pending *InsufficientApprovalsError
	if !errors.As(err, &pending) || pending.Have != 1 || pending.Need != 2 {
		t.Fatalf("pending error = %#v, want 1/2 InsufficientApprovalsError", err)
	}
}
