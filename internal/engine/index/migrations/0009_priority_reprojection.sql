-- Migration 0009: patient activity hints and patient-scoped rolling link
-- reprojection retry state.
--
-- A knowledge release may affect millions of patients.  Applying it in one
-- synchronous all-patient pass would make ordinary ingestion compete with a
-- long-running maintenance job.  The target link generation is therefore
-- activated as a rolling generation and patients are moved to it atomically,
-- one at a time.  New patient data bypasses the rolling backlog: Ingest
-- projects that patient under the current target generation in its own
-- transaction.
--
-- patient_activity is only a scheduling hint.  It is derived from immutable
-- Beads and may be deleted/rebuilt; it must never be used as clinical state.
-- In particular deceased_hint deliberately fails open to 0/unknown for legacy
-- rows, so missing metadata never causes a possibly-living patient to be
-- deprioritized.
CREATE TABLE patient_activity (
  patient_root       TEXT PRIMARY KEY,
  last_recorded_at   TEXT,
  last_clinical_at   TEXT,
  last_encounter_at  TEXT,
  deceased_hint      INTEGER NOT NULL DEFAULT 0 CHECK (deceased_hint IN (0,1)),
  deceased_at        TEXT,
  updated_at         TEXT NOT NULL
);

CREATE INDEX idx_patient_activity_encounter
  ON patient_activity(deceased_hint, last_encounter_at DESC, patient_root);
CREATE INDEX idx_patient_activity_recorded
  ON patient_activity(last_recorded_at DESC, patient_root);

-- Backfill inexpensive activity fields from the current index.  Patient death
-- is held inside the immutable Patient Bead content rather than a SQL column,
-- so legacy databases retain the safe unknown/alive priority until a reindex or
-- a new patient-registration revision supplies that hint.
INSERT INTO patient_activity
  (patient_root, last_recorded_at, last_clinical_at, last_encounter_at,
   deceased_hint, deceased_at, updated_at)
SELECT patient_root,
       MAX(recorded_at),
       MAX(CASE WHEN type <> 'patient_registration' THEN timestamp END),
       MAX(CASE WHEN type IN ('fhir_encounter','encounter') THEN timestamp END),
       0,
       NULL,
       COALESCE(MAX(recorded_at), '1970-01-01T00:00:00Z')
FROM beads
WHERE patient_root IS NOT NULL AND patient_root <> ''
GROUP BY patient_root;

-- Stale patients are not all materialized here: a mismatch between
-- patient_projection_state.clinical_links_run_id and the active target
-- manifest is the scalable virtual queue. This small table stores only
-- exceptional retry/error state.
CREATE TABLE patient_reprojection_queue (
  patient_root        TEXT PRIMARY KEY,
  target_links_run_id TEXT NOT NULL,
  reason              TEXT NOT NULL,
  enqueued_at         TEXT NOT NULL,
  attempts            INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  last_attempt_at     TEXT,
  last_error          TEXT
);

CREATE INDEX idx_patient_reprojection_target
  ON patient_reprojection_queue(target_links_run_id, enqueued_at, patient_root);
