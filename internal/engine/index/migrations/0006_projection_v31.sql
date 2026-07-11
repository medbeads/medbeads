-- Migration 0006: v3.1 projection schema — the new tables/columns (specs/
-- U2_projection_schema.md, specs/DESIGN_v3.1_draft.md §2/§3/§6). Peer-
-- reviewed and confirmed (data-reviewer + Codex, 2026-07-11 U2 entry,
-- docs/decisions.md).
--
-- # Scope: 器（schema）だけ。書き込み/読み取り経路は無傷
--
-- This migration only creates empty tables/indexes and adds one nullable
-- column. It does NOT touch the existing bead_antigens/sibling_pairs write
-- or read paths (write.go, read.go, package apc, package mcpserver) — those
-- keep writing/reading bead_antigens and sibling_pairs exactly as before, so
-- every existing test is unaffected by this migration. The cutover to the
-- new tables (bead_tags, clinical_links, bead_status) plus a full reindex is
-- deliberately deferred to U3: switching both write and read sides in one
-- atomic unit avoids a half-migrated state where some code paths read the
-- old tables and others read the new, empty ones (U2_projection_schema.md's
-- "半端状態が構造的に発生しない" argument). U2's only job is to make the new
-- tables exist so U3 has somewhere to write.

-- ---------------------------------------------------------------------
-- crux 1: beads.recorded_at — required for deterministic correction-chain
-- resolution (DESIGN_v3.1_draft.md §2).
-- ---------------------------------------------------------------------
--
-- beads (0001_init.sql) only has `timestamp`, which is the *clinical event*
-- time (e.g. an FHIR Observation's effectiveDateTime), not the time the Bead
-- itself was written to a Pod. §2's correction-chain resolution needs "most
-- recently written wins" (recorded_at, tie-broken by Bead ID lexical order)
-- to pick the current version of a Bead that has been amended/retracted —
-- and clinical event time is the wrong axis for that (an amendment can carry
-- an *older* clinical timestamp than the record it corrects, e.g. a late
-- correction to a months-ago lab value). Pod frame metadata already carries
-- this: pod/record.go's WrittenAt (RFC3339Nano) is the actual write instant,
-- it is just not projected into the index yet.
--
-- This column is added nullable and left NULL for all existing rows here —
-- U2 does not backfill it. U3's IndexBead will populate recorded_at from Pod
-- meta WrittenAt during the full reindex that switches the write path over;
-- until then, no code reads this column, so leaving old rows NULL is safe.
-- The projection_run_id-carrying tables below make the same choice for the
-- analogous reason (U2 creates the column/table, U3 fills it).
ALTER TABLE beads ADD COLUMN recorded_at TEXT;
CREATE INDEX idx_beads_root_recorded ON beads(patient_root, recorded_at, id);

-- ---------------------------------------------------------------------
-- bead_tags — semantic successor to bead_antigens (0001/0005).
-- ---------------------------------------------------------------------
--
-- Same inverted-index shape as bead_antigens (tag-first PK, per 0001's
-- "antigen-first = inverted index" precedent), and it carries forward all
-- three indexes 0005 added for bead_antigens verbatim (same column shapes,
-- just antigen -> tag): losing any one of them would reintroduce the exact
-- ~200x full-scan regression 0005's migration comment documents, since
-- package apc's frequentAntigens/candidateRows/GetAntigens query shapes
-- carry over unchanged onto whichever table backs "tag" lookups once U3
-- switches the write/read path over.
--
-- No FK to beads(id): the store connects with `_foreign_keys=1` (see
-- index.go), and projection tables are *derived* data that U3's full
-- reindex replaces wholesale (delete old projection_run_id rows, insert
-- new). An FK to beads(id) would impose a delete/insert ordering constraint
-- on that atomic replace; 0002's sibling_pairs already established the
-- precedent of not FK-ing a derived/generated table's members back to
-- beads(id) for this reason (sibling_pairs itself has no such FK either),
-- and this migration follows it for every new projection table below.
--
-- patient_root is nullable (NULL = shared) to match the beads/bead_antigens
-- convention (0001's "NULL = shared" comment on beads.patient_root) rather
-- than sqlite-vec's vec0 convention of using '' for "no value" — mixing the
-- two conventions across tables in the same store would make patient-scope
-- filtering (`WHERE patient_root = ?` vs `WHERE patient_root IS NULL`)
-- inconsistent depending on which table a query touches.
--
-- projection_run_id is row-level provenance (which projection run wrote
-- this row), not a generation/version column meant for multiple generations
-- to coexist: U3's full-reindex-then-atomic-replace design means only one
-- run's rows are ever live at a time. Its purpose is (a) letting a
-- mid-reindex crash be detected (rows tagged with a stale run_id can be
-- swept), and (b) letting an audit query confirm no rows from two different
-- runs are mixed together after a replace.
CREATE TABLE bead_tags (
  tag               TEXT NOT NULL,
  bead_id           TEXT NOT NULL,
  patient_root      TEXT,
  projection_run_id TEXT,
  PRIMARY KEY (tag, bead_id)
);
CREATE INDEX idx_bead_tags_patient ON bead_tags(patient_root, tag, bead_id);
CREATE INDEX idx_bead_tags_bead    ON bead_tags(bead_id, tag);
CREATE INDEX idx_bead_tags_run     ON bead_tags(projection_run_id);

-- ---------------------------------------------------------------------
-- clinical_links — successor to sibling_pairs + bead_edges('sibling')
-- (0001/0002).
-- ---------------------------------------------------------------------
--
-- bead_a/bead_b keep 0002's sibling_pairs convention of normalizing the
-- undirected pair so bead_a < bead_b always (CHECK enforces this, unlike
-- sibling_pairs where it was only an application-level convention) — same
-- reasoning as 0002's comment: the same unordered pair must always
-- normalize to exactly one row regardless of scan order. matched_tag is
-- promoted to its own column (rather than being folded only into a
-- sibling_link Bead body) specifically so audit/monitoring queries can index
-- and filter on "which tag caused this link" directly in SQL, without
-- parsing a Bead payload.
--
-- Uniqueness is (bead_a, bead_b, relation, matched_tag) — the direct
-- successor to sibling_pairs' UNIQUE (bead_a, bead_b, matched_antigen), with
-- `relation` added because clinical_links is no longer sibling-only (it also
-- carries curated-knowledge/guideline-derived relations, see evidence_basis
-- below), so two different relations between the same pair over the same
-- tag are legitimately two different rows. link_id is a separate
-- display/reference ID column (not part of the natural key), for the same
-- reason Beads have both a natural identity and an `id`.
--
-- severity/evidence_basis/evidence_bead_ids and the two CHECK constraints
-- below are the runaway-prevention mechanism specs/U2_projection_schema.md
-- §"実証で決着した crux" and DESIGN_v3.1_draft.md §4 call for: a purely
-- co-occurrence-derived link (evidence_basis='cooccurrence', i.e. "these two
-- Beads share a tag") can only ever be surfaced at severity='info' — it is
-- statistical correlation, not a vetted clinical claim, so it must never be
-- allowed to masquerade as a 'warning'/'alert'/'critical' finding. Anything
-- at 'warning' or above must instead cite curated_knowledge or a guideline
-- (evidence_basis) AND carry both a concrete rule_version (which knowledge
-- Bead generation asserted this) and a non-empty evidence_bead_ids array (at
-- least one citation) — so severity above 'info' is always traceable to an
-- explicit, versioned clinical source, never bare statistics. This is
-- enforced here as a CHECK constraint (not just application-level
-- validation) precisely so it holds even if a future projector has a bug:
-- the database itself refuses to store an unsupported high-severity claim.
--
-- evidence_bead_ids and score_breakdown are stored as canonical JSON text
-- (DEFAULT '[]' / '{}') and are read/written whole by the application layer,
-- never filtered or joined on inside SQL — consistent with how this
-- project already stores JSON blobs elsewhere (e.g. pods/beads metadata),
-- so no extra JSON1-based indexing is introduced here.
--
-- No FK to beads(id), same reasoning as bead_tags above.
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
  UNIQUE (bead_a, bead_b, relation, matched_tag)
);
CREATE INDEX idx_clinical_links_a           ON clinical_links(bead_a);
CREATE INDEX idx_clinical_links_b           ON clinical_links(bead_b);
CREATE INDEX idx_clinical_links_patient_sev ON clinical_links(patient_root, severity, relation);
CREATE INDEX idx_clinical_links_run         ON clinical_links(projection_run_id);

-- ---------------------------------------------------------------------
-- bead_status — deterministic correction-chain resolution (DESIGN_v3.1_
-- draft.md §2).
-- ---------------------------------------------------------------------
--
-- This is a distinct concept from FHIR's own clinicalStatus (e.g. an
-- Observation/Condition's own `status` field, which is clinical metadata
-- about the resource's meaning) — bead_status instead tracks *this store's*
-- correction/attestation bookkeeping for a Bead: whether a later Bead
-- amended or retracted it, and if so, which Bead is now "current". retrieve
-- (and any other read path) uses current_bead_id to transparently resolve
-- an amended Bead reference straight to its latest valid version without
-- the caller needing to walk the correction chain itself.
--
-- No FK to beads(id), same derived-data/atomic-replace reasoning as
-- bead_tags and clinical_links above.
CREATE TABLE bead_status (
  bead_id             TEXT PRIMARY KEY,
  status              TEXT NOT NULL CHECK (status IN ('active','amended','retracted','unattested')),
  current_bead_id     TEXT,
  superseded_by       TEXT,
  attestation_bead_id TEXT,
  retraction_bead_id  TEXT,
  reason              TEXT,
  projection_run_id   TEXT
);
CREATE INDEX idx_bead_status_active  ON bead_status(status, bead_id);
CREATE INDEX idx_bead_status_current ON bead_status(current_bead_id);

-- ---------------------------------------------------------------------
-- projection_manifest — append-only ledger of projection runs.
-- ---------------------------------------------------------------------
--
-- Every U3+ full-reindex run gets one row here, recording exactly what went
-- into it: code_version (git SHA + build tag/algorithm version) and
-- knowledge_bead_ids (the JSON array of dictionary/link_rule Bead IDs
-- consulted) together let a later audit reconstruct "what logic + what
-- knowledge produced this projection_run_id's rows" without needing the
-- original run's process state — the chain of custody DESIGN_v3.1_draft.md
-- §6 requires stays intact even long after the run finished.
-- input_watermarks (JSON: pod path -> indexed_upto) is what makes an
-- incremental re-run reproducible/resumable rather than always requiring a
-- from-scratch full reindex.
--
-- status/activated_at/superseded_at plus the CHECK constraint encode the
-- run lifecycle: a run starts 'building', and only a run that reaches
-- 'active' or 'superseded' may have activated_at set (a 'failed' run never
-- did, by construction). The partial UNIQUE index below is what actually
-- enforces "at most one active run per projection_name" — this is the
-- mechanism U3's atomic activation-flip (old run: active -> superseded, new
-- run: building -> active, in the same transaction) relies on: the database
-- itself refuses to let two runs of the same projection be 'active'
-- simultaneously, closing the same "constraint enforced in SQL, not just in
-- application code" gap clinical_links' CHECK constraints close above.
CREATE TABLE projection_manifest (
  run_id             TEXT PRIMARY KEY,
  projection_name    TEXT NOT NULL,
  code_version       TEXT NOT NULL,
  knowledge_bead_ids TEXT NOT NULL DEFAULT '[]',
  config_hash        TEXT NOT NULL,
  input_watermarks   TEXT NOT NULL,
  built_at           TEXT NOT NULL,
  activated_at       TEXT,
  superseded_at      TEXT,
  status             TEXT NOT NULL CHECK (status IN ('building','active','superseded','failed')),
  CHECK (activated_at IS NULL OR status IN ('active','superseded'))
);
-- Partial unique index: at most one 'active' manifest row per
-- projection_name at any time (rows with any other status are unconstrained
-- by this index, so history accumulates freely).
CREATE UNIQUE INDEX idx_projection_manifest_one_active
  ON projection_manifest(projection_name) WHERE status = 'active';

-- ---------------------------------------------------------------------
-- Intentionally NOT created in U2 (over-engineering avoidance — both peer
-- reviews agreed on this point):
-- ---------------------------------------------------------------------
--
-- active_conditions / active_medications: these will start as VIEWs (or a
-- read-time reconstruction) in U4, materialized into physical tables only if
-- measurement shows a VIEW is too slow at scale. Committing to a physical
-- table shape for them now, before U3/U4 land the projector logic that
-- would populate them, risks locking in a schema that has to be reworked
-- once real query patterns are known.
