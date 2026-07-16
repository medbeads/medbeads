# Public paper-demo data

This directory contains the immutable Pod sources for the public MedBeads
paper demo. The data consists of **10 synthetic Synthea patients** selected
deterministically (filename-ascending, first 10) from a 1,135-patient Synthea
FHIR corpus and converted with the v3 ingestion pipeline.

- Patients: 10
- Patient Beads: 4,202
- Shared knowledge Pod: 1
- Expected projected `clinical_links`: 492
- Real patient information: none

Only `pods/` is distributed. `index.db` is deliberately excluded because it
is derived state. The Docker image runs `medbeadsd reindex` and
`medbeadsd reproject` while building, proving that the index and clinical
links can be reconstructed from the Pod sources and the versioned projection
code. `CHECKSUMS.sha256` fixes the distributed Pod-file bytes; `medbeadsd
verify` additionally checks every frame CRC and content-addressed Bead ID.

The public demo does not provide patient registration. It may write temporary
derived-state changes (for example, a synthetic clearance demonstration)
inside its disposable container. Removing the containers resets those changes.

This dataset is for research demonstration only and must not be used for
clinical care.
