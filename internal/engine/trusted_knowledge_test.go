package engine_test

import (
	"crypto/ed25519"
	"encoding/base64"
	"testing"
	"time"

	"github.com/medbeads/medbeads/internal/engine"
	"github.com/medbeads/medbeads/internal/engine/projector"
	"github.com/medbeads/medbeads/internal/engine/trust"
)

func TestOpenWithOptions_TrustedKnowledgeReleaseActivatesAndReopens(t *testing.T) {
	dataDir := t.TempDir()
	org := trust.Organization{ID: "org:test-hospital", Name: "テスト病院"}
	keyFile, publicKey, err := trust.GenerateLocalKey(org, "org:test-hospital#key-1", []string{
		trust.PurposeClinicalOrigin, trust.PurposeKnowledgeRelease,
	})
	if err != nil {
		t.Fatal(err)
	}
	privateRaw, err := base64.StdEncoding.DecodeString(keyFile.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	policy := trust.Policy{
		Schema: trust.PolicySchema, TenantID: "tenant:test", RequireKnowledgeRelease: true,
		MinimumKnowledgeApprovals: 1,
		Organizations:             []trust.Organization{org},
		Keys:                      []trust.TrustedKey{publicKey},
	}

	bootstrap, err := engine.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	rule, err := bootstrap.Ingest(projector.BuildCooccurrenceRuleBead("2026-01-01T00:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	publishedAt := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	release, err := trust.BuildKnowledgeRelease(trust.ReleaseSpec{
		ReleaseID: "test-release-1", Organization: org, RuleBeadIDs: []string{rule.ID},
		PublishedAt: publishedAt, AuthorActorID: "ehr:committee-user-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	release, err = bootstrap.Ingest(release)
	if err != nil {
		t.Fatal(err)
	}
	approval, err := trust.BuildSignatureAttestation(
		release.ID, trust.PurposeKnowledgeRelease, "governance:test-hospital",
		trust.Actor{ID: "ehr:committee-user-1", Role: "knowledge_approver"},
		keyFile, ed25519.PrivateKey(privateRaw), publishedAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	approval, err = bootstrap.Ingest(approval)
	if err != nil {
		t.Fatal(err)
	}
	if err := bootstrap.Close(); err != nil {
		t.Fatal(err)
	}

	knowledgeIDs := []string{rule.ID, release.ID, approval.ID}
	trustedEngine, err := engine.OpenWithOptions(dataDir, engine.OpenOptions{
		AutoProject:                  true,
		ProjectionCodeVersion:        "trusted-test-v1",
		RecordStateProjectionVersion: engine.DefaultRecordStateProjectionCodeVersion(),
		TrustPolicy:                  &policy,
		InitialKnowledgeBeadIDs:      knowledgeIDs,
	})
	if err != nil {
		t.Fatalf("first trusted OpenWithOptions: %v", err)
	}
	activation, err := trustedEngine.ActivateKnowledgeRelease(knowledgeIDs, "trusted-test-v1", "", time.Now().UTC())
	if err != nil {
		t.Fatalf("ActivateKnowledgeRelease: %v", err)
	}
	if !activation.Rolling.AlreadyActive {
		t.Fatalf("activation AlreadyActive=false, want true after trusted open")
	}
	if activation.Validation.ReleaseBeadID != release.ID {
		t.Fatalf("release ID = %s, want %s", activation.Validation.ReleaseBeadID, release.ID)
	}
	if _, err := trustedEngine.ActivateLinkKnowledge([]string{rule.ID}, "unsigned-bypass", ""); err == nil {
		t.Fatal("ActivateLinkKnowledge accepted a bare unsigned rule under a required-release policy")
	}
	if err := trustedEngine.Close(); err != nil {
		t.Fatal(err)
	}

	// Production serve does not receive InitialKnowledgeBeadIDs. It validates
	// the active manifest's closed signed set and must reopen successfully.
	reopened, err := engine.OpenWithOptions(dataDir, engine.OpenOptions{
		AutoProject:                  true,
		ProjectionCodeVersion:        "trusted-test-v1",
		RecordStateProjectionVersion: engine.DefaultRecordStateProjectionCodeVersion(),
		TrustPolicy:                  &policy,
	})
	if err != nil {
		t.Fatalf("reopen trusted active generation: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenWithOptions_RequiredReleaseFailsClosedWithoutManifest(t *testing.T) {
	org := trust.Organization{ID: "org:test-hospital", Name: "テスト病院"}
	_, publicKey, err := trust.GenerateLocalKey(org, "org:test-hospital#key-1", []string{trust.PurposeKnowledgeRelease})
	if err != nil {
		t.Fatal(err)
	}
	policy := trust.Policy{
		Schema: trust.PolicySchema, TenantID: "tenant:test", RequireKnowledgeRelease: true,
		MinimumKnowledgeApprovals: 1, Organizations: []trust.Organization{org}, Keys: []trust.TrustedKey{publicKey},
	}
	eng, err := engine.OpenWithOptions(t.TempDir(), engine.OpenOptions{AutoProject: true, TrustPolicy: &policy})
	if err == nil {
		eng.Close() //nolint:errcheck
		t.Fatal("trusted OpenWithOptions succeeded without a signed release")
	}
}
