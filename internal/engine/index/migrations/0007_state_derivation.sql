-- Migration 0007: state derivation foundation (specs/U4_state_derivation.md,
-- docs/decisions.md 2026-07-11 U4 entry). Peer-reviewed and confirmed
-- (data-reviewer + Codex, both conditional-GO -> confirmed after the two
-- correctness holes below were closed). 0006 is immutable; this migration is
-- append-only on top of it.
--
-- # Scope: schema only, U4a. The record_state projector (specs/
-- U4_state_derivation.md's section 2 resolution algorithm: retracted mark ->
-- attestation gate -> amends replace) is U4b's job. This migration only adds
-- the columns/tables U4b needs somewhere to write; no projector writes to
-- them yet, so every existing test and read/write path is unaffected.

-- ---------------------------------------------------------------------
-- bead_status.patient_root — per-patient atomic replace (0006's bead_status
-- table has no patient scoping column yet).
-- ---------------------------------------------------------------------
--
-- U3's clinical_links already established the pattern this table now
-- follows: a full-reprojection run replaces one patient's rows at a time
-- inside a single transaction (DELETE the patient's stale rows, INSERT the
-- newly-computed set), rather than one all-patients transaction, to avoid
-- holding index.db's single writer lock (index.go's SetMaxOpenConns(1))
-- for the whole run's duration (specs/U3_link_projector.md's crux, mirrored
-- in reproject.go's own doc comment). bead_status needs the identical
-- per-patient DELETE+INSERT scope for U4b's record_state projector, but
-- 0006 only gave it a bare bead_id primary key with no patient column to
-- scope a DELETE by — WHERE patient_root = ? is what makes "replace only
-- this patient's status rows" possible without a full-table scan or a JOIN
-- back to beads(id) (which projection tables deliberately avoid an FK to,
-- per 0006's comment on bead_tags/clinical_links/bead_status, for the same
-- atomic-replace-ordering reason).
--
-- A JOIN-based scoping (deriving patient_root at query time by joining
-- bead_status to beads on bead_id) was considered and rejected: it would
-- work for reads, but the per-patient DELETE this projector's atomic
-- replace needs must scope on a column bead_status itself carries, not one
-- it has to look up via a join, so the DELETE's WHERE clause stays a single
-- indexed-column comparison exactly like clinical_links.patient_root.
--
-- Nullable (not NOT NULL) because ALTER TABLE ADD COLUMN cannot backfill a
-- NOT NULL column without a default in SQLite, and no default value would
-- honestly describe pre-migration rows anyway — 0006 shipped with 0 rows in
-- bead_status in every store this has run against, so there is nothing to
-- backfill in practice, but leaving the column NULL-able is the honest
-- schema regardless.
ALTER TABLE bead_status ADD COLUMN patient_root TEXT;
CREATE INDEX idx_bead_status_patient ON bead_status(patient_root, status, bead_id);

-- ---------------------------------------------------------------------
-- active_conditions / active_medications — current-problem-list /
-- current-medication-list projection tables (specs/U4_state_derivation.md's
-- 0007 migration block).
-- ---------------------------------------------------------------------
--
-- 0006 deliberately did NOT create these tables yet (its own closing
-- comment: "committing to a physical table shape before U3/U4 land the
-- projector logic... risks locking in a schema before real query patterns
-- are known"). U4's design settles this: these must be PHYSICAL tables, not
-- SQL VIEWs, because the one piece of data a VIEW would need to expose —
-- FHIR content.clinicalStatus (e.g. a Condition/MedicationRequest resource's
-- own status field) — lives only inside the Bead's Pod-stored content, never
-- projected into any index.db column. A VIEW can only combine columns
-- index.db already has; it cannot decode a Pod frame's JSON body, so there
-- is no SQL expression that could compute "clinicalStatus='active' AND
-- bead_status.status='active'" without a physical table a projector first
-- populates by decoding that Pod content. Read-time reconstruction (walking
-- Pods on every read) was also rejected, for the same reason U3's
-- clinical_links rejected it: it would throw away the incremental/
-- deterministic-projection property this store's whole design relies on,
-- forcing every read to redo work a projection run already did once.
--
-- These two tables are record_state_v31's output (specs/
-- U4_state_derivation.md: "别 projection_name = 'record_state_v31'... 同一
-- run・同一患者 tx で bead_status + active_* を3表 DELETE+INSERT"), populated
-- by U4b's projector when it decodes a patient's Pod content and derives
-- status — they are created empty here and stay empty until U4b lands.
--
-- current_bead_id (NOT NULL, unlike bead_status.current_bead_id which is
-- nullable for the retracted case) is the resolved chain leaf a reader
-- should treat as authoritative for this condition/medication: since a row
-- only exists in active_conditions/active_medications at all when the
-- underlying fact is still active (not retracted -- a retracted fact has no
-- "current" version and is therefore projected out of these tables
-- entirely, not represented here with a NULL), current_bead_id always names
-- a real Bead for any row that exists.
--
-- No FK to beads(id), same atomic-replace/derived-data reasoning as every
-- other projection table since 0006 (bead_tags/clinical_links/bead_status):
-- an FK would impose delete/insert ordering constraints on the
-- per-patient-transaction replace these tables are designed around.
--
-- PRIMARY KEY (patient_root, bead_id): bead_id alone would already be
-- unique (a Bead belongs to exactly one patient_root), but leading with
-- patient_root matches this table's own idx_active_conditions_patient/
-- idx_active_medications_patient query shape (WHERE patient_root = ?,
-- optionally + clinical_status/medication_status) and mirrors
-- clinical_links'/bead_tags' patient-first key convention.
CREATE TABLE active_conditions (
  patient_root        TEXT NOT NULL,
  bead_id              TEXT NOT NULL,
  current_bead_id      TEXT NOT NULL,
  clinical_status      TEXT,
  verification_status  TEXT,
  projection_run_id    TEXT NOT NULL,
  PRIMARY KEY (patient_root, bead_id)
);
CREATE INDEX idx_active_conditions_patient ON active_conditions(patient_root, clinical_status, bead_id);

CREATE TABLE active_medications (
  patient_root        TEXT NOT NULL,
  bead_id              TEXT NOT NULL,
  current_bead_id      TEXT NOT NULL,
  medication_status    TEXT,
  intent               TEXT,
  projection_run_id    TEXT NOT NULL,
  PRIMARY KEY (patient_root, bead_id)
);
CREATE INDEX idx_active_medications_patient ON active_medications(patient_root, medication_status, bead_id);
