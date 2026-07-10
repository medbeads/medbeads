// Package embedder is an HTTP client for an OpenAI-compatible
// `POST /v1/embeddings` endpoint ({model, input: [...]} -> data[].embedding),
// per docs/requirements.md R4.2 and specs/DESIGN_v3.md §6: "Embedder は
// OpenAI 互換 /v1/embeddings HTTP インターフェースに統一（既定: llama.cpp サイド
// カー。GPU サーバーでは vLLM / sentence-transformers に設定1行で差し替え可）".
//
// This package deliberately knows nothing about index.db, bead_embed_queue,
// or vec0 (see the lead's "embedder を index 層に持ち込まない" decision): it is
// a narrow, swappable HTTP client — Client.Embed takes a batch of strings and
// returns a batch of float32 vectors, full stop. The async indexer
// (internal/engine/index's package-level indexer wiring, cmd/medbeadsd) is
// what calls this and writes results into index.db; retrieve/rag_search
// (internal/mcpserver) is what calls this to embed a query string before
// calling index.DB.SemanticSearch. Retry/backoff on a failing embedder is
// deliberately NOT this package's job either (see StartEmbedIndexer's doc
// comment in internal/engine/index) — Client.Embed is a single HTTP
// round-trip that returns an error on any non-2xx response or malformed
// body, and the caller decides what to do with that error.
//
// The embedding server itself (a real llama.cpp/vLLM/sentence-transformers
// process) is out of scope for this project (docs/requirements.md task
// note: "埋め込みサーバーの実体はスコープ外") — this package's own tests use
// httptest fakes only.
package embedder
