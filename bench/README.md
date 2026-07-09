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
