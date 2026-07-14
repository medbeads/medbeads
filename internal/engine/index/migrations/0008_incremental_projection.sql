-- Migration 0008: patient-local incremental projection bookkeeping.
--
-- projection_manifest remains the immutable ledger for an interpretation
-- generation: code_version + the exact knowledge Bead set identify the logic
-- that produced clinical_links / record_state rows.  A patient Pod can keep
-- growing while that generation stays active, however, so its original global
-- input_watermarks snapshot is not enough to prove how far EACH patient has
-- been projected after later appends.
--
-- patient_projection_state is the small, rebuildable current-state companion:
-- one row says that every frame through indexed_upto in this patient Pod has
-- been reflected in BOTH derived projections under the named generation run
-- IDs.  Ingest updates this row in the same SQLite transaction as IndexBead,
-- clinical_links, bead_status and active_*; therefore a reader can never see an
-- indexed Bead with an older committed patient projection.  After a crash in
-- the Pod-append-before-SQLite-commit window, CatchUp advances pods.indexed_upto
-- but this row remains behind; Engine.Open detects that mismatch and reprojects
-- only the affected patient before serving reads.
--
-- This table is deliberately mutable derived state, not a fact ledger.  Pod
-- frames and projection_manifest retain immutable provenance; deleting this
-- table merely causes a patient-local rebuild on the next automatic Open.
CREATE TABLE patient_projection_state (
  patient_root          TEXT PRIMARY KEY,
  pod_path              TEXT NOT NULL,
  indexed_upto          INTEGER NOT NULL CHECK (indexed_upto >= 0),
  clinical_links_run_id TEXT NOT NULL,
  record_state_run_id   TEXT NOT NULL,
  projected_at          TEXT NOT NULL
);

CREATE INDEX idx_patient_projection_links_run
  ON patient_projection_state(clinical_links_run_id, patient_root);
CREATE INDEX idx_patient_projection_status_run
  ON patient_projection_state(record_state_run_id, patient_root);
