// runReproject implements `medbeadsd reproject`: a minimal CLI entry point
// for internal/engine/projector's U3b Reproject (specs/U3_link_projector.md's
// U3b section) — the full-reprojection of clinical_links from the
// already-indexed bead_tags/beads plus the cooccurrence link_rule Bead,
// distinct from `reindex` (which rebuilds index.db from Pod files;
// Reproject never touches Pods — see projector.Reproject's own doc comment).
//
// This subcommand also seeds the built-in cooccurrence link_rule Bead
// (projector.BuildCooccurrenceRuleBead) if one is not already present in
// the shared Pod, so an operator can bootstrap a fresh store with a single
// `reproject` call rather than needing a separate seeding step.
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
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/medbeads/medbeads/internal/engine"
	"github.com/medbeads/medbeads/internal/engine/index"
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

// ensureCooccurrenceRule finds the already-seeded cooccurrence link_rule
// Bead (projector.LoadActiveCooccurrenceRule), or ingests
// projector.BuildCooccurrenceRuleBead if none exists yet, returning the
// rule Bead's own ID (clinical_links.rule_version's value) either way.
//
// The seeding timestamp is a fixed literal (not time.Now()) so that two
// independent fresh-store bootstraps mint the byte-identical rule Bead ID —
// see BuildCooccurrenceRuleBead's own doc comment on why a knowledge Bead's
// ID must not depend on when it happened to be seeded.
func ensureCooccurrenceRule(eng *engine.Engine) (string, error) {
	rule, err := projector.LoadActiveCooccurrenceRule(eng.Index(), func(id string) (map[string]any, error) {
		b, err := eng.GetBead(id)
		if err != nil {
			return nil, err
		}
		return b.Content, nil
	})
	if err == nil {
		return rule.RuleVersion, nil
	}
	if !errors.Is(err, index.ErrNotFound) {
		return "", fmt.Errorf("load link_rule: %w", err)
	}

	ruleBead := projector.BuildCooccurrenceRuleBead("2026-01-01T00:00:00Z")
	saved, err := eng.Ingest(ruleBead)
	if err != nil {
		return "", fmt.Errorf("ingest link_rule: %w", err)
	}
	return saved.ID, nil
}
