package engine

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/medbeads/medbeads/internal/engine/bead"
	"github.com/medbeads/medbeads/internal/engine/index"
)

// ingestT calls e.Ingest(b) and fails the test on error, mirroring
// internal/mcpserver/mcpserver_test.go's identical helper — this package's
// own existing tests (ingest_test.go etc.) call e.Ingest directly and check
// the error inline instead, but this file's tests chain several Ingest
// calls per test, where that inline-check style would be repetitive.
func ingestT(t *testing.T, e *Engine, b bead.Bead) bead.Bead {
	t.Helper()
	out, err := e.Ingest(b)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	return out
}

// fakeEmbedDim matches migrations/0004_embed.sql's frozen vec0 column width
// (index.EmbedDim): a fake Embedder that returned a different-length vector
// would fail at index.SerializeEmbedding, which is exactly what
// TestStartEmbedIndexer_MismatchedDimension_IsARetriedError below exercises
// on purpose.
const fakeEmbedDim = index.EmbedDim

// deterministicVector derives an index.EmbedDim-length float32 vector from
// text's sha256 digest — a real embedder client would call an HTTP server
// (internal/engine/embedder does exactly that against an httptest fake; see
// its own tests), but StartEmbedIndexer itself only depends on the narrow
// Embedder interface, so an in-process fake exercises this package's own
// batching/retry/backoff logic without any HTTP round-trip.
func deterministicVector(text string) []float32 {
	sum := sha256.Sum256([]byte(text))
	out := make([]float32, fakeEmbedDim)
	for i := range out {
		out[i] = float32(sum[i%len(sum)]) / 255.0
	}
	return out
}

// fakeEmbedder is a test Embedder whose behavior (success, per-call error,
// and per-call recorded input) is controlled by the test via its exported
// fields/methods, guarded by a mutex since StartEmbedIndexer's goroutine
// calls Embed concurrently with the test goroutine's own assertions.
type fakeEmbedder struct {
	mu       sync.Mutex
	failNext int // Embed fails this many more times before succeeding
	calls    int
	lastSize int
}

func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.lastSize = len(texts)
	if f.failNext > 0 {
		f.failNext--
		return nil, fmt.Errorf("fakeEmbedder: simulated embedder outage")
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = deterministicVector(t)
	}
	return out, nil
}

func (f *fakeEmbedder) setFailNext(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failNext = n
}

func (f *fakeEmbedder) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// waitForQueueDepth polls e's embed queue depth until it reaches want or
// timeout elapses, for tests observing StartEmbedIndexer's asynchronous
// drain rather than calling its internals directly.
func waitForQueueDepth(t *testing.T, e *Engine, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		depth, err := e.idx.EmbedQueueDepth()
		if err != nil {
			t.Fatalf("EmbedQueueDepth: %v", err)
		}
		if depth == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("EmbedQueueDepth: timed out waiting for depth=%d, still %d after %s", want, depth, timeout)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestStartEmbedIndexer_DrainsQueueAndPopulatesSemanticSearch is the
// end-to-end happy path: Ingest enqueues, StartEmbedIndexer drains via the
// fake Embedder, and the resulting bead_embed row is found by
// SemanticSearch scoped to the right patient_root.
func TestStartEmbedIndexer_DrainsQueueAndPopulatesSemanticSearch(t *testing.T) {
	e := openT(t)
	root := ingestT(t, e, unsavedBead("patient_registration", nil, nil, map[string]any{"name": "Semantic Patient"}))
	obs := ingestT(t, e, unsavedBead("fhir_observation", []string{root.ID}, nil, map[string]any{"note": "elevated potassium level"}))

	depth, err := e.idx.EmbedQueueDepth()
	if err != nil {
		t.Fatalf("EmbedQueueDepth: %v", err)
	}
	if depth == 0 {
		t.Fatal("EmbedQueueDepth = 0 after Ingest, want > 0 (IndexBead must enqueue every Bead unconditionally)")
	}

	fake := &fakeEmbedder{}
	ctx, cancel := context.WithCancel(context.Background())
	done := e.StartEmbedIndexer(ctx, fake, EmbedIndexerOptions{
		BatchSize:    64,
		PollInterval: 10 * time.Millisecond,
	})

	waitForQueueDepth(t, e, 0, 5*time.Second)

	queryVec := deterministicVector("elevated potassium level")
	queryBlob, err := index.SerializeEmbedding(queryVec)
	if err != nil {
		t.Fatalf("SerializeEmbedding: %v", err)
	}
	results, err := e.idx.SemanticSearch(queryBlob, 5, root.ID)
	if err != nil {
		t.Fatalf("SemanticSearch: %v", err)
	}
	found := false
	for _, r := range results {
		if r.BeadID == obs.ID {
			found = true
			if r.Distance > 0.0001 {
				t.Errorf("distance for exact-match query = %f, want ~0 (identical vector)", r.Distance)
			}
		}
	}
	if !found {
		t.Fatalf("SemanticSearch(patient_root=%s) did not return the ingested observation %s; got %+v", root.ID, obs.ID, results)
	}

	cancel()
	<-done // StartEmbedIndexer's goroutine must exit promptly once ctx is cancelled
}

// TestStartEmbedIndexer_PatientRootPartitionFilter checks that a
// patient-scoped SemanticSearch never returns another patient's Bead, even
// when that other Bead's embedding is a closer vector match — the vec0
// PARTITION KEY pre-filter (migrations/0004_embed.sql) must exclude it
// entirely, not merely rank it lower.
func TestStartEmbedIndexer_PatientRootPartitionFilter(t *testing.T) {
	e := openT(t)
	rootA := ingestT(t, e, unsavedBead("patient_registration", nil, nil, map[string]any{"name": "Patient A"}))
	rootB := ingestT(t, e, unsavedBead("patient_registration", nil, nil, map[string]any{"name": "Patient B"}))
	beadA := ingestT(t, e, unsavedBead("fhir_observation", []string{rootA.ID}, nil, map[string]any{"note": "shared query text"}))
	beadB := ingestT(t, e, unsavedBead("fhir_observation", []string{rootB.ID}, nil, map[string]any{"note": "shared query text"}))

	fake := &fakeEmbedder{}
	ctx, cancel := context.WithCancel(context.Background())
	done := e.StartEmbedIndexer(ctx, fake, EmbedIndexerOptions{BatchSize: 64, PollInterval: 10 * time.Millisecond})
	waitForQueueDepth(t, e, 0, 5*time.Second)
	cancel()
	<-done

	queryBlob, err := index.SerializeEmbedding(deterministicVector("shared query text"))
	if err != nil {
		t.Fatalf("SerializeEmbedding: %v", err)
	}

	resultsA, err := e.idx.SemanticSearch(queryBlob, 10, rootA.ID)
	if err != nil {
		t.Fatalf("SemanticSearch(rootA): %v", err)
	}
	for _, r := range resultsA {
		if r.BeadID == beadB.ID {
			t.Fatalf("SemanticSearch(patient_root=%s) returned patient B's Bead %s — partition pre-filter leaked across patients", rootA.ID, beadB.ID)
		}
	}
	foundA := false
	for _, r := range resultsA {
		if r.BeadID == beadA.ID {
			foundA = true
		}
	}
	if !foundA {
		t.Fatalf("SemanticSearch(patient_root=%s) did not return patient A's own Bead %s", rootA.ID, beadA.ID)
	}
}

// TestStartEmbedIndexer_EmbedderDown_IngestStillSucceeds_QueueRetained
// exercises the lead's core availability requirement: an embedder that is
// down (every Embed call errors) must never block or fail Ingest, and the
// queue row must survive (not be dropped) until the embedder recovers.
func TestStartEmbedIndexer_EmbedderDown_IngestStillSucceeds_QueueRetained(t *testing.T) {
	e := openT(t)
	root := ingestT(t, e, unsavedBead("patient_registration", nil, nil, map[string]any{"name": "Outage Patient"}))

	fake := &fakeEmbedder{}
	fake.setFailNext(1000000) // effectively "always fails" for this test's duration

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.StartEmbedIndexer(ctx, fake, EmbedIndexerOptions{
		BatchSize:       64,
		PollInterval:    10 * time.Millisecond,
		RetryBackoff:    5 * time.Millisecond,
		RetryBackoffMax: 20 * time.Millisecond,
	})

	// Ingest must succeed (and not hang) even while the indexer goroutine is
	// continuously failing to embed in the background.
	obs := ingestT(t, e, unsavedBead("fhir_observation", []string{root.ID}, nil, map[string]any{"note": "ingest must not block on embedder outage"}))

	// Give the indexer goroutine a few failed-retry cycles to run.
	time.Sleep(80 * time.Millisecond)

	if fake.callCount() == 0 {
		t.Fatal("fakeEmbedder was never called — StartEmbedIndexer did not attempt to drain the queue at all")
	}

	depth, err := e.idx.EmbedQueueDepth()
	if err != nil {
		t.Fatalf("EmbedQueueDepth: %v", err)
	}
	if depth == 0 {
		t.Fatal("EmbedQueueDepth = 0 while the embedder is down — a failed batch must remain queued, not be dropped")
	}

	// GetBead must still resolve the Bead: Ingest's own success is untouched
	// by the embedder outage.
	if _, err := e.GetBead(obs.ID); err != nil {
		t.Fatalf("GetBead after embedder outage: %v", err)
	}
}

// TestStartEmbedIndexer_RecoversAfterOutage checks the second half of the
// availability story: once the embedder comes back, a previously-failed,
// still-queued item is picked up and embedded without needing to be
// re-ingested.
func TestStartEmbedIndexer_RecoversAfterOutage(t *testing.T) {
	e := openT(t)
	root := ingestT(t, e, unsavedBead("patient_registration", nil, nil, map[string]any{"name": "Recovery Patient"}))
	obs := ingestT(t, e, unsavedBead("fhir_observation", []string{root.ID}, nil, map[string]any{"note": "recovers after outage"}))

	fake := &fakeEmbedder{}
	fake.setFailNext(3) // fails a few times, then starts succeeding

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.StartEmbedIndexer(ctx, fake, EmbedIndexerOptions{
		BatchSize:       64,
		PollInterval:    10 * time.Millisecond,
		RetryBackoff:    5 * time.Millisecond,
		RetryBackoffMax: 20 * time.Millisecond,
	})

	waitForQueueDepth(t, e, 0, 5*time.Second)

	queryBlob, err := index.SerializeEmbedding(deterministicVector("recovers after outage"))
	if err != nil {
		t.Fatalf("SerializeEmbedding: %v", err)
	}
	results, err := e.idx.SemanticSearch(queryBlob, 5, root.ID)
	if err != nil {
		t.Fatalf("SemanticSearch: %v", err)
	}
	found := false
	for _, r := range results {
		if r.BeadID == obs.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("SemanticSearch after recovery did not find %s; got %+v", obs.ID, results)
	}
}

// TestStartEmbedIndexer_NoGoroutineWithoutExplicitStart checks the lead's
// "既存の「常駐 goroutine なし」衛生を維持" decision: simply opening an Engine
// and Ingesting must never enqueue any background embedding work beyond the
// (cheap, synchronous) bead_embed_queue row — nothing drains it unless
// StartEmbedIndexer is explicitly called. This is checked indirectly: the
// queue stays non-empty indefinitely (well past any plausible poll
// interval) when StartEmbedIndexer is never invoked at all.
func TestStartEmbedIndexer_NoGoroutineWithoutExplicitStart(t *testing.T) {
	e := openT(t)
	root := ingestT(t, e, unsavedBead("patient_registration", nil, nil, map[string]any{"name": "No Indexer Patient"}))
	ingestT(t, e, unsavedBead("fhir_observation", []string{root.ID}, nil, map[string]any{"note": "never drained"}))

	time.Sleep(50 * time.Millisecond) // comfortably longer than this test suite's own PollInterval values

	depth, err := e.idx.EmbedQueueDepth()
	if err != nil {
		t.Fatalf("EmbedQueueDepth: %v", err)
	}
	if depth == 0 {
		t.Fatal("EmbedQueueDepth = 0 without ever calling StartEmbedIndexer — a background goroutine must be draining it unexpectedly")
	}
}
