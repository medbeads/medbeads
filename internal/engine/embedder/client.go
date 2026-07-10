package embedder

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultModel is the -embed-model flag's default value (cmd/medbeadsd's
// serve subcommand): cl-nagoya/ruri-v3-310m, docs/requirements.md R4.2's
// lead-decided default embedding model, referred to by its short name
// "ruri-v3" the way an OpenAI-compatible server would expect it in a
// {"model": "..."} request field.
const DefaultModel = "ruri-v3"

// DefaultTimeout bounds a single Embed HTTP round-trip. The async indexer
// (index.StartEmbedIndexer) is what supplies retry/backoff across repeated
// failures — this is just the per-attempt deadline so one hung request
// cannot block a batch (or a synchronous retrieve/rag_search query-embed
// call) forever.
const DefaultTimeout = 30 * time.Second

// Client is a thin HTTP client for one OpenAI-compatible
// `POST {BaseURL}/v1/embeddings` endpoint. The zero value is not usable;
// construct with New.
type Client struct {
	baseURL string
	model   string
	http    *http.Client
}

// New returns a Client for baseURL (e.g. "http://localhost:8080", no
// trailing slash required) using model for every Embed call's "model"
// field. An empty model defaults to DefaultModel. httpClient may be nil, in
// which case a *http.Client with DefaultTimeout is used; passing one
// explicitly (e.g. in tests, pointed at an httptest.Server) overrides both
// the transport and the timeout.
func New(baseURL, model string, httpClient *http.Client) *Client {
	if model == "" {
		model = DefaultModel
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: DefaultTimeout}
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		model:   model,
		http:    httpClient,
	}
}

// Model returns the model name this Client sends on every Embed request.
func (c *Client) Model() string {
	return c.model
}

// embeddingsRequest is the OpenAI-compatible request body:
// {"model": "...", "input": ["...", ...]}.
type embeddingsRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

// embeddingsResponse is the OpenAI-compatible response body's shape this
// client actually consumes: data[].embedding, one entry per input string, in
// the same order (per the OpenAI Embeddings API contract this project
// standardizes on — see package doc comment). Other fields a real server
// includes (usage, object, model, data[].index) are accepted but ignored;
// this client does not re-sort by data[].index, matching the "既定
// llama.cpp" reference implementation's behavior of returning results in
// request order (a server that reorders would be a spec violation this
// client does not attempt to work around).
type embeddingsResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

// Embed sends texts as one batched `POST /v1/embeddings` request and
// returns one embedding vector per input string, in the same order. An
// empty texts returns (nil, nil) without making any HTTP request. Any
// non-2xx response, network error, or malformed/short response body is
// returned as an error — Embed makes exactly one HTTP attempt; retrying is
// the caller's responsibility (see package doc comment).
func (c *Client) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	body, err := json.Marshal(embeddingsRequest{Model: c.model, Input: texts})
	if err != nil {
		return nil, fmt.Errorf("embedder: marshal request: %w", err)
	}

	url := c.baseURL + "/v1/embeddings"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("embedder: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embedder: request %s: %w", url, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20)) // 64MiB cap: a pathological/misbehaving server must not exhaust memory on one batch
	if err != nil {
		return nil, fmt.Errorf("embedder: read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("embedder: %s: status %d: %s", url, resp.StatusCode, truncateForError(respBody))
	}

	var parsed embeddingsResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("embedder: unmarshal response: %w", err)
	}
	if len(parsed.Data) != len(texts) {
		return nil, fmt.Errorf("embedder: response has %d embedding(s), want %d (one per input)", len(parsed.Data), len(texts))
	}

	out := make([][]float32, len(parsed.Data))
	for i, d := range parsed.Data {
		if len(d.Embedding) == 0 {
			return nil, fmt.Errorf("embedder: response data[%d] has an empty embedding", i)
		}
		out[i] = d.Embedding
	}
	return out, nil
}

// truncateForError bounds how much of a non-2xx response body an error
// message quotes, so a large HTML error page from a misconfigured base URL
// does not make Embed's error unreadable.
func truncateForError(body []byte) string {
	const maxLen = 500
	s := string(body)
	if len(s) > maxLen {
		return s[:maxLen] + "...(truncated)"
	}
	return s
}
