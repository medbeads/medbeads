// Command medbeadsd is the MedBeads v3 single-binary daemon.
//
// It is the sole long-running process (see specs/DESIGN_v3.md §2): it hosts
// the engine, the MCP server, and the REST projection for the UI. At this
// stage (M1 scaffolding) the subcommands are placeholders; real behavior
// lands as the engine packages are implemented.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/medbeads/medbeads/internal/engine/pod"
)

const usage = `medbeadsd - MedBeads v3 single-binary daemon

Usage:
  medbeadsd <command>

Commands:
  serve     Start the daemon (engine + MCP + REST)
  verify    Verify Pod/index integrity (flags: -data <dir>)
  reindex   Rebuild index.db from Pod files (source of truth)
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
	case "serve", "reindex":
		fmt.Fprintf(stderr, "medbeadsd %s: not implemented (M1)\n", cmd)
		return 1
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
