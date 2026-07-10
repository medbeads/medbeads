# bench

MICCAI experiment harness (uv Python, not a long-running process). See
specs/DESIGN_v3.md §9. Implementation lands in M2; this is a placeholder
layout.

- `ingest/` — Synthea FHIR → Bead ingestion, with deterministic ground-truth generation
- `scenarios/` — clinical question YAML (patient ID, question, answer, evidence Bead IDs, category, reasoning type)
- `retrieval/` — Retriever interface implementations: `rag.py` / `fts.py` / `dag_nosib.py` / `dag.py` (4 arms)
- `llm/` — shared Claude/Gemini client (fixed temperature/seed, all round-trips logged as JSONL)
- `metrics/` — token efficiency, latency, recall/precision@budget, hallucination rate, causal-order agreement
- `runs/` — run manifests (git commit, config hash, dataset Merkle fingerprint, model version)

`bench/` talks to core only via MCP/REST (see requirements R8.5).

## Dataset: Synthea 1,000 patients (generated 2026-07-10)

The M1/M2 benchmark dataset lives **outside the repo** (and outside Dropbox) at
`~/medbeads-synthea/output/fhir/` — 1,135 patient Bundles (1,000 alive + 135
deceased), ~3.9 GB, FHIR R4. Not committed; ingest tooling takes the path as input.

Reproducible generation (R8.4):

```
cd ~/medbeads-synthea
java -jar synthea-with-dependencies.jar \
  -p 1000 -s 42 -cs 42 \
  --exporter.baseDirectory=./output \
  Massachusetts
```

- Synthea: latest release jar as of 2026-07-10 (`synthea-with-dependencies.jar`,
  GitHub synthetichealth/synthea releases), Java: OpenJDK 23 (Temurin)
- Seeds: patient `-s 42`, clinician `-cs 42` (final RNG=1000 / Clinician RNG=5643,
  recorded in `~/medbeads-synthea/generation.log`)
- `-p 1000` counts living patients; deceased patients encountered during
  simulation are also exported (hence 1,135 bundles)

## Embedding sidecar (`bench/embed_sidecar`)

An OpenAI-compatible `POST /v1/embeddings` HTTP server that the existing Go
embedder client (`internal/engine/embedder/client.go`, unchanged by this
sidecar) talks to — `medbeadsd serve -embedder <url>` and
`medbeadsd embed -embedder <url>` both point at it.

**Model: `intfloat/multilingual-e5-base`, 768 dimensions.**

- 768 dims matches `index.db`'s `bead_embed` vec0 column
  (`internal/engine/index/migrations/0004_embed.sql`'s `embedding FLOAT[768]`)
  exactly — no migration change needed.
- Multilingual e5 (not a Japanese-tuned model like ruri-v3, and not
  llama.cpp/ruri-v3 quantized) was chosen specifically because the M2
  benchmark corpus (Synthea) is English-only: a Japanese-biased embedding
  model would be an unfair handicap for an English-corpus RAG baseline.
  e5-base still covers English at native quality (it's multilingual, not
  Japanese-first), giving a fair comparison point.

### Starting it

```bash
cd bench
uv run python -m bench.embed_sidecar --port 18100
# first run downloads ~1GB of model weights from the HF hub
```

Flags: `--model NAME` (default `intfloat/multilingual-e5-base`), `--host`
(default `127.0.0.1`), `--port` (default `18100`), `--prefix-mode
{none,model_suffix}` (default `none` — see below).

Then point medbeadsd at it. Recommended invocation (e5 prefixes applied
correctly — see the section below):

```bash
# sidecar: apply e5's "query: "/"passage: " task-prefix based on the
# request's model field (see prefix_for in bench/embed_sidecar/model.py)
uv run python -m bench.embed_sidecar --port 18100 --prefix-mode model_suffix

# one-shot backfill (passage side only — see next section)
medbeadsd embed -data <dir> -embedder http://127.0.0.1:18100 -embed-model e5-passage

# resident server: separate passage/query model names route to the right prefix
medbeadsd serve -data <dir> -embedder http://127.0.0.1:18100 \
  -embed-model e5-passage -embed-model-query e5-query ...
```

### Normalization / distance metric

`bead_embed`'s vec0 table has no explicit `distance_metric` override, so
sqlite-vec ranks by raw L2 (Euclidean) distance over the stored vectors
(see `SemanticResult`'s doc comment in `internal/engine/index/embed.go`).
For L2-distance ranking to agree with cosine similarity, every vector
(query and passage alike) must be unit-norm: `||a-b||² = 2 - 2·cos(a,b)`
when `||a||=||b||=1`. The sidecar therefore **always L2-normalizes** its
output vectors (`sentence_transformers`' `normalize_embeddings=True`) —
this is required for correct nearest-neighbor ranking under the existing,
unmodified vec0 schema, not a stylistic choice.

### E5 query/passage prefix convention — resolved via `-embed-model-query`

E5 models are trained expecting a task prefix: `"passage: "` for indexed
documents, `"query: "` for search queries. This sidecar picks the prefix
from the request's `model` field (`--prefix-mode model_suffix`: a
`-query`/`-passage` suffix selects `"query: "`/`"passage: "`; anything
else gets no prefix — see `prefix_for` in `bench/embed_sidecar/model.py`).

That only works if passage traffic and query traffic actually carry
*different* `model` strings on the wire. They didn't, originally:
`medbeadsd serve` built exactly one `embedder.Client` from one
`-embed-model` flag value and passed that *same* Client to both the async
indexer (`StartEmbedIndexer`, passage/document embedding of `search_text`)
and `mcpserver.Config.Embedder` (query embedding in `retrieve`/
`rag_search`) — every `/v1/embeddings` request from one running process
carried the same `model` value regardless of role.

**Fixed** (lead decision, MICCAI comparison fairness — E5 without its
trained prefixes is a real, measurable quality handicap for the RAG
baseline): `cmd/medbeadsd/serve.go` now has a second flag,
`-embed-model-query <name>`, building a *second* `embedder.Client` (see
`buildEmbedClients` in `serve.go`) used only for `mcpCfg.Embedder`
(query-side); `StartEmbedIndexer` keeps using `-embed-model`'s Client
(passage-side) unchanged. Leaving `-embed-model-query` unset reuses
`-embed-model`'s Client for both roles — byte-for-byte the original
behavior — so this is fully backward compatible with every existing
invocation. `medbeadsd embed` (the one-shot backfill CLI) needed **no
change**: it only ever does passage-side batch embedding
(`Engine.DrainEmbedQueue`), so `-embed-model e5-passage` alone is
sufficient there.

Recommended invocation: `--prefix-mode model_suffix` on the sidecar +
`-embed-model e5-passage -embed-model-query e5-query` on `medbeadsd serve`
(and `-embed-model e5-passage` on `medbeadsd embed`) — see the invocation
example above.

### Smoke-tested behavior (scratch data, not the real store)

Verified end-to-end twice, with a 5-patient scratch ingest each time
(`bench/ingest --limit 5`, 308 Beads):

1. **`--prefix-mode none`** (no e5 prefix, either role — the pre-fix
   baseline): `medbeadsd embed` drained 308/308 → `rag_search`/
   `retrieve(semantic=true)` returned semantically sensible neighbors
   (`"hypertension"` → top-5 all `fhir_observation`, `"diabetes
   medication"` → top-5 all `fhir_medicationrequest`, `"immunization
   record"` → top-5 all `fhir_immunization`), L2 distances 0.50–0.58.
2. **`--prefix-mode model_suffix` + `-embed-model-query`** (proper e5
   prefixes, this fix): same 308/308 drain (`-embed-model e5-passage`),
   same `serve -embed-model e5-passage -embed-model-query e5-query`. A
   direct curl to the sidecar confirmed the two model names really do
   produce different vectors for identical input text (`model=e5-query`
   vs `model=e5-passage` on `"hypertension"` → different embedding
   values, proving the prefix dispatch is live on the wire, not just a
   pass-through of the `model` field). Retrieval quality matched or beat
   the no-prefix baseline: `"hypertension"` top-5 `rag_search` distances
   tightened to 0.4904–0.4955 (vs 0.5305–0.5358 without prefixes), same
   correct type composition across all three queries
   (`fhir_observation`/`fhir_medicationrequest`-adjacent/
   `fhir_immunization`).
