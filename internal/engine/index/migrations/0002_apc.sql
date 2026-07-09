-- Migration 0002: APC scanner state (specs/DESIGN_v3.md §7, §5's
-- "bead_apc_scan ... は v2 踏襲" note, docs/requirements.md R5).
--
-- bead_apc_scan is v2's scan-management table (specs/MEDBEADS_SIBLING_SPEC.md
-- §6.3), carried forward unchanged: one row per Bead that has been scanned by
-- the APC batch scanner, tracking sibling_count (runaway-prevention point b:
-- max_siblings_per_bead) and scan_generation (the highest generation any
-- sibling_link this Bead participated in has reached, for the
-- generation<=2-decay rule, runaway-prevention point c).
--
-- v2 has no equivalent of sibling_pairs: v2's only de-duplication was
-- "already_linked(bead_A, bead_B)" checked ad hoc against bead_edges inside
-- the matching loop (SPEC §6.4), not a durable, indexed, UNIQUE-enforced
-- record. DESIGN §7 point 1 calls for "sibling_pairs(bead_a, bead_b,
-- matched_antigen) UNIQUE 制約" explicitly as a runaway-prevention mechanism,
-- so this migration adds a minimal new table for it: the UNIQUE constraint
-- itself (not just an application-level check) is what makes "同一ペア×同一
-- 抗原の再生成防止" (runaway-prevention point a) hold even under concurrent or
-- re-run scans, which an ad hoc bead_edges lookup cannot guarantee on its
-- own. bead_a/bead_b are stored in lexicographic order (bead_a < bead_b) so
-- the same undirected pair always normalizes to one row regardless of which
-- Bead the scanner happened to visit first.
CREATE TABLE bead_apc_scan (
  bead_id         TEXT PRIMARY KEY REFERENCES beads(id),
  scanned_at      TEXT NOT NULL,
  scan_generation INTEGER NOT NULL DEFAULT 0,
  sibling_count   INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE sibling_pairs (
  bead_a          TEXT NOT NULL,
  bead_b          TEXT NOT NULL,
  matched_antigen TEXT NOT NULL,
  sibling_link_id TEXT NOT NULL REFERENCES beads(id),
  created_at      TEXT NOT NULL,
  UNIQUE (bead_a, bead_b, matched_antigen)
);
CREATE INDEX idx_sibling_pairs_a ON sibling_pairs(bead_a);
CREATE INDEX idx_sibling_pairs_b ON sibling_pairs(bead_b);
