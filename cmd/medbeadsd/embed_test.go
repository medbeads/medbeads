package main

import (
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/medbeads/medbeads/internal/engine"
	"github.com/medbeads/medbeads/internal/engine/bead"
	"github.com/medbeads/medbeads/internal/engine/index"
)

// TestRun_EmbedUsageErrors checks embed's two required flags (-data,
// -embedder) are both enforced as usage errors (exit 2), matching every
// other subcommand's -data convention plus embed's own -embedder
// requirement.
func TestRun_EmbedUsageErrors(t *testing.T) {
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer devNull.Close()

	dir := t.TempDir()

	tests := []struct {
		name string
		args []string
	}{
		{name: "no -data", args: []string{"embed", "-embedder", "http://example.invalid"}},
		{name: "no -embedder", args: []string{"embed", "-data", dir}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := run(tt.args, devNull, devNull); got != 2 {
				t.Errorf("run(%v) = %d, want 2", tt.args, got)
			}
		})
	}
}

// fakeEmbeddingServer returns an httptest.Server implementing the OpenAI-
// compatible POST /v1/embeddings contract, with a deterministic sha256-
// derived index.EmbedDim-length vector per input string — this test's own
// small-scale stand-in for a real embedding server (out of scope per the
// task: "埋め込みサーバーの実体はスコープ外").
func fakeEmbeddingServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/embeddings", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		type item struct {
			Embedding []float32 `json:"embedding"`
		}
		resp := struct {
			Data []item `json:"data"`
		}{}
		for _, text := range req.Input {
			sum := sha256.Sum256([]byte(text))
			vec := make([]float32, index.EmbedDim)
			for i := range vec {
				vec[i] = float32(sum[i%len(sum)]) / 255.0
			}
			resp.Data = append(resp.Data, item{Embedding: vec})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestRun_EmbedEndToEnd is the CLI-level e2e: seed a small store (Ingest
// enqueues every Bead per index.IndexBead's EnqueueEmbed call), run
// `medbeadsd embed -data <dir> -embedder <fake server URL>`, and check
// bead_embed_queue is fully drained and bead_embed has one row per
// originally-queued Bead.
func TestRun_EmbedEndToEnd(t *testing.T) {
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer devNull.Close()

	dir := t.TempDir()

	e, err := engine.Open(dir)
	if err != nil {
		t.Fatalf("engine.Open: %v", err)
	}
	root := apcIngestT(t, e, bead.Bead{
		Type:      "patient_registration",
		Timestamp: apcTimestamp(),
		Author:    "did:medbeads:doctor:12345",
		Content:   map[string]any{"name": "Embed CLI Patient"},
	})
	for i := 0; i < 5; i++ {
		apcIngestT(t, e, bead.Bead{
			Type:      "fhir_observation",
			Timestamp: apcTimestamp(),
			Author:    "did:medbeads:doctor:12345",
			Parents:   []string{root.ID},
			Content:   map[string]any{"note": "obs"},
		})
	}

	depthBefore, err := e.Index().EmbedQueueDepth()
	if err != nil {
		t.Fatalf("EmbedQueueDepth: %v", err)
	}
	if depthBefore == 0 {
		t.Fatal("EmbedQueueDepth = 0 before running embed CLI — Ingest should have enqueued 6 Beads")
	}
	if err := e.Close(); err != nil {
		t.Fatalf("engine.Close: %v", err)
	}

	srv := fakeEmbeddingServer(t)

	if got := run([]string{"embed", "-data", dir, "-embedder", srv.URL, "-batch", "2"}, devNull, devNull); got != 0 {
		t.Fatalf("run(embed -data %s -embedder %s) = %d, want 0", dir, srv.URL, got)
	}

	e2, err := engine.Open(dir)
	if err != nil {
		t.Fatalf("engine.Open (reopen): %v", err)
	}
	defer e2.Close()

	depthAfter, err := e2.Index().EmbedQueueDepth()
	if err != nil {
		t.Fatalf("EmbedQueueDepth (after): %v", err)
	}
	if depthAfter != 0 {
		t.Errorf("EmbedQueueDepth after embed CLI run = %d, want 0 (fully drained)", depthAfter)
	}

	var embedCount int
	if err := e2.Index().SQLDB().QueryRow(`SELECT COUNT(*) FROM bead_embed`).Scan(&embedCount); err != nil {
		t.Fatalf("count bead_embed: %v", err)
	}
	if embedCount != depthBefore {
		t.Errorf("bead_embed row count = %d, want %d (one per originally-queued Bead)", embedCount, depthBefore)
	}
}

// TestRun_EmbedEmptyQueue_Succeeds checks `medbeadsd embed` against a store
// with nothing queued (no Beads ingested at all) exits 0 without contacting
// any embedder — a data directory with no work pending must not fail just
// because the queue happens to be empty.
func TestRun_EmbedEmptyQueue_Succeeds(t *testing.T) {
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer devNull.Close()

	dir := t.TempDir()
	e, err := engine.Open(dir)
	if err != nil {
		t.Fatalf("engine.Open: %v", err)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("engine.Close: %v", err)
	}

	if got := run([]string{"embed", "-data", dir, "-embedder", "http://example.invalid"}, devNull, devNull); got != 0 {
		t.Errorf("run(embed, empty queue) = %d, want 0", got)
	}
}
