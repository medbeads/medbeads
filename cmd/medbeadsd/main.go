// Command medbeadsd is the MedBeads v3 single-binary daemon.
//
// It is the sole long-running process (see specs/DESIGN_v3.md §2): it hosts
// the engine, the MCP server, and the REST projection for the UI. `serve`
// hosts the engine + MCP server (internal/mcpserver, docs/requirements.md
// R6) over stdio (default) or Streamable HTTP (-http addr); the REST
// projection for the UI is a later unit's addition to this subcommand.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/medbeads/medbeads/internal/engine/index"
	"github.com/medbeads/medbeads/internal/engine/pod"
)

const usage = `medbeadsd - MedBeads v3 single-binary daemon

Usage:
  medbeadsd <command>

Commands:
  serve     Start the daemon (engine + MCP; flags: -data <dir> [-role <role>] [-http <addr>]
            [-embedder <url>] [-embed-model <name>])
  verify    Verify Pod/index integrity (flags: -data <dir>)
  reindex   Rebuild index.db from Pod files (source of truth)
  apc       Run the APC batch scanner (flags: -data <dir> [-patient <root>])
  embed     Backfill L2 semantic embeddings by draining bead_embed_queue synchronously
            (flags: -data <dir> -embedder <url> [-embed-model <name>] [-batch <n>])
  reproject Rebuild clinical_links from bead_tags + the cooccurrence link_rule
            Bead and flip projection_manifest's active run (flags: -data <dir>
            [-code-version <v>] [-record-state]; does not re-scan Pods, see
            reindex for that). -record-state additionally runs U4b's
            record_state projector (bead_status/active_conditions/
            active_medications, its own separate manifest lineage)
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run dispatches the requested subcommand and returns the process exit code.
// It is factored out of main so it can be exercised by tests without calling
// os.Exit.
func run(args []string, stdout, stderr *os.File) int {
	if len(args) == 0 {
		fmt.Fprint(stdout, usage)
		return 0
	}

	switch cmd := args[0]; cmd {
	case "verify":
		return runVerify(args[1:], stdout, stderr)
	case "reindex":
		return runReindex(args[1:], stdout, stderr)
	case "apc":
		return runApc(args[1:], stdout, stderr)
	case "embed":
		return runEmbed(args[1:], stdout, stderr)
	case "reproject":
		return runReproject(args[1:], stdout, stderr)
	case "serve":
		return runServe(args[1:], stdout, stderr)
	case "-h", "-help", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "medbeadsd: unknown command %q\n\n%s", cmd, usage)
		return 1
	}
}

// runVerify implements `medbeadsd verify`: it walks every Pod file under
// -data's pods/ directory (see pod.VerifyAll, specs/DESIGN_v3.md §3, R1.4)
// and reports per-frame CRC + self-verification results. It returns exit
// code 0 only if every Pod's every frame verified cleanly.
func runVerify(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data", "", "MedBeads data directory (contains pods/, dict/, index.db)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dataDir == "" {
		fmt.Fprintln(stderr, "medbeadsd verify: -data <dir> is required")
		return 2
	}

	store := pod.NewStore(*dataDir)
	report, err := pod.VerifyAll(store)
	if err != nil {
		fmt.Fprintf(stderr, "medbeadsd verify: %v\n", err)
		return 1
	}

	if len(report.Pods) == 0 {
		fmt.Fprintf(stdout, "medbeadsd verify: no Pod files found under %s\n", store.PodsDir())
		return 0
	}

	for _, p := range report.Pods {
		status := "OK"
		if !p.OK() {
			status = "FAIL"
		}
		fmt.Fprintf(stdout, "%s: %s (%d frames)\n", p.Path, status, len(p.Frames))
		for _, f := range p.FailedFrames() {
			fmt.Fprintf(stdout, "  frame at offset %d (bead_id=%s): %v\n", f.Offset, f.BeadID, f.Err)
		}
		if p.Truncated {
			fmt.Fprintf(stdout, "  truncated: valid data ends at offset %d: %v\n", p.TruncatedAt, p.TruncationErr)
		}
	}

	fmt.Fprintf(stdout, "verified %d pod(s), %d frame(s) total\n", len(report.Pods), report.TotalFrames())
	if !report.OK() {
		fmt.Fprintf(stdout, "result: FAIL (%d pod(s) with errors)\n", len(report.FailedPods()))
		return 1
	}
	fmt.Fprintln(stdout, "result: OK")
	return 0
}

// runReindex implements `medbeadsd reindex`: it rebuilds -data's index.db
// from scratch by scanning every Pod file under -data/pods/ (see
// index.Reindex, specs/DESIGN_v3.md §3/§5, R1.4/R3). index.db always lives
// at <data>/index.db, matching the data directory layout documented in
// internal/engine/pod's Store (pods/, dict/, index.db siblings).
func runReindex(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("reindex", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data", "", "MedBeads data directory (contains pods/, dict/, index.db)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dataDir == "" {
		fmt.Fprintln(stderr, "medbeadsd reindex: -data <dir> is required")
		return 2
	}

	dbPath := filepath.Join(*dataDir, "index.db")
	db, err := index.Reindex(*dataDir, dbPath, index.DefaultFlattener{})
	if err != nil {
		fmt.Fprintf(stderr, "medbeadsd reindex: %v\n", err)
		return 1
	}
	defer db.Close()

	version, err := index.SchemaVersion(db.SQLDB())
	if err != nil {
		fmt.Fprintf(stderr, "medbeadsd reindex: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "medbeadsd reindex: rebuilt %s (schema version %d)\n", dbPath, version)
	return 0
}
