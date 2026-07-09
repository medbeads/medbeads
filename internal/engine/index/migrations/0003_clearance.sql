-- Migration 0003: clearance rules + audit log (specs/DESIGN_v3.md §2's
-- "clearance/ embedded + DB ルール・監査ログ（v2 から移植）", §5's
-- "clearance_rules / clearance_audit は v2 踏襲", docs/requirements.md §8
-- ("Embedded Clearance の UI/監査完備は v3.0 スコープ外、M4")).
--
-- Schema is v2's core/store.go InitDB clearance_rules/clearance_audit DDL,
-- carried forward with only the ALTER TABLE-based allowed_roles migration
-- folded directly into the CREATE TABLE (v2 added allowed_roles via a
-- separate ALTER TABLE for existing DBs; a fresh v3 schema has no such
-- existing-DB concern, so the column is simply part of the table from the
-- start here).
--
-- bead_id intentionally has no FOREIGN KEY REFERENCES beads(id): a clearance
-- rule may be authored for a Bead ID before that Bead itself is ever
-- ingested/indexed here (v2 had no such constraint either), and rules are
-- independent, DB-only overlays layered on top of (not a structural part
-- of) the Merkle DAG the beads table represents.
CREATE TABLE clearance_rules (
  id            TEXT PRIMARY KEY,
  bead_id       TEXT NOT NULL,
  denied_roles  TEXT NOT NULL,   -- JSON array of role strings (blacklist)
  allowed_roles TEXT,            -- JSON array of role strings (whitelist); NULL = unset
  created_by    TEXT NOT NULL,
  created_at    TEXT NOT NULL,
  reason        TEXT,
  expires_at    TEXT             -- NULL = permanent
);
CREATE INDEX idx_clearance_bead ON clearance_rules(bead_id);

CREATE TABLE clearance_audit (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  bead_id    TEXT NOT NULL,
  action     TEXT NOT NULL,
  user_id    TEXT NOT NULL,
  user_roles TEXT NOT NULL,      -- JSON array of role strings
  timestamp  TEXT NOT NULL,
  details    TEXT
);
CREATE INDEX idx_audit_bead ON clearance_audit(bead_id);
