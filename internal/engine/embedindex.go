package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/medbeads/medbeads/internal/engine/index"
)

// Embedder is the subset of internal/engine/embedder.Client's API
// StartEmbedIndexer needs: turn a batch of search_text strings into one
// embedding vector per string, in the same order. Package engine depends
// only on this interface, not on package embedder itself (mirroring package
// apc's ingester interface, internal/engine/apc/scanner.go) — the lead's
// "embedder を index 層に持ち込まない" decision extends to package engine too:
// nothing here constructs an embedder.Client or knows its HTTP transport;
// cmd/medbeadsd wires a real one in and passes it here as this interface.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// EmbedIndexerOptions configures StartEmbedIndexer's batch-drain loop.
type EmbedIndexerOptions struct {
	// BatchSize bounds how many bead_embed_queue rows one drain iteration
	// embeds and commits together (index.DequeueEmbedBatch's limit, and the
	// unit this loop's single transaction per iteration is scoped to — see
	// drainOnce). The lead's task spec gives "例 64件" as the illustrative
	// value; DefaultBatchSize uses that.
	BatchSize int

	// PollInterval is how long the loop sleeps after an iteration that found
	// an empty queue (nothing to embed right now) before checking again.
	// DefaultPollInterval is used if zero.
	PollInterval time.Duration

	// RetryBackoff is the initial backoff after an Embed call fails (e.g.
	// the embedder is down or returns an error); it doubles on each
	// consecutive failure up to RetryBackoffMax, per the lead's "embedder
	// エラー時はリトライ+バックオフ、キューに残す" decision — a failed batch is
	// never dropped from the queue (see drainOnce), only retried later.
	// DefaultRetryBackoff is used if zero.
	RetryBackoff time.Duration

	// RetryBackoffMax caps the exponential backoff above.
	// DefaultRetryBackoffMax is used if zero.
	RetryBackoffMax time.Duration
}

const (
	// DefaultBatchSize is StartEmbedIndexer's default EmbedIndexerOptions.BatchSize.
	DefaultBatchSize = 64
	// DefaultPollInterval is StartEmbedIndexer's default EmbedIndexerOptions.PollInterval.
	DefaultPollInterval = 2 * time.Second
	// DefaultRetryBackoff is StartEmbedIndexer's default EmbedIndexerOptions.RetryBackoff.
	DefaultRetryBackoff = 1 * time.Second
	// DefaultRetryBackoffMax is StartEmbedIndexer's default EmbedIndexerOptions.RetryBackoffMax.
	DefaultRetryBackoffMax = 30 * time.Second
)

func (o EmbedIndexerOptions) withDefaults() EmbedIndexerOptions {
	if o.BatchSize <= 0 {
		o.BatchSize = DefaultBatchSize
	}
	if o.PollInterval <= 0 {
		o.PollInterval = DefaultPollInterval
	}
	if o.RetryBackoff <= 0 {
		o.RetryBackoff = DefaultRetryBackoff
	}
	if o.RetryBackoffMax <= 0 {
		o.RetryBackoffMax = DefaultRetryBackoffMax
	}
	return o
}

// StartEmbedIndexer launches the L2 semantic search async indexer (R4.2,
// specs/DESIGN_v3.md §6: "埋め込み生成は書き込みパスから分離した非同期インデ
// クサ... 埋め込みサーバー停止時も ingest は止まらない") as exactly one
// goroutine that repeatedly drains e's bead_embed_queue in batches, calls
// embedder.Embed on each batch's search_text values, and writes the
// resulting vectors into bead_embed (index.UpsertEmbedAndDequeue), until ctx
// is cancelled.
//
// # Why this is opt-in, not automatic
//
// Nothing in Engine.Open or Ingest ever calls this: per the lead's decision
// ("既存の「常駐 goroutine なし」衛生を維持" — every test, every CLI subcommand
// other than `serve -embedder ...`, and every caller that just wants
// Ingest/GetBead's synchronous API gets exactly zero background goroutines
// from opening an Engine, matching this project's existing hygiene where
// package apc's Scan is also caller-driven, not a resident goroutine (see
// apc/scanner.go's own doc comment on the same point for R5). Only
// cmd/medbeadsd's `serve` subcommand, and only when its own -embedder flag
// is set, calls this at all.
//
// # Lifecycle
//
// StartEmbedIndexer returns immediately; its goroutine runs until ctx is
// Done, at which point it finishes any batch already in flight (embedder
// HTTP calls themselves are ctx-bound as well, so a cancelled ctx also
// unblocks an in-progress Embed call promptly rather than only being
// checked between batches) and then closes the returned <-chan struct{},
// which the caller (or a test) can range over / receive from to know the
// goroutine has fully exited — matching cmd/medbeadsd/serve.go's own
// "channel signals goroutine completion" idiom (runServeHTTP's serveErr).
// There is no separate Stop method: cancelling ctx is the only shutdown
// path, so a caller always has exactly one thing to do to stop this
// goroutine (cancel the context it was given), never two.
//
// # Failure handling
//
// A batch that fails to embed (the embedder is down, times out, or returns
// a malformed response) is never partially applied and never dropped from
// the queue: drainOnce's transaction only commits once every item in a
// batch has both an embedding and its bead_embed insert prepared, so a
// failure part-way through a batch leaves every one of that batch's queue
// rows untouched (no partial writes to clean up) for the next retry — with
// an exponential backoff between attempts (EmbedIndexerOptions.RetryBackoff
// up to RetryBackoffMax) so a persistently-down embedder does not spin this
// goroutine in a tight failing loop. Ingest itself is never blocked or
// slowed by any of this (see IndexBead's EnqueueEmbed call, which only ever
// writes a lightweight queue row and never calls the embedder).
func (e *Engine) StartEmbedIndexer(ctx context.Context, embedder Embedder, opts EmbedIndexerOptions) <-chan struct{} {
	opts = opts.withDefaults()
	done := make(chan struct{})

	go func() {
		defer close(done)
		backoff := opts.RetryBackoff

		for {
			if ctx.Err() != nil {
				return
			}

			drained, err := e.drainEmbedBatchOnce(ctx, embedder, opts.BatchSize)
			if err != nil {
				// Backoff, but remain responsive to ctx cancellation while
				// waiting rather than sleeping the full backoff duration
				// unconditionally.
				if !sleepOrDone(ctx, backoff) {
					return
				}
				backoff *= 2
				if backoff > opts.RetryBackoffMax {
					backoff = opts.RetryBackoffMax
				}
				continue
			}
			backoff = opts.RetryBackoff // reset after any successful drain iteration

			if drained == 0 {
				if !sleepOrDone(ctx, opts.PollInterval) {
					return
				}
			}
		}
	}()

	return done
}

// DrainEmbedQueue synchronously drains e's entire bead_embed_queue in
// batches of batchSize (DefaultBatchSize if <= 0), calling embedder.Embed
// once per batch, and returns the total number of Beads embedded. Unlike
// StartEmbedIndexer, this makes no attempt to retry or back off on a
// failure: a batch that errors stops the drain immediately and returns that
// error (with the count of Beads successfully drained *before* the failing
// batch), leaving every not-yet-drained row — including the failed batch's
// own rows, which drainEmbedBatchOnce's all-or-nothing transaction never
// partially applies — still queued for a subsequent call to pick up.
//
// This is `medbeadsd embed`'s synchronous backfill primitive (see
// cmd/medbeadsd/embed.go): a one-shot CLI invocation over a batch job (the
// task's "queue を同期でドレインして終了する CLI") has no long-running process
// to retry in the background the way StartEmbedIndexer's goroutine does, so
// surfacing the first failure immediately (rather than looping with
// backoff, which would make a one-shot CLI invocation hang indefinitely
// against a persistently-down embedder) is the correct behavior for this
// caller.
func (e *Engine) DrainEmbedQueue(ctx context.Context, embedder Embedder, batchSize int) (int, error) {
	if batchSize <= 0 {
		batchSize = DefaultBatchSize
	}

	total := 0
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		drained, err := e.drainEmbedBatchOnce(ctx, embedder, batchSize)
		if err != nil {
			return total, err
		}
		total += drained
		if drained == 0 {
			return total, nil
		}
	}
}

// sleepOrDone waits for d or ctx.Done(), whichever comes first, reporting
// whether the wait completed normally (true) or was cut short by ctx
// cancellation (false) — the shared "responsive sleep" building block
// StartEmbedIndexer's loop uses for both its poll-interval and its
// retry-backoff waits, so cancelling ctx during either kind of wait stops
// the goroutine within one scheduling tick rather than up to a full
// interval late.
func sleepOrDone(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// drainEmbedBatchOnce runs one batch iteration: dequeue up to batchSize
// pending items, embed their search_text in one Embedder.Embed call, and
// write every result to bead_embed + remove it from bead_embed_queue in a
// single transaction. It returns the number of items drained (0 meaning the
// queue was empty, a normal/expected outcome — see StartEmbedIndexer's poll-
// interval sleep) and a non-nil error only for a failure that should trigger
// a backoff-and-retry (an empty queue is not an error).
func (e *Engine) drainEmbedBatchOnce(ctx context.Context, embedder Embedder, batchSize int) (int, error) {
	items, err := e.idx.DequeueEmbedBatch(batchSize)
	if err != nil {
		return 0, fmt.Errorf("engine: embed indexer: dequeue batch: %w", err)
	}
	if len(items) == 0 {
		return 0, nil
	}

	texts := make([]string, len(items))
	for i, item := range items {
		texts[i] = item.SearchText
	}

	vectors, err := embedder.Embed(ctx, texts)
	if err != nil {
		return 0, fmt.Errorf("engine: embed indexer: embed batch of %d: %w", len(items), err)
	}
	if len(vectors) != len(items) {
		return 0, fmt.Errorf("engine: embed indexer: embedder returned %d vector(s) for %d item(s)", len(vectors), len(items))
	}

	tx, err := e.idx.SQLDB().Begin()
	if err != nil {
		return 0, fmt.Errorf("engine: embed indexer: begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after commit is a no-op

	for i, item := range items {
		blob, err := index.SerializeEmbedding(vectors[i])
		if err != nil {
			return 0, fmt.Errorf("engine: embed indexer: serialize embedding for %s: %w", item.BeadID, err)
		}
		if err := index.UpsertEmbedAndDequeue(tx, item.BeadID, item.PatientRoot, blob); err != nil {
			return 0, fmt.Errorf("engine: embed indexer: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("engine: embed indexer: commit batch of %d: %w", len(items), err)
	}
	return len(items), nil
}
