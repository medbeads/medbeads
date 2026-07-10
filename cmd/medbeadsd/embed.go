// runEmbed implements `medbeadsd embed`: a one-shot L2 semantic embedding
// backfill CLI (docs/requirements.md R4.2's "埋め込みバックフィル用に
// medbeadsd embed -data <dir> -embedder <url> [-batch N]（queue を同期で
// ドレインして終了する CLI）だけ用意する" — deliberately separate from
// `serve -embedder ...`'s own long-running, backoff-retrying async indexer:
// this subcommand drains synchronously and exits, for an operator-run batch
// job (e.g. backfilling the real store's ~96万 Bead queue once an embedding
// server is available — explicitly out of this unit's own scope to run, per
// the task's "実ストア 96万 Bead の埋め込み生成はこのタスクのスコープ外").
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/medbeads/medbeads/internal/engine"
	"github.com/medbeads/medbeads/internal/engine/embedder"
)

// runEmbed parses `embed -data <dir> -embedder <url> [-embed-model <name>]
// [-batch <n>]`, opens the Engine, and calls Engine.DrainEmbedQueue once
// (synchronously, no retry/backoff — see DrainEmbedQueue's own doc comment
// on why a one-shot CLI differs from serve's resident indexer). Exit codes
// follow this package's existing convention (verify/reindex/apc): 0 (drain
// completed, queue now empty), 1 (engine/drain error), 2 (usage error).
func runEmbed(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("embed", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data", "", "MedBeads data directory (contains pods/, dict/, index.db)")
	embedderURL := fs.String("embedder", "", "base URL of an OpenAI-compatible /v1/embeddings server (e.g. http://localhost:8080); required")
	embedModel := fs.String("embed-model", embedder.DefaultModel, "model name sent to the -embedder server "+
		"(default \"ruri-v3\", cl-nagoya/ruri-v3-310m, 768 dims — must match index.db's migrations/0004_embed.sql vec0 column width)")
	batchSize := fs.Int("batch", engine.DefaultBatchSize, "Beads embedded per batch/transaction (default 64)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dataDir == "" {
		fmt.Fprintln(stderr, "medbeadsd embed: -data <dir> is required")
		return 2
	}
	if *embedderURL == "" {
		fmt.Fprintln(stderr, "medbeadsd embed: -embedder <url> is required")
		return 2
	}

	eng, err := engine.Open(*dataDir)
	if err != nil {
		fmt.Fprintf(stderr, "medbeadsd embed: open engine: %v\n", err)
		return 1
	}
	defer eng.Close() //nolint:errcheck // best-effort unwind; process is exiting either way

	before, err := eng.Index().EmbedQueueDepth()
	if err != nil {
		fmt.Fprintf(stderr, "medbeadsd embed: %v\n", err)
		return 1
	}
	if before == 0 {
		fmt.Fprintln(stdout, "medbeadsd embed: bead_embed_queue is already empty, nothing to do")
		return 0
	}

	client := embedder.New(*embedderURL, *embedModel, nil)
	fmt.Fprintf(stderr, "medbeadsd embed: draining %d queued Bead(s) via %s (model=%s, batch=%d)\n",
		before, *embedderURL, *embedModel, *batchSize)

	drained, err := eng.DrainEmbedQueue(context.Background(), client, *batchSize)
	if err != nil {
		fmt.Fprintf(stderr, "medbeadsd embed: drain failed after %d Bead(s): %v\n", drained, err)
		return 1
	}

	fmt.Fprintf(stdout, "medbeadsd embed: embedded %d Bead(s), bead_embed_queue is now empty\n", drained)
	return 0
}
