-- Migration 0011: preserve one clinical-link assertion per rule revision.
--
-- The original natural key omitted rule_version, so two independent guideline
-- Beads asserting the same relation/tag over the same Bead pair overwrote one
-- another. That loses provenance. rule_version is now part of uniqueness;
-- retrieval may group equivalent assertions for display, but storage retains
-- every exact knowledge source.
DROP INDEX idx_clinical_links_a;
DROP INDEX idx_clinical_links_b;
DROP INDEX idx_clinical_links_patient_sev;
DROP INDEX idx_clinical_links_run;

ALTER TABLE clinical_links RENAME TO clinical_links_pre_0011;

CREATE TABLE clinical_links (
  link_id           TEXT NOT NULL,
  bead_a            TEXT NOT NULL,
  bead_b            TEXT NOT NULL,
  patient_root      TEXT NOT NULL,
  relation          TEXT NOT NULL,
  matched_tag       TEXT NOT NULL,
  severity          TEXT NOT NULL CHECK (severity IN ('info','warning','alert','critical')),
  evidence_basis    TEXT NOT NULL CHECK (evidence_basis IN ('cooccurrence','curated_knowledge','guideline')),
  evidence_bead_ids TEXT NOT NULL DEFAULT '[]',
  score_breakdown   TEXT NOT NULL DEFAULT '{}',
  rule_id           TEXT,
  rule_version      TEXT,
  projection_run_id TEXT,
  created_at        TEXT NOT NULL,
  CHECK (bead_a < bead_b),
  CHECK (severity = 'info'
         OR (evidence_basis IN ('curated_knowledge','guideline')
             AND rule_version IS NOT NULL AND evidence_bead_ids <> '[]')),
  UNIQUE (bead_a, bead_b, relation, matched_tag, rule_version)
);

INSERT INTO clinical_links
  (link_id, bead_a, bead_b, patient_root, relation, matched_tag, severity,
   evidence_basis, evidence_bead_ids, score_breakdown, rule_id, rule_version,
   projection_run_id, created_at)
SELECT link_id, bead_a, bead_b, patient_root, relation, matched_tag, severity,
       evidence_basis, evidence_bead_ids, score_breakdown, rule_id, rule_version,
       projection_run_id, created_at
FROM clinical_links_pre_0011;

DROP TABLE clinical_links_pre_0011;

CREATE INDEX idx_clinical_links_a           ON clinical_links(bead_a);
CREATE INDEX idx_clinical_links_b           ON clinical_links(bead_b);
CREATE INDEX idx_clinical_links_patient_sev ON clinical_links(patient_root, severity, relation);
CREATE INDEX idx_clinical_links_run         ON clinical_links(projection_run_id);
CREATE INDEX idx_clinical_links_rule        ON clinical_links(rule_version, patient_root);
