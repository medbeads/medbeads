-- Migration 0010: precomputed scheduling timestamp for index-ordered rolling
-- projection selection. Without this column, ORDER BY CASE/COALESCE over a
-- million stale patients would build a large temporary sort for every small
-- background batch.
ALTER TABLE patient_activity ADD COLUMN last_visit_at TEXT;

UPDATE patient_activity
SET last_visit_at = CASE
  WHEN last_encounter_at IS NULL THEN last_clinical_at
  WHEN last_clinical_at IS NULL THEN last_encounter_at
  WHEN last_encounter_at >= last_clinical_at THEN last_encounter_at
  ELSE last_clinical_at
END;

CREATE INDEX idx_patient_activity_priority
  ON patient_activity(deceased_hint, last_visit_at DESC, patient_root);
