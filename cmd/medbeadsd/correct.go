// runCorrect implements `medbeadsd correct amend|retract|attest` — the operator
// path for writing a correction.
//
// # Why this exists
//
// MedBeads' fact layer is append-only and content-addressed: a record is never
// edited. A correction is a NEW Bead pointing at the one it corrects — `amends`
// supersedes it, `retracts` withdraws it as entered-in-error, and an
// `attestation` is the clinician sign-off that decides whether an amendment
// becomes the patient's current version at all (projector/resolve.go).
//
// All of that was implemented, unit-tested, and rendered by the UI — and had
// never once been exercised, because the only way to write a Bead was MCP
// `create_bead` under `-role system`. There was no way for an operator to correct
// a record in a real store. This subcommand is that way.
//
// # What it deliberately does NOT do
//
// It does not add a REST write endpoint. REST is this system's read contract
// (`beadView` is explicitly frozen); `POST /clearance` writes an index row, which
// is mutable policy, not an immutable fact. Appending to the fact layer and
// updating a policy table are categorically different operations and should not
// share a surface.
//
// It does not authenticate. `-author` is an assertion by an operator who already
// holds filesystem access to the store — attributable, not authenticated, exactly
// like `X-User-ID` on POST /clearance. bead.Bead.Signature sits outside the
// content hash and is where a future DID/JWS binding belongs. What this command
// DOES guarantee is that a correction cannot be written anonymously.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/medbeads/medbeads/internal/engine"
	"github.com/medbeads/medbeads/internal/engine/bead"
)

const correctUsage = `medbeadsd correct - write a correction Bead

Usage:
  medbeadsd correct amend   -data <dir> -target <bead-id> -author <who> -content <json-file> [-type <t>] [-timestamp <rfc3339>]
  medbeadsd correct retract -data <dir> -target <bead-id> -author <who> -reason <text>                   [-timestamp <rfc3339>]
  medbeadsd correct attest  -data <dir> -target <bead-id> -author <who> -verdict approved|rejected [-reason <text>] [-timestamp <rfc3339>]

A correction is a new, immutable Bead — nothing is ever edited in place:

  amend    supersede <target> with corrected content. It does NOT become the
           patient's current version until an attestation approves it.
  retract  withdraw <target> as entered-in-error. Takes effect immediately.
  attest   approve or reject an amendment (pass the AMENDMENT's Bead ID as
           -target). Only an approved amendment becomes current.

-author is required: a correction that cannot name who made it is not auditable.
It is an assertion, not an authenticated identity.

-timestamp is the CLINICAL event time (default: now). It does NOT decide which of
two competing corrections wins — that is the Pod append order.

After writing, re-derive record status:
  medbeadsd reproject -data <dir> -record-state
`

func runCorrect(args []string, stdout, stderr *os.File) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, correctUsage)
		return 2
	}

	switch sub := args[0]; sub {
	case "amend":
		return runCorrectAmend(args[1:], stdout, stderr)
	case "retract":
		return runCorrectRetract(args[1:], stdout, stderr)
	case "attest":
		return runCorrectAttest(args[1:], stdout, stderr)
	case "-h", "-help", "--help", "help":
		fmt.Fprint(stdout, correctUsage)
		return 0
	default:
		fmt.Fprintf(stderr, "medbeadsd correct: unknown subcommand %q\n\n%s", sub, correctUsage)
		return 2
	}
}

// correctCommon holds the flags every correction subcommand shares.
type correctCommon struct {
	dataDir   string
	target    string
	author    string
	timestamp string
}

func (c *correctCommon) bind(fs *flag.FlagSet) {
	fs.StringVar(&c.dataDir, "data", "", "MedBeads data directory (contains pods/, index.db)")
	fs.StringVar(&c.target, "target", "", "Bead ID this correction acts on (sha256: prefix optional)")
	fs.StringVar(&c.author, "author", "", "who is making this correction (required — a correction must be attributable)")
	fs.StringVar(&c.timestamp, "timestamp", "", "clinical event time, RFC3339 (default: now)")
}

// validate checks the shared flags and normalizes the target ID.
func (c *correctCommon) validate(stderr *os.File) (targetID, timestamp string, ok bool) {
	if c.dataDir == "" {
		fmt.Fprintln(stderr, "medbeadsd correct: -data <dir> is required")
		return "", "", false
	}
	if c.target == "" {
		fmt.Fprintln(stderr, "medbeadsd correct: -target <bead-id> is required")
		return "", "", false
	}
	// An anonymous correction is not auditable. The MCP write path enforces the
	// same rule (requiresAuthor in mcpserver/tools_write.go), so neither entry
	// point can produce one.
	if c.author == "" {
		fmt.Fprintln(stderr, "medbeadsd correct: -author <who> is required: "+
			"an amendment, retraction or attestation that cannot name who made it is not auditable")
		return "", "", false
	}

	id, err := bead.ParseID(c.target)
	if err != nil {
		fmt.Fprintf(stderr, "medbeadsd correct: -target %q: %v\n", c.target, err)
		return "", "", false
	}

	ts := c.timestamp
	if ts == "" {
		ts = time.Now().UTC().Format(time.RFC3339)
	}
	return id, ts, true
}

// openAndCheckTarget opens the store and confirms the target Bead exists.
//
// Ingest would reject a dangling reference anyway, but failing here gives the
// operator a precise error ("no such Bead") rather than a validation failure
// buried in an append path — and it does so before anything is written.
func openAndCheckTarget(dataDir, targetID string, stderr *os.File) (*engine.Engine, bead.Bead, bool) {
	eng, err := engine.Open(dataDir)
	if err != nil {
		fmt.Fprintf(stderr, "medbeadsd correct: open engine: %v\n", err)
		return nil, bead.Bead{}, false
	}

	target, err := eng.GetBead(targetID)
	if err != nil {
		_ = eng.Close()
		fmt.Fprintf(stderr, "medbeadsd correct: target %s: %v\n", targetID, err)
		return nil, bead.Bead{}, false
	}
	return eng, target, true
}

// reportWritten prints the new Bead and the reprojection the operator must run.
// bead_status is a PROJECTION: writing the correction Bead does not by itself
// change what the record resolves to.
func reportWritten(stdout *os.File, action string, saved bead.Bead, dataDir string) {
	fmt.Fprintf(stdout, "medbeadsd correct %s: wrote Bead %s (author=%s)\n", action, saved.ID, saved.Author)
	fmt.Fprintf(stdout, "  the fact is durable. To re-derive record status, run:\n")
	fmt.Fprintf(stdout, "    medbeadsd reproject -data %s -record-state\n", dataDir)
}

// runCorrectAmend writes a Bead superseding -target with corrected content.
//
// The amendment does NOT become the patient's current version on its own: it
// lands `unattested` and stays there until an attestation approves it
// (projector/resolve.go's gate). That is the point — a correction to a clinical
// record is a proposal until a clinician signs it.
func runCorrectAmend(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("correct amend", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var c correctCommon
	c.bind(fs)
	contentFile := fs.String("content", "", "path to a JSON file holding the corrected content (required)")
	beadType := fs.String("type", "", "type for the amending Bead (default: the target's own type)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	targetID, timestamp, ok := c.validate(stderr)
	if !ok {
		return 2
	}
	if *contentFile == "" {
		fmt.Fprintln(stderr, "medbeadsd correct amend: -content <json-file> is required (the corrected content)")
		return 2
	}

	raw, err := os.ReadFile(*contentFile)
	if err != nil {
		fmt.Fprintf(stderr, "medbeadsd correct amend: read %s: %v\n", *contentFile, err)
		return 1
	}
	var content map[string]any
	if err := json.Unmarshal(raw, &content); err != nil {
		fmt.Fprintf(stderr, "medbeadsd correct amend: parse %s: %v\n", *contentFile, err)
		return 1
	}

	eng, target, ok := openAndCheckTarget(c.dataDir, targetID, stderr)
	if !ok {
		return 1
	}
	defer eng.Close() //nolint:errcheck // the process is exiting either way

	typ := *beadType
	if typ == "" {
		typ = target.Type
	}

	// Parents carries the target as well as Amends: the amendment is a child of
	// what it corrects, so the causal DAG stays connected AND Ingest can resolve
	// the patient_root from it (a correction may not cross patients).
	saved, err := eng.Ingest(bead.Bead{
		Type:      typ,
		Timestamp: timestamp,
		Author:    c.author,
		Parents:   []string{targetID},
		Amends:    []string{targetID},
		Content:   content,
	})
	if err != nil {
		fmt.Fprintf(stderr, "medbeadsd correct amend: %v\n", err)
		return 1
	}

	reportWritten(stdout, "amend", saved, c.dataDir)
	fmt.Fprintf(stdout, "  this amendment is UNATTESTED: it does not supersede %s until approved:\n", targetID)
	fmt.Fprintf(stdout, "    medbeadsd correct attest -data %s -target %s -author <clinician> -verdict approved\n",
		c.dataDir, saved.ID)
	return 0
}

// runCorrectRetract withdraws -target as entered-in-error.
//
// Unlike an amendment, a retraction takes effect IMMEDIATELY and needs no
// attestation: withdrawing a record that should never have been there is a safety
// action, and making it wait for a second signature would leave a known-wrong
// value standing in the chart meanwhile. projector/resolve.go applies retraction
// first and transitively invalidates amendments of a retracted Bead.
func runCorrectRetract(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("correct retract", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var c correctCommon
	c.bind(fs)
	reason := fs.String("reason", "", "why this record is being withdrawn (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	targetID, timestamp, ok := c.validate(stderr)
	if !ok {
		return 2
	}
	if *reason == "" {
		fmt.Fprintln(stderr, "medbeadsd correct retract: -reason <text> is required: "+
			"a withdrawal with no stated reason is not auditable")
		return 2
	}

	eng, _, ok := openAndCheckTarget(c.dataDir, targetID, stderr)
	if !ok {
		return 1
	}
	defer eng.Close() //nolint:errcheck

	saved, err := eng.Ingest(bead.Bead{
		Type:      "retraction",
		Timestamp: timestamp,
		Author:    c.author,
		Parents:   []string{targetID},
		Retracts:  []string{targetID},
		Content:   map[string]any{"reason": *reason},
	})
	if err != nil {
		fmt.Fprintf(stderr, "medbeadsd correct retract: %v\n", err)
		return 1
	}

	reportWritten(stdout, "retract", saved, c.dataDir)
	fmt.Fprintf(stdout, "  %s is retracted with immediate effect (no attestation required).\n", targetID)
	return 0
}

// runCorrectAttest approves or rejects an amendment.
//
// -target is the AMENDMENT's Bead ID, not the record it corrects: an attestation
// names what it signs off on. Only an approved amendment becomes the patient's
// current version; a rejected one stays visible in the DAG and supersedes nothing.
func runCorrectAttest(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("correct attest", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var c correctCommon
	c.bind(fs)
	verdict := fs.String("verdict", "", "approved | rejected (required)")
	reason := fs.String("reason", "", "optional note recorded with the verdict")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	targetID, timestamp, ok := c.validate(stderr)
	if !ok {
		return 2
	}
	if *verdict != "approved" && *verdict != "rejected" {
		fmt.Fprintf(stderr, "medbeadsd correct attest: -verdict must be \"approved\" or \"rejected\", got %q\n", *verdict)
		return 2
	}

	eng, _, ok := openAndCheckTarget(c.dataDir, targetID, stderr)
	if !ok {
		return 1
	}
	defer eng.Close() //nolint:errcheck

	content := map[string]any{"verdict": *verdict}
	if *reason != "" {
		content["reason"] = *reason
	}

	// An attestation names its subject in Parents — that is how
	// projector/resolve.go finds which Bead a verdict applies to.
	saved, err := eng.Ingest(bead.Bead{
		Type:      "attestation",
		Timestamp: timestamp,
		Author:    c.author,
		Parents:   []string{targetID},
		Content:   content,
	})
	if err != nil {
		fmt.Fprintf(stderr, "medbeadsd correct attest: %v\n", err)
		return 1
	}

	reportWritten(stdout, "attest", saved, c.dataDir)
	if *verdict == "approved" {
		fmt.Fprintf(stdout, "  %s is APPROVED: after reprojection it becomes the current version of what it amends.\n", targetID)
	} else {
		fmt.Fprintf(stdout, "  %s is REJECTED: it remains in the record but supersedes nothing.\n", targetID)
	}
	return 0
}
