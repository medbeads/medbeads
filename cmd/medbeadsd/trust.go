package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/medbeads/medbeads/internal/engine"
	"github.com/medbeads/medbeads/internal/engine/bead"
	"github.com/medbeads/medbeads/internal/engine/projector"
	"github.com/medbeads/medbeads/internal/engine/trust"
)

const trustUsage = `medbeadsd trust - cryptographic provenance and knowledge release

Usage:
  medbeadsd trust init     -data <dir> -organization-id <id> -organization-name <name>
  medbeadsd trust attest   -data <dir> -subject <bead-id> -purpose <clinical_origin|fhir_import|knowledge_release>
                           -actor-id <ehr-user-id> -source-system-id <ehr-id>
  medbeadsd trust release  -data <dir> -release-id <id> -rule-ids <id,id,...>
                           -actor-id <approver-id> -source-system-id <system-id>

init creates a local Ed25519 bootstrap key. Production deployments should
replace the local private-key file with KMS/HSM-backed signing.
`

func runTrust(args []string, stdout, stderr *os.File) int {
	if len(args) == 0 {
		fmt.Fprint(stdout, trustUsage)
		return 0
	}
	switch args[0] {
	case "init":
		return runTrustInit(args[1:], stdout, stderr)
	case "attest":
		return runTrustAttest(args[1:], stdout, stderr)
	case "release":
		return runTrustRelease(args[1:], stdout, stderr)
	case "-h", "-help", "--help", "help":
		fmt.Fprint(stdout, trustUsage)
		return 0
	default:
		fmt.Fprintf(stderr, "medbeadsd trust: unknown command %q\n\n%s", args[0], trustUsage)
		return 1
	}
}

func runTrustInit(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("trust init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data", "", "MedBeads data directory")
	organizationID := fs.String("organization-id", "", "stable hospital/organization ID (not a display name)")
	organizationName := fs.String("organization-name", "", "hospital/organization display name")
	tenantID := fs.String("tenant-id", "", "cloud/storage tenant ID; defaults to organization-id")
	keyID := fs.String("key-id", "", "stable signing key ID; defaults to <organization-id>#system-signing-1")
	policyPath := fs.String("trust-policy", "", "public policy output path; defaults to <data>/trust/policy.json")
	privateKeyPath := fs.String("private-key", "", "private key output path; defaults to <data>/trust/private-key.json")
	minimumApprovals := fs.Int("minimum-knowledge-approvals", 1, "independent actors required to approve a knowledge release")
	force := fs.Bool("force", false, "overwrite existing policy/key files")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dataDir == "" || *organizationID == "" || *organizationName == "" {
		fmt.Fprintln(stderr, "medbeadsd trust init: -data, -organization-id and -organization-name are required")
		return 2
	}
	if *tenantID == "" {
		*tenantID = *organizationID
	}
	if *keyID == "" {
		*keyID = *organizationID + "#system-signing-1"
	}
	if *policyPath == "" {
		*policyPath = filepath.Join(*dataDir, "trust", "policy.json")
	}
	if *privateKeyPath == "" {
		*privateKeyPath = filepath.Join(*dataDir, "trust", "private-key.json")
	}
	if !*force {
		for _, path := range []string{*policyPath, *privateKeyPath} {
			if _, err := os.Stat(path); err == nil {
				fmt.Fprintf(stderr, "medbeadsd trust init: %s already exists (use -force to replace)\n", path)
				return 1
			} else if !errors.Is(err, os.ErrNotExist) {
				fmt.Fprintf(stderr, "medbeadsd trust init: inspect %s: %v\n", path, err)
				return 1
			}
		}
	}

	org := trust.Organization{ID: *organizationID, Name: *organizationName}
	privateFile, publicKey, err := trust.GenerateLocalKey(org, *keyID, []string{
		trust.PurposeClinicalOrigin,
		trust.PurposeFHIRImport,
		trust.PurposeKnowledgeRelease,
	})
	if err != nil {
		fmt.Fprintf(stderr, "medbeadsd trust init: %v\n", err)
		return 1
	}
	policy := trust.Policy{
		Schema:                    trust.PolicySchema,
		TenantID:                  *tenantID,
		RequireKnowledgeRelease:   true,
		MinimumKnowledgeApprovals: *minimumApprovals,
		Organizations:             []trust.Organization{org},
		Keys:                      []trust.TrustedKey{publicKey},
	}
	if err := policy.Validate(); err != nil {
		fmt.Fprintf(stderr, "medbeadsd trust init: %v\n", err)
		return 1
	}
	if err := writeJSONFile(*privateKeyPath, privateFile, 0o600, *force); err != nil {
		fmt.Fprintf(stderr, "medbeadsd trust init: %v\n", err)
		return 1
	}
	if err := writeJSONFile(*policyPath, policy, 0o644, *force); err != nil {
		fmt.Fprintf(stderr, "medbeadsd trust init: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "medbeadsd trust init: organization=%s (%s) key_id=%s\n", org.Name, org.ID, *keyID)
	fmt.Fprintf(stdout, "medbeadsd trust init: public policy %s\n", *policyPath)
	fmt.Fprintf(stdout, "medbeadsd trust init: local private key %s (mode 0600; use KMS/HSM in production)\n", *privateKeyPath)
	return 0
}

func runTrustAttest(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("trust attest", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data", "", "MedBeads data directory")
	subject := fs.String("subject", "", "subject Bead ID")
	purpose := fs.String("purpose", trust.PurposeClinicalOrigin, "signature purpose")
	actorID := fs.String("actor-id", "", "authenticated EHR/operator actor ID")
	actorName := fs.String("actor-name", "", "actor display name snapshot")
	actorRole := fs.String("actor-role", "", "actor role, e.g. physician or pharmacist")
	sourceSystemID := fs.String("source-system-id", "", "source EHR/governance system ID")
	signedAt := fs.String("signed-at", "", "RFC3339 signature time; defaults to now")
	policyPath := fs.String("trust-policy", "", "public trust policy; defaults to <data>/trust/policy.json")
	privateKeyPath := fs.String("private-key", "", "local private key; defaults to <data>/trust/private-key.json")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dataDir == "" || *subject == "" || *actorID == "" || *sourceSystemID == "" {
		fmt.Fprintln(stderr, "medbeadsd trust attest: -data, -subject, -actor-id and -source-system-id are required")
		return 2
	}
	defaultTrustPaths(*dataDir, policyPath, privateKeyPath)
	policy, err := trust.LoadPolicy(*policyPath)
	if err != nil {
		fmt.Fprintf(stderr, "medbeadsd trust attest: %v\n", err)
		return 1
	}
	keyFile, privateKey, err := trust.LoadPrivateKey(*privateKeyPath)
	if err != nil {
		fmt.Fprintf(stderr, "medbeadsd trust attest: %v\n", err)
		return 1
	}
	if *signedAt == "" {
		*signedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	eng, err := engine.Open(*dataDir)
	if err != nil {
		fmt.Fprintf(stderr, "medbeadsd trust attest: open engine: %v\n", err)
		return 1
	}
	defer eng.Close() //nolint:errcheck
	parsedSubject, err := bead.ParseID(*subject)
	if err != nil {
		fmt.Fprintf(stderr, "medbeadsd trust attest: subject: %v\n", err)
		return 1
	}
	if _, err := eng.GetBead(parsedSubject); err != nil {
		fmt.Fprintf(stderr, "medbeadsd trust attest: subject %s: %v\n", parsedSubject, err)
		return 1
	}
	attestation, err := trust.BuildSignatureAttestation(parsedSubject, *purpose, *sourceSystemID, trust.Actor{
		ID: *actorID, DisplayName: *actorName, Role: *actorRole,
	}, *keyFile, privateKey, *signedAt)
	if err != nil {
		fmt.Fprintf(stderr, "medbeadsd trust attest: %v\n", err)
		return 1
	}
	attestation, err = bead.WithID(attestation)
	if err != nil {
		fmt.Fprintf(stderr, "medbeadsd trust attest: compute ID: %v\n", err)
		return 1
	}
	if _, err := trust.VerifySignatureAttestation(attestation, *policy, time.Now().UTC()); err != nil {
		fmt.Fprintf(stderr, "medbeadsd trust attest: verification before ingest failed: %v\n", err)
		return 1
	}
	saved, err := eng.Ingest(attestation)
	if err != nil {
		fmt.Fprintf(stderr, "medbeadsd trust attest: ingest: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "medbeadsd trust attest: signature_attestation Bead %s\n", saved.ID)
	return 0
}

func runTrustRelease(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("trust release", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data", "", "MedBeads data directory")
	releaseID := fs.String("release-id", "", "operator-stable release identifier")
	revisionLabel := fs.String("revision-label", "", "human-readable revision label")
	ruleIDsRaw := fs.String("rule-ids", "", "comma-separated link_rule Bead IDs")
	includeBuiltIn := fs.Bool("include-built-in", true, "include/seed this build's cooccurrence rule")
	effectiveFrom := fs.String("effective-from", "", "application start (RFC3339 or YYYY-MM-DD)")
	effectiveTo := fs.String("effective-to", "", "application end/expiry (RFC3339 or YYYY-MM-DD)")
	publishedAt := fs.String("published-at", "", "RFC3339 publication time; defaults to now")
	actorID := fs.String("actor-id", "", "approving actor ID")
	actorName := fs.String("actor-name", "", "approving actor display name snapshot")
	actorRole := fs.String("actor-role", "", "approving actor role")
	sourceSystemID := fs.String("source-system-id", "", "governance/source system ID")
	policyPath := fs.String("trust-policy", "", "public trust policy; defaults to <data>/trust/policy.json")
	privateKeyPath := fs.String("private-key", "", "local private key; defaults to <data>/trust/private-key.json")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dataDir == "" || *releaseID == "" || *actorID == "" || *sourceSystemID == "" {
		fmt.Fprintln(stderr, "medbeadsd trust release: -data, -release-id, -actor-id and -source-system-id are required")
		return 2
	}
	defaultTrustPaths(*dataDir, policyPath, privateKeyPath)
	policy, err := trust.LoadPolicy(*policyPath)
	if err != nil {
		fmt.Fprintf(stderr, "medbeadsd trust release: %v\n", err)
		return 1
	}
	keyFile, privateKey, err := trust.LoadPrivateKey(*privateKeyPath)
	if err != nil {
		fmt.Fprintf(stderr, "medbeadsd trust release: %v\n", err)
		return 1
	}
	if *publishedAt == "" {
		*publishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	eng, err := engine.Open(*dataDir)
	if err != nil {
		fmt.Fprintf(stderr, "medbeadsd trust release: open engine: %v\n", err)
		return 1
	}
	defer eng.Close() //nolint:errcheck

	ruleIDs, err := parseCSVBeadIDs(*ruleIDsRaw)
	if err != nil {
		fmt.Fprintf(stderr, "medbeadsd trust release: rule-ids: %v\n", err)
		return 1
	}
	if *includeBuiltIn {
		builtIn, err := eng.Ingest(projector.BuildCooccurrenceRuleBead("2026-01-01T00:00:00Z"))
		if err != nil {
			fmt.Fprintf(stderr, "medbeadsd trust release: seed built-in rule: %v\n", err)
			return 1
		}
		ruleIDs = append(ruleIDs, builtIn.ID)
	}
	ruleIDs = uniqueSorted(ruleIDs)
	if len(ruleIDs) == 0 {
		fmt.Fprintln(stderr, "medbeadsd trust release: at least one rule is required")
		return 2
	}
	for _, id := range ruleIDs {
		rule, err := eng.GetBead(id)
		if err != nil {
			fmt.Fprintf(stderr, "medbeadsd trust release: rule %s: %v\n", id, err)
			return 1
		}
		if rule.Type != "link_rule" {
			fmt.Fprintf(stderr, "medbeadsd trust release: Bead %s has type %q, want link_rule\n", id, rule.Type)
			return 1
		}
	}
	org, err := policyOrganization(*policy, keyFile.OrganizationID)
	if err != nil {
		fmt.Fprintf(stderr, "medbeadsd trust release: %v\n", err)
		return 1
	}
	release, err := trust.BuildKnowledgeRelease(trust.ReleaseSpec{
		ReleaseID:     *releaseID,
		RevisionLabel: *revisionLabel,
		Organization:  org,
		RuleBeadIDs:   ruleIDs,
		EffectiveFrom: *effectiveFrom,
		EffectiveTo:   *effectiveTo,
		PublishedAt:   *publishedAt,
		AuthorActorID: *actorID,
	})
	if err != nil {
		fmt.Fprintf(stderr, "medbeadsd trust release: %v\n", err)
		return 1
	}
	savedRelease, err := eng.Ingest(release)
	if err != nil {
		fmt.Fprintf(stderr, "medbeadsd trust release: ingest release: %v\n", err)
		return 1
	}
	approval, err := trust.BuildSignatureAttestation(savedRelease.ID, trust.PurposeKnowledgeRelease, *sourceSystemID, trust.Actor{
		ID: *actorID, DisplayName: *actorName, Role: *actorRole,
	}, *keyFile, privateKey, *publishedAt)
	if err != nil {
		fmt.Fprintf(stderr, "medbeadsd trust release: sign release: %v\n", err)
		return 1
	}
	approval, err = bead.WithID(approval)
	if err != nil {
		fmt.Fprintf(stderr, "medbeadsd trust release: compute approval ID: %v\n", err)
		return 1
	}
	if _, err := trust.VerifySignatureAttestation(approval, *policy, time.Now().UTC()); err != nil {
		fmt.Fprintf(stderr, "medbeadsd trust release: approval verification failed: %v\n", err)
		return 1
	}
	savedApproval, err := eng.Ingest(approval)
	if err != nil {
		fmt.Fprintf(stderr, "medbeadsd trust release: ingest approval: %v\n", err)
		return 1
	}
	knowledgeIDs := append(append([]string(nil), ruleIDs...), savedRelease.ID, savedApproval.ID)
	knowledgeIDs = uniqueSorted(knowledgeIDs)
	validation, validationErr := trust.ValidateKnowledgeSet(eng, knowledgeIDs, *policy, time.Now().UTC())
	var pending *trust.InsufficientApprovalsError
	if validationErr != nil && !errors.As(validationErr, &pending) {
		fmt.Fprintf(stderr, "medbeadsd trust release: closed-set verification failed: %v\n", validationErr)
		return 1
	}
	fmt.Fprintf(stdout, "medbeadsd trust release: knowledge_release Bead %s\n", savedRelease.ID)
	fmt.Fprintf(stdout, "medbeadsd trust release: approval signature_attestation Bead %s\n", savedApproval.ID)
	if pending != nil {
		fmt.Fprintf(stdout, "medbeadsd trust release: pending approvals %d/%d; add signature_attestation Beads before activation\n", pending.Have, pending.Need)
		fmt.Fprintf(stdout, "medbeadsd trust release: pending knowledge IDs %s\n", strings.Join(knowledgeIDs, ","))
		return 0
	}
	fmt.Fprintf(stdout, "medbeadsd trust release: verified release %s with %d approval actor(s)\n",
		validation.ReleaseBeadID, len(validation.ValidApprovalActors))
	fmt.Fprintf(stdout, "medbeadsd trust release: verified knowledge IDs %s\n", strings.Join(knowledgeIDs, ","))
	fmt.Fprintf(stdout, "medbeadsd trust release: activate with: medbeadsd reproject -data %s -trust-policy %s -knowledge-ids %s\n",
		*dataDir, *policyPath, strings.Join(knowledgeIDs, ","))
	return 0
}

func defaultTrustPaths(dataDir string, policyPath, privateKeyPath *string) {
	if *policyPath == "" {
		*policyPath = filepath.Join(dataDir, "trust", "policy.json")
	}
	if *privateKeyPath == "" {
		*privateKeyPath = filepath.Join(dataDir, "trust", "private-key.json")
	}
}

func writeJSONFile(path string, value any, mode os.FileMode, force bool) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create %s directory: %w", path, err)
	}
	flags := os.O_WRONLY | os.O_CREATE
	if force {
		flags |= os.O_TRUNC
	} else {
		flags |= os.O_EXCL
	}
	file, err := os.OpenFile(path, flags, mode)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	if err := file.Chmod(mode); err != nil {
		file.Close() //nolint:errcheck
		return fmt.Errorf("set permissions on %s: %w", path, err)
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		file.Close() //nolint:errcheck
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := file.Sync(); err != nil {
		file.Close() //nolint:errcheck
		return fmt.Errorf("sync %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	return nil
}

func parseCSVBeadIDs(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var ids []string
	for _, value := range strings.Split(raw, ",") {
		id, err := bead.ParseID(strings.TrimSpace(value))
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return uniqueSorted(ids), nil
}

func uniqueSorted(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	sortStrings(out)
	return out
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

func policyOrganization(policy trust.Policy, organizationID string) (trust.Organization, error) {
	for _, org := range policy.Organizations {
		if org.ID == organizationID {
			return org, nil
		}
	}
	return trust.Organization{}, fmt.Errorf("private key organization %q is not in trust policy", organizationID)
}
