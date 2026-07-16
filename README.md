# MedBeads

MedBeads is an agent-oriented medical information substrate built from
content-addressed, immutable **Beads** and rebuildable **clinical links**. A
patient's longitudinal record is stored in a patient-scoped Pod; deterministic
link traversal assembles the connected context an AI system needs without
asking a probabilistic retriever to guess what is missing.

![MedBeads concept](docs/concept-image.jpeg)

> **Public distribution profile:** This repository is a research reference
> implementation and paper-reproduction demo. It contains synthetic data only
> and is not a production EHR, medical device, or clinical-use deployment.
> See [R16](specs/R16_public_demo_and_production_boundary.md).

[日本語](README.ja.md)

## Quick start with Docker

Requirements: Docker Desktop or Docker Engine with Compose v2.

```bash
git clone https://github.com/medbeads/medbeads.git
cd medbeads
docker compose up --build
```

Open **http://localhost:5174**. The first build compiles the Go core, verifies
the Pod files, reconstructs `index.db`, projects the clinical links, and builds
the React UI. No API key, external FHIR server, Python environment, Go toolchain,
or local Node.js installation is required.

The demo binds only to localhost:

- UI: http://127.0.0.1:5174
- REST API and MCP HTTP endpoint: http://127.0.0.1:8080

Stop and remove the disposable demo containers with:

```bash
docker compose down
```

Running `docker compose up` again starts from the synthetic seed embedded in
the image. To rebuild after source changes, use `docker compose up --build`.

## What to verify

The bundled dataset contains 10 deterministic Synthea patients, 4,202 patient
Beads, one shared knowledge Pod, and 492 projected clinical links.

1. Open the UI and select a patient.
2. Inspect the vertical parent/time DAG and horizontal `clinical_links`.
3. Change the viewer role to observe security-clearance filtering.
4. Check the REST API:

   ```bash
   curl http://127.0.0.1:8080/patients
   ```

5. Verify every Pod frame and Bead hash:

   ```bash
   docker compose exec core medbeadsd verify -data /data
   ```

The same core exposes MCP at `/mcp`, including deterministic retrieval and
bounded clinical-link expansion for an AI client. An external LLM is optional
and is not part of the paper-demo containers.

## Public demo versus production development

The public demo deliberately provides:

- ten synthetic patients and a fixed Bead corpus;
- no new-patient registration or `create_bead` access;
- a `viewer` MCP role without a service token;
- localhost-only ports;
- no FHIR credentials, hospital identity, private key, or real patient data;
- disposable derived-state changes for demonstrating clearance behavior.

Production development uses the same core semantics but a separately controlled
private deployment overlay. Hospital FHIR endpoints, patient identity mapping,
trust policies, credentials, KMS/HSM integration, monitoring, backups, and real
data must never be added to this public demo configuration. MedBeads is not
described as production-ready until the R13/R14 integrity controls and the
operational, security, performance, and regulatory validation gates are met.

## Architecture

```text
Browser
  │
  ▼
Nginx / React UI :5174
  │  /api/core/*
  ▼
medbeadsd :8080 ── REST + MCP
  │
  ├── patient Pod files       immutable source of truth
  └── index.db                rebuildable search/link projection
```

`medbeadsd` is the single Go daemon for the engine, REST API, and MCP server.
The Docker build reconstructs SQLite from the committed Pod files rather than
shipping a developer-machine database.

## Local development without Docker

Requirements: Go 1.25+, a C compiler, and Node.js 24+.

```bash
# Core
CGO_ENABLED=1 go build -tags sqlite_fts5 -o medbeadsd ./cmd/medbeadsd
./medbeadsd reindex -data ./demo_data
./medbeadsd reproject -data ./demo_data -code-version local-demo -record-state -drain
./medbeadsd serve -data ./demo_data -role viewer -http 127.0.0.1:8080 \
  -projection-code-version local-demo
```

In a second terminal:

```bash
cd ui
cp .env.example .env.local
npm ci
npm run dev
```

The Python code under `bench/` is for reproducible ingestion and benchmarks;
it is not a runtime service in v3.

## Integrity and derived links

- A Bead ID is the SHA-256 digest of its canonical content.
- Beads are appended to patient-scoped Pod files and are not edited in place.
- `index.db` is derived and can be rebuilt from Pods.
- `clinical_links` are derived from versioned rules and projection code and can
  be rolled forward patient by patient.
- Retrieval enforces record status, patient partition, security clearance, and
  explicit depth/item/token bounds.

Design and implementation decisions are recorded in [`specs/`](specs/) and
[`docs/decisions.md`](docs/decisions.md).

## Tests

```bash
CGO_ENABLED=1 go test -tags sqlite_fts5 ./... -race
cd ui && npm test && npm run build
```

## Citation

If you use MedBeads in research, cite the paper
([arXiv:2602.01086](https://arxiv.org/abs/2602.01086)):

```bibtex
@article{nakajima2026medbeads,
  title={MedBeads: An Agent-Native, Immutable Data Substrate for Trustworthy Medical AI},
  author={Nakajima, Takahito},
  journal={arXiv preprint arXiv:2602.01086},
  year={2026},
  doi={10.48550/arXiv.2602.01086},
  url={https://arxiv.org/abs/2602.01086}
}
```

`CITATION.cff` supports GitHub's “Cite this repository” feature.

## License and data

Licensed under the [Apache License 2.0](LICENSE). See [`NOTICE`](NOTICE).
All bundled patient data is synthetic and generated by
[Synthea](https://synthetichealth.github.io/synthea/); no real protected
health information is included.
