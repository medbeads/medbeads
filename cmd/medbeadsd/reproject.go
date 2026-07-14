// runReproject implements `medbeadsd reproject`: the rolling CLI entry point
// for rebuilding clinical_links from already-indexed bead_tags/beads plus
// explicitly selected link_rule Beads,
// distinct from `reindex` (which rebuilds index.db from Pod files;
// Reproject never touches Pods — see projector.Reproject's own doc comment).
//
// This subcommand also seeds the built-in cooccurrence link_rule Bead
// (projector.BuildCooccurrenceRuleBead), so an operator can bootstrap a
// fresh store with a single `reproject` call rather than needing a
// separate seeding step. Seeding always ingests THIS build's own rule Bead
// (a no-op if content-identical to one already present) and explicitly
// names its ID to the projector rather than asking "is anything with this
// rule_id already seeded" — see ensureCooccurrenceRule's doc comment for
// why the latter would silently ignore a code-level rule revision.
//
// With -record-state, it additionally runs U4b's record_state projector
// (projector.StatusReproject, specs/U4_state_derivation.md) after
// the selected clinical_links batch completes — bead_status/active_conditions/
// active_medications, a separate manifest lineage (StatusProjectionName)
// from clinical_links' own, so the two runs' manifest flips are independent
// (a failure in one does not roll back the other; see StatusReproject's own
// doc comment on why they are lineage-independent). This is folded into the
// existing `reproject` subcommand rather than a new subcommand, per the U4
// task's own "extend cmd/medbeadsd/reproject.go … — minimal" instruction:
// both projectors are "recompute derived state from what's already indexed",
// the natural operational grouping for a single reprojection pass.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/medbeads/medbeads/internal/engine"
	"github.com/medbeads/medbeads/internal/engine/bead"
	"github.com/medbeads/medbeads/internal/engine/projector"
	"github.com/medbeads/medbeads/internal/engine/trust"
)

// runReproject implements `medbeadsd reproject -data <dir> [-code-version <v>]
// [-record-state]`.
//
// Exit codes follow this package's existing convention (verify/reindex/
// embed): 0 (activation/batch, and record_state if requested, ran to
// completion), 1 (engine/projector error), 2 (usage error).
func runReproject(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("reproject", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data", "", "MedBeads data directory (contains pods/, dict/, index.db)")
	codeVersion := fs.String("code-version", engine.DefaultProjectionCodeVersion(), "opaque code_version string recorded in projection_manifest (e.g. a git SHA)")
	recordStateCodeVersion := fs.String("record-state-code-version", engine.DefaultRecordStateProjectionCodeVersion(), "record_state algorithm contract version")
	recordState := fs.Bool("record-state", false, "also run U4b's record_state projector (bead_status/active_conditions/active_medications)")
	ruleFile := fs.String("rule-file", "", "JSON file of curated link rules to publish as knowledge Beads before projecting")
	trustPolicyPath := fs.String("trust-policy", "", "public trust policy JSON; when require_knowledge_release=true only a verified release can become active")
	knowledgeIDsRaw := fs.String("knowledge-ids", "", "comma-separated closed set from 'medbeadsd trust release' (link_rule + knowledge_release + signature_attestation IDs)")
	batchSize := fs.Int("batch-size", 100, "maximum patients to update now; 0 only activates and queues the generation")
	inactiveAfter := fs.Duration("inactive-after", engine.DefaultReprojectionInactiveAfter, "patients without an encounter in this window are processed after recent patients")
	drain := fs.Bool("drain", false, "continue prioritized batches until the current link queue is empty")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dataDir == "" {
		fmt.Fprintln(stderr, "medbeadsd reproject: -data <dir> is required")
		return 2
	}
	knowledgeIDs, err := parseCSVBeadIDs(*knowledgeIDsRaw)
	if err != nil {
		fmt.Fprintf(stderr, "medbeadsd reproject: -knowledge-ids: %v\n", err)
		return 2
	}
	var trustPolicy *trust.Policy
	if *trustPolicyPath != "" {
		trustPolicy, err = trust.LoadPolicy(*trustPolicyPath)
		if err != nil {
			fmt.Fprintf(stderr, "medbeadsd reproject: %v\n", err)
			return 1
		}
		if trustPolicy.RequireKnowledgeRelease && len(knowledgeIDs) == 0 {
			fmt.Fprintln(stderr, "medbeadsd reproject: -knowledge-ids is required by this trust policy")
			return 2
		}
	}
	if len(knowledgeIDs) > 0 && trustPolicy == nil {
		fmt.Fprintln(stderr, "medbeadsd reproject: -knowledge-ids requires -trust-policy")
		return 2
	}
	if len(knowledgeIDs) > 0 && *ruleFile != "" {
		fmt.Fprintln(stderr, "medbeadsd reproject: publish rules first, then sign them; -rule-file and -knowledge-ids cannot be combined")
		return 2
	}

	eng, err := engine.OpenWithOptions(*dataDir, engine.OpenOptions{
		AutoProject:                  true,
		ProjectionCodeVersion:        *codeVersion,
		RecordStateProjectionVersion: *recordStateCodeVersion,
		TrustPolicy:                  trustPolicy,
		InitialKnowledgeBeadIDs:      knowledgeIDs,
	})
	if err != nil {
		fmt.Fprintf(stderr, "medbeadsd reproject: open engine: %v\n", err)
		return 1
	}
	defer eng.Close() //nolint:errcheck // best-effort unwind; process is exiting either way

	knowledgeBeadIDs := append([]string(nil), knowledgeIDs...)
	if len(knowledgeBeadIDs) == 0 {
		ruleID, err := ensureCooccurrenceRule(eng)
		if err != nil {
			fmt.Fprintf(stderr, "medbeadsd reproject: seed link_rule: %v\n", err)
			return 1
		}
		knowledgeBeadIDs = []string{ruleID}
	}

	// Publishing curated knowledge is an ordinary Bead write: the rule becomes an
	// immutable, content-addressed fact. Re-running with the same file is a no-op
	// (Ingest early-returns on an already-present content hash), and REVISING a
	// rule means publishing a NEW Bead — the old one is never rewritten, so a
	// warning already in the store keeps naming the exact rule text that
	// justified it.
	if *ruleFile != "" {
		curatedIDs, err := publishCuratedRules(eng, *ruleFile)
		if err != nil {
			fmt.Fprintf(stderr, "medbeadsd reproject: publish curated rules: %v\n", err)
			return 1
		}
		for _, id := range curatedIDs {
			fmt.Fprintf(stdout, "medbeadsd reproject: curated rule Bead %s\n", id)
		}
		knowledgeBeadIDs = append(knowledgeBeadIDs, curatedIDs...)
	}

	builtAt := time.Now().UTC().Format(time.RFC3339Nano)
	var activation engine.RollingActivation
	if trustPolicy != nil && len(knowledgeIDs) > 0 {
		trustedActivation, err := eng.ActivateKnowledgeRelease(knowledgeBeadIDs, *codeVersion, builtAt, time.Now().UTC())
		if err != nil {
			fmt.Fprintf(stderr, "medbeadsd reproject: %v\n", err)
			return 1
		}
		activation = trustedActivation.Rolling
		fmt.Fprintf(stdout, "medbeadsd reproject: verified release %s: organization=%s approvals=%d rules=%d\n",
			trustedActivation.Validation.ReleaseBeadID,
			trustedActivation.Validation.OrganizationID,
			len(trustedActivation.Validation.ValidApprovalActors),
			len(trustedActivation.Validation.RuleBeadIDs))
	} else {
		activation, err = eng.ActivateLinkKnowledge(knowledgeBeadIDs, *codeVersion, builtAt)
		if err != nil {
			fmt.Fprintf(stderr, "medbeadsd reproject: %v\n", err)
			return 1
		}
	}
	fmt.Fprintf(stdout, "medbeadsd reproject: rolling run %s: %d patient(s) queued (already_active=%t)\n",
		activation.RunID, activation.QueuedPatients, activation.AlreadyActive)

	limit := *batchSize
	if *drain && limit <= 0 {
		limit = 100
	}
	for {
		batch, err := eng.ProcessLinkReprojectionQueue(limit, time.Now().UTC(), *inactiveAfter)
		if err != nil {
			fmt.Fprintf(stderr, "medbeadsd reproject: process queue: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "medbeadsd reproject: batch projected=%d (recent=%d inactive=%d deceased=%d failed=%d), remaining=%d\n",
			batch.Projected, batch.Recent, batch.Inactive, batch.Deceased, batch.Failed, batch.Remaining)
		if !*drain || batch.Remaining == 0 || batch.Projected == 0 {
			break
		}
	}

	if *recordState {
		// A fresh builtAt (not the same string as the clinical_links run
		// above): the two projectors are lineage-independent runs (see this
		// file's doc comment), and computeRunID's own determinism-via-builtAt
		// discipline (projector/reproject.go) expects a real caller to supply
		// a fresh value per actual invocation.
		statusBuiltAt := time.Now().UTC().Format(time.RFC3339)
		statusRes, err := projector.StatusReproject(eng.Index(), eng, *recordStateCodeVersion, statusBuiltAt)
		if err != nil {
			fmt.Fprintf(stderr, "medbeadsd reproject: record_state: %v\n", err)
			return 1
		}
		if _, err := eng.Index().SQLDB().Exec(`
			UPDATE patient_projection_state
			SET record_state_run_id=?, projected_at=?`,
			statusRes.RunID, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			fmt.Fprintf(stderr, "medbeadsd reproject: update record_state checkpoints: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "medbeadsd reproject: record_state run %s: %d patient(s) projected, "+
			"%d bead_status row(s), %d active_conditions row(s), %d active_medications row(s)\n",
			statusRes.RunID, statusRes.PatientsProjected, statusRes.BeadStatusWritten,
			statusRes.ActiveConditions, statusRes.ActiveMedications)
	}

	return 0
}

// ensureCooccurrenceRule ingests this package's built-in cooccurrence
// link_rule Bead (projector.BuildCooccurrenceRuleBead) — a no-op if a
// content-identical Bead is already present (bead IDs are content hashes;
// see engine.Ingest's "idempotent replay" doc comment) — and returns its ID
// (clinical_links.rule_version's value) either way.
//
// This deliberately does NOT call projector.LoadActiveCooccurrenceRule
// first to check "is some rule already seeded". rule_id
// (projector.CooccurrenceRuleID) is a stable key across revisions BY
// DESIGN — that's the whole point of rule_version being the Bead's own
// content hash, a separate field, precisely so a rule's content can be
// revised without changing its rule_id (specs/U2_projection_schema.md).
// But it means LoadActiveCooccurrenceRule with no knowledgeBeadIDs filter
// (its "greatest ID among every matching rule_id Bead wins" mode) would
// happily keep matching an OLDER same-rule_id Bead already in the store and
// return early, before this package's own current
// BuildCooccurrenceRuleBead is ever computed or ingested — silently
// pinning every reproject run to stale knowledge no matter how this
// package's own rule content is revised. (This is exactly the bug this
// function used to have: an older rule Bead already present in the store
// made every subsequent build's own reordered TriggerNamespaces a 100%
// no-op, because it never even got ingested, let alone selected.)
//
// Always computing and ingesting the CURRENT build's rule Bead, then
// handing its own ID to loadRule's knowledgeBeadIDs filter (reproject.go's
// loadRule, which already implements "restrict candidates to exactly this
// set" — see its own doc comment), is what makes selection track code
// instead of "whatever happened to be seeded first": the current build's
// rule Bead is guaranteed both present and selected, and an older
// same-rule_id Bead from a prior revision is left untouched in the shared
// Pod (knowledge Beads are immutable; a superseded rule Bead is not deleted
// or overwritten, only no longer the one this call names).
//
// The seeding timestamp is a fixed literal (not time.Now()) so that two
// independent fresh-store bootstraps mint the byte-identical rule Bead ID —
// see BuildCooccurrenceRuleBead's own doc comment on why a knowledge Bead's
// ID must not depend on when it happened to be seeded.
// curatedRuleFile is the on-disk shape of `-rule-file`: curated pair rules to
// publish as knowledge Beads.
//
//	{
//	  "rules": [
//	    {
//	      "rule_id":   "ddi-warfarin-nsaid-v1",
//	      "relation":  "drug_drug_interaction",
//	      "severity":  "warning",
//	      "tag_pairs": [["atc:b01aa03", "atc:m01ae01"]],
//	      "timestamp": "2026-01-01T00:00:00Z",
//	      "author": "did:medbeads:pharmacist:123"
//	    }
//	  ]
//	}
//
// timestamp is caller-supplied rather than time.Now() for the reason every
// Bead-minting path in this codebase demands it: a knowledge Bead's ID must
// depend only on its content, so publishing the same rule twice — from a script,
// a re-run, a second operator — collapses onto the same content-addressed Bead
// instead of littering the fact layer with duplicates of the same knowledge.
type curatedRuleFile struct {
	Rules []struct {
		RuleID          string      `json:"rule_id"`
		RevisionLabel   string      `json:"revision_label"`
		Relation        string      `json:"relation"`
		Severity        string      `json:"severity"`
		EvidenceBasis   string      `json:"evidence_basis"`
		EvidenceBeadIDs []string    `json:"evidence_bead_ids"`
		TagPairs        [][2]string `json:"tag_pairs"`
		Timestamp       string      `json:"timestamp"`
		EffectiveFrom   string      `json:"effective_from"`
		EffectiveTo     string      `json:"effective_to"`
		Author          string      `json:"author"`
		Signature       string      `json:"signature"`
	} `json:"rules"`
}

// publishCuratedRules reads path, mints a link_rule Bead per declared rule,
// ingests it, and returns the resulting Bead IDs — the rule_versions the
// projector stamps on every warning it derives from them.
//
// The severity floor is deliberately NOT re-checked here: it is enforced by
// clinical_links' CHECK constraint at INSERT time. A rule declaring a severity
// above `info` produces links naming this Bead as their evidence, and the
// database accepts them precisely because they can name it. Duplicating that rule
// in the CLI would only create a second place for the two to disagree.
func publishCuratedRules(eng *engine.Engine, path string) ([]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var file curatedRuleFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if len(file.Rules) == 0 {
		return nil, fmt.Errorf("parse %s: no rules declared", path)
	}

	ids := make([]string, 0, len(file.Rules))
	for i, r := range file.Rules {
		if r.RuleID == "" || r.Relation == "" || r.Severity == "" || len(r.TagPairs) == 0 {
			return nil, fmt.Errorf("rule[%d]: rule_id, relation, severity and tag_pairs are all required", i)
		}
		if r.Timestamp == "" {
			return nil, fmt.Errorf("rule[%d] (%s): timestamp is required — a knowledge Bead's ID must not depend on when it happened to be minted", i, r.RuleID)
		}
		if r.Author == "" {
			return nil, fmt.Errorf("rule[%d] (%s): author DID/identifier is required", i, r.RuleID)
		}
		if r.Signature != "" {
			return nil, fmt.Errorf("rule[%d] (%s): inline signature is not a trusted signature; publish the rule, then use 'medbeadsd trust release'", i, r.RuleID)
		}
		if err := validateEffectivePeriod(r.EffectiveFrom, r.EffectiveTo); err != nil {
			return nil, fmt.Errorf("rule[%d] (%s): %w", i, r.RuleID, err)
		}
		evidenceIDs := make([]string, 0, len(r.EvidenceBeadIDs))
		for _, externalID := range r.EvidenceBeadIDs {
			id, err := bead.ParseID(externalID)
			if err != nil {
				return nil, fmt.Errorf("rule[%d] (%s): evidence_bead_id %q: %w", i, r.RuleID, externalID, err)
			}
			if _, err := eng.GetBead(id); err != nil {
				return nil, fmt.Errorf("rule[%d] (%s): evidence Bead %s is not available: %w", i, r.RuleID, id, err)
			}
			evidenceIDs = append(evidenceIDs, id)
		}
		if r.EvidenceBasis == "guideline" && len(evidenceIDs) == 0 {
			return nil, fmt.Errorf("rule[%d] (%s): guideline rules require at least one evidence_bead_id", i, r.RuleID)
		}

		ruleBead := projector.BuildCuratedPairRuleBead(r.RuleID, r.Relation, r.Severity, r.TagPairs, r.Timestamp)
		ruleBead.Author = r.Author
		if r.RevisionLabel != "" {
			ruleBead.Content["revision_label"] = r.RevisionLabel
		}
		if r.EvidenceBasis != "" {
			ruleBead.Content["evidence_basis"] = r.EvidenceBasis
		}
		if len(evidenceIDs) > 0 {
			ruleBead.Content["evidence_bead_ids"] = evidenceIDs
		}
		if r.EffectiveFrom != "" || r.EffectiveTo != "" {
			ruleBead.Content["effective_period"] = map[string]any{
				"from": r.EffectiveFrom,
				"to":   r.EffectiveTo,
			}
		}
		saved, err := eng.Ingest(ruleBead)
		if err != nil {
			return nil, fmt.Errorf("ingest rule %s: %w", r.RuleID, err)
		}
		ids = append(ids, saved.ID)
	}
	return ids, nil
}

func validateEffectivePeriod(from, to string) error {
	parse := func(name, value string) (time.Time, error) {
		if value == "" {
			return time.Time{}, nil
		}
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02"} {
			if parsed, err := time.Parse(layout, value); err == nil {
				return parsed, nil
			}
		}
		return time.Time{}, fmt.Errorf("%s must be RFC3339 or YYYY-MM-DD", name)
	}
	fromTime, err := parse("effective_from", from)
	if err != nil {
		return err
	}
	toTime, err := parse("effective_to", to)
	if err != nil {
		return err
	}
	if !fromTime.IsZero() && !toTime.IsZero() && toTime.Before(fromTime) {
		return fmt.Errorf("effective_to precedes effective_from")
	}
	return nil
}

func ensureCooccurrenceRule(eng *engine.Engine) (string, error) {
	ruleBead := projector.BuildCooccurrenceRuleBead("2026-01-01T00:00:00Z")
	saved, err := eng.Ingest(ruleBead)
	if err != nil {
		return "", fmt.Errorf("ingest link_rule: %w", err)
	}
	return saved.ID, nil
}
