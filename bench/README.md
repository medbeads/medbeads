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
