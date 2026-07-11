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

// runReproject implements `medbeadsd reproject -data <dir> [-code-version <v>]`.
//
// Exit codes follow this package's existing convention (verify/reindex/
// apc/embed): 0 (Reproject ran to completion), 1 (engine/projector error),
// 2 (usage error).
func runReproject(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("reproject", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data", "", "MedBeads data directory (contains pods/, dict/, index.db)")
	codeVersion := fs.String("code-version", "dev", "opaque code_version string recorded in projection_manifest (e.g. a git SHA)")
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
