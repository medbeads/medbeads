// runApc implements `medbeadsd apc`: it runs the APC batch scanner
// (internal/engine/apc, docs/requirements.md R5) directly from the CLI,
// rather than only through the MCP apc_trigger tool (internal/mcpserver).
// apc_trigger's single-tool-call shape is fragile for a long-running batch
// like the 96万-Bead store's initial full scan; `apc` is a first-class
// subcommand alongside verify/reindex for that reason.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/medbeads/medbeads/internal/engine"
	"github.com/medbeads/medbeads/internal/engine/apc"
	"github.com/medbeads/medbeads/internal/engine/bead"
)

// progressEveryNPatients controls how often runApc's all-patients loop
// reports progress to stderr (see runApc's doc comment: "長時間バッチで無音
// は不安" — a 96万-Bead / 1,135-patient-scale full scan must not go
// completely silent for its whole run).
const progressEveryNPatients = 100

// runApc implements `medbeadsd apc -data <dir> [-patient <root>]`.
//
// With no -patient, it scans every patient in the store: it lists every
// patient (index.DB.ListPatients, i.e. every patient_registration Bead —
// engine.resolvePatientRoot makes every non-empty patient_root exactly one
// such Bead's own ID, so this enumerates every possible patient_root value
// that can appear in the store) and calls apc.Scanner.ScanPatient once per
// patient, reporting progress every progressEveryNPatients patients. A
// final apc.Scanner.Scan() call (unfiltered) then sweeps any anchors
// ScanPatient's per-patient loop cannot reach — Beads with no patient_root
// at all (the shared Pod; see resolvePatientRoot's "no parents: shared
// Pod" case). Note the trailing sweep also picks up sibling_link Beads the
// per-patient loop itself created, so a single run of this subcommand
// converges further than one direct Scan() call would (never less); the
// fully-converged sibling_link set is identical either way (data-
// deterministic — verified by probe against repeated Scan() to fixed
// point). The per-patient loop only exists to get patient-granular
// progress out of an otherwise-opaque single Scan() call. Scan is itself
// idempotent (an already-watermarked Bead is never re-examined as an
// anchor, see Scan's own doc comment) so this trailing sweep never
// re-examines any Bead the per-patient loop already visited.
//
// With -patient <root>, it scans only that one patient via
// apc.Scanner.ScanPatient (not RescanPatient, which only clears a prior
// watermark and does not itself scan anything — see RescanPatient's doc
// comment) — the "通常 Scan の患者スコープ" this unit's task calls for.
// root is parsed with bead.ParseID, so both the bare 64-hex-char form and
// the "sha256:"-prefixed display form are accepted.
//
// Exit codes follow verify's convention: 0 (scan ran to completion), 1
// (scan/engine error), 2 (usage error).
func runApc(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("apc", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data", "", "MedBeads data directory (contains pods/, dict/, index.db)")
	patientArg := fs.String("patient", "", "if set, scan only this patient (patient_root, sha256: prefix optional); default scans every patient")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dataDir == "" {
		fmt.Fprintln(stderr, "medbeadsd apc: -data <dir> is required")
		return 2
	}

	var patientRoot string
	if *patientArg != "" {
		root, err := bead.ParseID(*patientArg)
		if err != nil {
			fmt.Fprintf(stderr, "medbeadsd apc: -patient: %v\n", err)
			return 2
		}
		patientRoot = root
	}

	eng, err := engine.Open(*dataDir)
	if err != nil {
		fmt.Fprintf(stderr, "medbeadsd apc: open engine: %v\n", err)
		return 1
	}
	defer eng.Close() //nolint:errcheck // best-effort unwind; process is exiting either way

	scanner := apc.New(eng, eng.Index(), apc.Default())

	totalBeadsBefore, err := countBeads(eng)
	if err != nil {
		fmt.Fprintf(stderr, "medbeadsd apc: %v\n", err)
		return 1
	}

	start := time.Now()
	var result apc.Result

	if patientRoot != "" {
		result, err = scanner.ScanPatient(patientRoot)
		if err != nil {
			fmt.Fprintf(stderr, "medbeadsd apc: scan patient %s: %v\n", patientRoot, err)
			return 1
		}
	} else {
		result, err = runApcAllPatients(scanner, eng, stderr)
		if err != nil {
			fmt.Fprintf(stderr, "medbeadsd apc: %v\n", err)
			return 1
		}
	}

	elapsed := time.Since(start)
	skipped := totalBeadsBefore - result.BeadsScanned
	if skipped < 0 {
		// Should not happen (BeadsScanned cannot exceed the Bead count taken
		// before the scan started, since Scan/ScanPatient only examine
		// Beads that already existed at call time), but never report a
		// nonsensical negative skip count if store state is unusual.
		skipped = 0
	}

	fmt.Fprintf(stdout, "medbeadsd apc: scanned %d bead(s), created %d sibling_link(s), skipped %d (already scanned), in %s\n",
		result.BeadsScanned, result.SiblingLinksCreated, skipped, elapsed.Round(time.Millisecond))
	return 0
}

// runApcAllPatients drives the per-patient progress loop described in
// runApc's doc comment: apc.Scanner.ScanPatient once per patient (index.DB.
// ListPatients order — most-recently-registered first; scan order does not
// affect the result, only progress-reporting order), reporting to stderr
// every progressEveryNPatients patients, followed by one unfiltered Scan()
// call to sweep any Beads with no patient_root at all.
func runApcAllPatients(scanner *apc.Scanner, eng *engine.Engine, stderr *os.File) (apc.Result, error) {
	patients, err := eng.Index().ListPatients()
	if err != nil {
		return apc.Result{}, fmt.Errorf("list patients: %w", err)
	}

	var total apc.Result
	for i, p := range patients {
		res, err := scanner.ScanPatient(p.ID)
		if err != nil {
			return total, fmt.Errorf("scan patient %s: %w", p.ID, err)
		}
		total.BeadsScanned += res.BeadsScanned
		total.SiblingLinksCreated += res.SiblingLinksCreated

		if (i+1)%progressEveryNPatients == 0 {
			fmt.Fprintf(stderr, "medbeadsd apc: %d/%d patients scanned (%d bead(s), %d sibling_link(s) so far)\n",
				i+1, len(patients), total.BeadsScanned, total.SiblingLinksCreated)
		}
	}
	if len(patients) > 0 && len(patients)%progressEveryNPatients != 0 {
		fmt.Fprintf(stderr, "medbeadsd apc: %d/%d patients scanned (%d bead(s), %d sibling_link(s) so far)\n",
			len(patients), len(patients), total.BeadsScanned, total.SiblingLinksCreated)
	}

	// Sweep any anchor ScanPatient's per-patient loop cannot reach: Beads
	// with no patient_root (the shared Pod). Scan's incremental watermark
	// means every Bead the loop above already visited is skipped here, so
	// this only examines shared-Pod Beads (which scanOne never links, see
	// Scan's "Sibling matching is patient-scoped by design" comment, but
	// which Scan still marks scanned) plus, defensively, any patient Bead
	// the loop above somehow missed.
	sweep, err := scanner.Scan()
	if err != nil {
		return total, fmt.Errorf("final sweep: %w", err)
	}
	total.BeadsScanned += sweep.BeadsScanned
	total.SiblingLinksCreated += sweep.SiblingLinksCreated

	return total, nil
}

// countBeads returns the total number of Beads currently indexed, for
// runApc's "skipped (already scanned)" summary figure: total Beads minus
// the number this run examined as new anchors.
func countBeads(eng *engine.Engine) (int, error) {
	var n int
	if err := eng.Index().SQLDB().QueryRow(`SELECT COUNT(*) FROM beads`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count beads: %w", err)
	}
	return n, nil
}
