package mcpserver

import (
	"context"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/medbeads/medbeads/internal/engine"
	"github.com/medbeads/medbeads/internal/engine/index"
)

// This file is test-only scaffolding shared by retrieve_semantic_test.go and
// rag_search_test.go (both L2 semantic search tests, R4.2/R6.3) — split out
// from mcpserver_test.go itself only to keep the two topics apart; it has no
// top-level TestXxx of its own, same as mcpserver_test.go's own shared
// helpers ("each package's tests re-derive these small helpers").

// fakeQueryEmbedder is a deterministic, in-process QueryEmbedder: the same
// input text always embeds to the same index.EmbedDim-length vector (derived
// from its sha256 digest), so tests can assert exact/near-exact
// SemanticSearch matches without any real embedding model or HTTP round
// trip (internal/engine/embedder's own package tests cover the real HTTP
// client against an httptest fake separately).
type fakeQueryEmbedder struct{}

func (fakeQueryEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = deterministicTestVector(t)
	}
	return out, nil
}

func deterministicTestVector(text string) []float32 {
	sum := sha256.Sum256([]byte(text))
	out := make([]float32, index.EmbedDim)
	for i := range out {
		out[i] = float32(sum[i%len(sum)]) / 255.0
	}
	return out
}

// newServerWithEmbedderT builds a Server with Config.Embedder set to a
// fakeQueryEmbedder{} — the "embedder is configured" counterpart to
// mcpserver_test.go's own newServerT (which never sets one).
func newServerWithEmbedderT(t testing.TB, e *engine.Engine, role string) *Server {
	t.Helper()
	s, err := New(Config{Engine: e, Role: role, Embedder: fakeQueryEmbedder{}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// drainEmbedIndexerT runs e's async embed indexer (engine.Engine.
// StartEmbedIndexer) against a fakeQueryEmbedder{} until the queue is
// empty, then stops it — the shared "make every already-Ingested Bead's
// embedding actually exist in bead_embed" setup step every semantic-search
// test in this package needs before SemanticSearch/retrieve(semantic=true)/
// rag_search can find anything.
func drainEmbedIndexerT(t *testing.T, e *engine.Engine) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := e.StartEmbedIndexer(ctx, fakeQueryEmbedder{}, engine.EmbedIndexerOptions{
		BatchSize:    64,
		PollInterval: 5 * time.Millisecond,
	})

	deadline := time.Now().Add(5 * time.Second)
	for {
		depth, err := e.Index().EmbedQueueDepth()
		if err != nil {
			t.Fatalf("EmbedQueueDepth: %v", err)
		}
		if depth == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("drainEmbedIndexerT: timed out waiting for embed queue to drain (depth=%d)", depth)
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
}
