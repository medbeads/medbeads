-- Migration 0001: initial index.db schema (specs/DESIGN_v3.md §5).
--
-- Scope for R3: pods / beads / bead_edges / bead_antigens / beads_fts only.
-- bead_apc_scan, clearance_rules/clearance_audit, and the sqlite-vec vec0
-- table are later units' responsibility (see DESIGN §5 comments) and are
-- deliberately NOT created here.

CREATE TABLE pods (
  pod_id INTEGER PRIMARY KEY,
  path TEXT UNIQUE NOT NULL,
  patient_root TEXT,
  size INTEGER NOT NULL DEFAULT 0,
  indexed_upto INTEGER NOT NULL DEFAULT 0   -- crash-recovery watermark (R1.3)
);

CREATE TABLE beads (
  id TEXT PRIMARY KEY,
  patient_root TEXT,                    -- resolved at write time; NULL = shared
  type TEXT NOT NULL,
  timestamp TEXT NOT NULL,
  pod_id INTEGER NOT NULL REFERENCES pods(pod_id),
  offset INTEGER NOT NULL,
  length INTEGER NOT NULL,
  summary TEXT                          -- machine-generated one-line summary (L1 token budget)
);
CREATE INDEX idx_beads_root ON beads(patient_root, timestamp);
CREATE INDEX idx_beads_type ON beads(type);

CREATE TABLE bead_edges (
  child_id TEXT NOT NULL,
  parent_id TEXT NOT NULL,
  edge_type TEXT NOT NULL DEFAULT 'parent',   -- 'parent' | 'sibling'
  PRIMARY KEY (child_id, parent_id, edge_type)
);
CREATE INDEX idx_edge_parent ON bead_edges(parent_id, edge_type);

CREATE TABLE bead_antigens (
  antigen TEXT NOT NULL,
  bead_id TEXT NOT NULL,
  patient_root TEXT,
  PRIMARY KEY (antigen, bead_id)              -- antigen-first = inverted index
);

-- beads_fts is a "contentless" FTS5 table (content=''): SQLite stores no
-- shadow copy of any column, including UNINDEXED ones — a column declared
-- UNINDEXED here would be write-only (accepted on INSERT, silently
-- unretrievable and unfilterable on SELECT/WHERE), so unlike
-- specs/DESIGN_v3.md §5's literal DDL, this table has no `id` column at
-- all. Instead, each row's `rowid` is set explicitly to its beads.rowid
-- (SQLite's implicit per-row integer, distinct from beads.id which is a
-- TEXT PRIMARY KEY and therefore not a rowid alias) at INSERT time, so a
-- hit can be resolved back to its Bead via `JOIN beads ON beads.rowid =
-- beads_fts.rowid` — the single JOIN specs/DESIGN_v3.md §5 calls for
-- ("JOIN 1回で患者集約"), just keyed on rowid rather than a stored id
-- column.
CREATE VIRTUAL TABLE beads_fts USING fts5(
  search_text,
  tokenize='trigram',
  content=''
);
