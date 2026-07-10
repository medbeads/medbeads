package embedder

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeDim is the deterministic fake embedder's vector width — deliberately
// not index.EmbedDim (768): this package has no dependency on package index
// (see doc.go), so its own tests only need internal consistency, not the
// real schema's dimension. Callers wiring a real 768-dim httptest fake for
// index/mcpserver-level tests build their own (see those packages' tests).
const fakeDim = 8

// deterministicVector derives a fakeDim-length float32 vector from text's
// sha256 digest, so the same input text always embeds to the same vector
// (byte-for-byte) across calls — good enough for this package's own
// Client.Embed round-trip tests (order preservation, batch size, error
// paths) without needing any real embedding model.
func deterministicVector(text string) []float32 {
	sum := sha256.Sum256([]byte(text))
	out := make([]float32, fakeDim)
	for i := range out {
		out[i] = float32(sum[i]) / 255.0
	}
	return out
}

// newFakeEmbedderServer returns an httptest.Server implementing the
// OpenAI-compatible POST /v1/embeddings contract this package's Client
// speaks: {model, input: [...]} -> {data: [{embedding: [...]}, ...]}, one
// deterministicVector per input string, in request order.
func newFakeEmbedderServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/embeddings", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req embeddingsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
			return
		}
		resp := embeddingsResponse{}
		for _, text := range req.Input {
			resp.Data = append(resp.Data, struct {
				Embedding []float32 `json:"embedding"`
			}{Embedding: deterministicVector(text)})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestClient_Embed_ReturnsOneVectorPerInput_InOrder(t *testing.T) {
	srv := newFakeEmbedderServer(t)
	c := New(srv.URL, "", nil)

	texts := []string{"first observation", "second observation", "third observation"}
	got, err := c.Embed(context.Background(), texts)
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(got) != len(texts) {
		t.Fatalf("Embed returned %d vector(s), want %d", len(got), len(texts))
	}
	for i, text := range texts {
		want := deterministicVector(text)
		if len(got[i]) != len(want) {
			t.Fatalf("vector %d has %d dims, want %d", i, len(got[i]), len(want))
		}
		for j := range want {
			if got[i][j] != want[j] {
				t.Fatalf("vector %d differs from expected at dim %d: got %v want %v", i, j, got[i][j], want[j])
			}
		}
	}
}

func TestClient_Embed_EmptyInput_NoRequest(t *testing.T) {
	called := false
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/embeddings", func(w http.ResponseWriter, r *http.Request) {
		called = true
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := New(srv.URL, "", nil)
	got, err := c.Embed(context.Background(), nil)
	if err != nil {
		t.Fatalf("Embed(nil): %v", err)
	}
	if got != nil {
		t.Fatalf("Embed(nil) = %v, want nil", got)
	}
	if called {
		t.Fatal("Embed(nil) made an HTTP request, want none")
	}
}

func TestClient_Embed_ServerDown_ReturnsError(t *testing.T) {
	srv := newFakeEmbedderServer(t)
	srv.Close() // connection refused for every subsequent request

	c := New(srv.URL, "", nil)
	_, err := c.Embed(context.Background(), []string{"anything"})
	if err == nil {
		t.Fatal("Embed against a closed server: want error, got nil")
	}
}

func TestClient_Embed_ServerErrorStatus_ReturnsError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/embeddings", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := New(srv.URL, "", nil)
	_, err := c.Embed(context.Background(), []string{"anything"})
	if err == nil {
		t.Fatal("Embed against a 500 server: want error, got nil")
	}
}

func TestClient_Embed_MismatchedResponseLength_ReturnsError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/embeddings", func(w http.ResponseWriter, r *http.Request) {
		// Always returns exactly one embedding, regardless of how many inputs
		// were requested — exercises the "one embedding per input" length
		// check.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(embeddingsResponse{Data: []struct {
			Embedding []float32 `json:"embedding"`
		}{{Embedding: deterministicVector("only one")}}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := New(srv.URL, "", nil)
	_, err := c.Embed(context.Background(), []string{"first", "second"})
	if err == nil {
		t.Fatal("Embed with mismatched response length: want error, got nil")
	}
}

func TestNew_DefaultModel(t *testing.T) {
	c := New("http://example.invalid", "", nil)
	if c.Model() != DefaultModel {
		t.Errorf("Model() = %q, want %q", c.Model(), DefaultModel)
	}
}

func TestNew_ExplicitModel(t *testing.T) {
	c := New("http://example.invalid", "custom-model", nil)
	if c.Model() != "custom-model" {
		t.Errorf("Model() = %q, want %q", c.Model(), "custom-model")
	}
}
