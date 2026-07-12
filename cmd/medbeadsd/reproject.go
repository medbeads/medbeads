// runReproject implements `medbeadsd reproject`: a minimal CLI entry point
// for internal/engine/projector's U3b Reproject (specs/U3_link_projector.md's
// U3b section) — the full-reprojection of clinical_links from the
// already-indexed bead_tags/beads plus the cooccurrence link_rule Bead,
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
// clinical_links Reproject completes — bead_status/active_conditions/
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
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/medbeads/medbeads/internal/engine"
	"github.com/medbeads/medbeads/internal/engine/projector"
)

// reprojectEngineReader adapts *engine.Engine to projector's unexported
// beadReader interface (Go structural typing satisfies it via this
// package's own GetBead-returning-BeadContent method).
type reprojectEngineReader struct{ e *engine.Engine }

func (r reprojectEngineReader) GetBead(id string) (projector.BeadContent, error) {
	b, err := r.e.GetBead(id)
	if err != nil {
		return projector.BeadContent{}, err
	}
	return projector.BeadContent{Content: b.Content}, nil
}

// runReproject implements `medbeadsd reproject -data <dir> [-code-version <v>]
// [-record-state]`.
//
// Exit codes follow this package's existing convention (verify/reindex/
// apc/embed): 0 (Reproject, and record_state if requested, ran to
// completion), 1 (engine/projector error), 2 (usage error).
func runReproject(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("reproject", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data", "", "MedBeads data directory (contains pods/, dict/, index.db)")
	codeVersion := fs.String("code-version", "dev", "opaque code_version string recorded in projection_manifest (e.g. a git SHA)")
	recordState := fs.Bool("record-state", false, "also run U4b's record_state projector (bead_status/active_conditions/active_medications)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dataDir == "" {
		fmt.Fprintln(stderr, "medbeadsd reproject: -data <dir> is required")
		return 2
	}

	eng, err := engine.Open(*dataDir)
	if err != nil {
		fmt.Fprintf(stderr, "medbeadsd reproject: open engine: %v\n", err)
		return 1
	}
	defer eng.Close() //nolint:errcheck // best-effort unwind; process is exiting either way

	ruleID, err := ensureCooccurrenceRule(eng)
	if err != nil {
		fmt.Fprintf(stderr, "medbeadsd reproject: seed link_rule: %v\n", err)
		return 1
	}

	builtAt := time.Now().UTC().Format(time.RFC3339)
	res, err := projector.Reproject(eng.Index(), reprojectEngineReader{eng}, []string{ruleID}, *codeVersion, builtAt)
	if err != nil {
		fmt.Fprintf(stderr, "medbeadsd reproject: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "medbeadsd reproject: run %s: %d patient(s) projected, %d clinical_link(s) written\n",
		res.RunID, res.PatientsProjected, res.LinksWritten)

	if *recordState {
		// A fresh builtAt (not the same string as the clinical_links run
		// above): the two projectors are lineage-independent runs (see this
		// file's doc comment), and computeRunID's own determinism-via-builtAt
		// discipline (projector/reproject.go) expects a real caller to supply
		// a fresh value per actual invocation.
		statusBuiltAt := time.Now().UTC().Format(time.RFC3339)
		statusRes, err := projector.StatusReproject(eng.Index(), eng, *codeVersion, statusBuiltAt)
		if err != nil {
			fmt.Fprintf(stderr, "medbeadsd reproject: record_state: %v\n", err)
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
func ensureCooccurrenceRule(eng *engine.Engine) (string, error) {
	ruleBead := projector.BuildCooccurrenceRuleBead("2026-01-01T00:00:00Z")
	saved, err := eng.Ingest(ruleBead)
	if err != nil {
		return "", fmt.Errorf("ingest link_rule: %w", err)
	}
	return saved.ID, nil
}
