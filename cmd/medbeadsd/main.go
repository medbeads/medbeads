// Command medbeadsd is the MedBeads v3 single-binary daemon.
//
// It is the sole long-running process (see specs/DESIGN_v3.md §2): it hosts
// the engine, the MCP server, and the REST projection for the UI. At this
// stage (M1 scaffolding) the subcommands are placeholders; real behavior
// lands as the engine packages are implemented.
package main

import (
	"fmt"
	"os"
)

const usage = `medbeadsd - MedBeads v3 single-binary daemon

Usage:
  medbeadsd <command>

Commands:
  serve     Start the daemon (engine + MCP + REST)
  verify    Verify Pod/index integrity
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
	case "serve", "verify", "reindex":
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
