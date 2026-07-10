"""Embedding sidecar: OpenAI-compatible `/v1/embeddings` HTTP server backing
intfloat/multilingual-e5-base (768 dims), the lead-decided embedding model
for the M2 RAG baseline (English Synthea; ruri-v3/llama.cpp rejected for
fairness — see bench/README.md's "Embedding sidecar" section for the
rationale).

Go side (internal/engine/embedder.Client) is the contract owner; this
package's request/response shapes are read directly off
internal/engine/embedder/client.go and must not drift from it without a
corresponding review of that file.
"""
