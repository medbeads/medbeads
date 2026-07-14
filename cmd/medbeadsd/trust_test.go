package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/medbeads/medbeads/internal/engine"
	"github.com/medbeads/medbeads/internal/engine/trust"
)

func TestTrustCLI_InitReleaseReprojectAndServeValidation(t *testing.T) {
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer devNull.Close()
	dataDir := t.TempDir()
	policyPath := filepath.Join(dataDir, "trust", "policy.json")
	keyPath := filepath.Join(dataDir, "trust", "private-key.json")

	if code := runTrustInit([]string{
		"-data", dataDir,
		"-organization-id", "org:cli-hospital",
		"-organization-name", "CLI病院",
	}, devNull, devNull); code != 0 {
		t.Fatalf("trust init exit=%d", code)
	}
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("private key mode=%#o, want 0600", got)
	}
	policy, err := trust.LoadPolicy(policyPath)
	if err != nil {
		t.Fatal(err)
	}
	if policy.Organizations[0].Name != "CLI病院" || !policy.RequireKnowledgeRelease {
		t.Fatalf("policy = %+v", policy)
	}

	if code := runTrustRelease([]string{
		"-data", dataDir,
		"-release-id", "cli-rules-1",
		"-actor-id", "ehr:committee:1",
		"-actor-name", "承認担当者",
		"-actor-role", "knowledge_approver",
		"-source-system-id", "governance:cli-hospital",
	}, devNull, devNull); code != 0 {
		t.Fatalf("trust release exit=%d", code)
	}

	eng, err := engine.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := eng.Index().ListSharedBeads()
	if err != nil {
		eng.Close() //nolint:errcheck
		t.Fatal(err)
	}
	var knowledgeIDs []string
	for _, ref := range refs {
		switch ref.Type {
		case "link_rule", trust.KnowledgeReleaseType, trust.SignatureAttestationType:
			knowledgeIDs = append(knowledgeIDs, ref.ID)
		}
	}
	if err := eng.Close(); err != nil {
		t.Fatal(err)
	}
	if len(knowledgeIDs) != 3 {
		t.Fatalf("closed knowledge IDs = %v, want rule+release+attestation", knowledgeIDs)
	}

	if code := runReproject([]string{
		"-data", dataDir,
		"-trust-policy", policyPath,
		"-knowledge-ids", strings.Join(knowledgeIDs, ","),
		"-batch-size", "0",
	}, devNull, devNull); code != 0 {
		t.Fatalf("trusted reproject exit=%d", code)
	}

	// This is the same policy-only open used by serve after activation. No
	// release IDs are supplied again; the active manifest must prove itself.
	served, err := engine.OpenWithOptions(dataDir, engine.OpenOptions{
		AutoProject:                  true,
		ProjectionCodeVersion:        engine.DefaultProjectionCodeVersion(),
		RecordStateProjectionVersion: engine.DefaultRecordStateProjectionCodeVersion(),
		TrustPolicy:                  policy,
	})
	if err != nil {
		t.Fatalf("serve-style trusted open: %v", err)
	}
	if err := served.Close(); err != nil {
		t.Fatal(err)
	}
}
