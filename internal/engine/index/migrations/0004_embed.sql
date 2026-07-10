-- Migration 0004: L2 semantic search state (specs/DESIGN_v3.md §6's L2
-- Semantic paragraph, docs/requirements.md R4.2).
--
-- Two pieces of new state:
--
--  1. bead_embed_queue: the async-indexer work queue. IndexBead enqueues
--     every Bead's (id, patient_root, search_text) here unconditionally —
--     regardless of whether an embedder is even configured for this process
--     (queueing is cheap; StartEmbedIndexer is what is conditional, gated on
--     -embedder being set at `serve` time). A row is deleted only once its
--     embedding has actually been written to bead_embed (see write.go's
--     EnqueueEmbed and the indexer's drainBatch). enqueued_at is kept for
--     future queue-age observability (not read by this unit's own code, but
--     cheap to have and a natural audit field for a queue table).
--
--  2. bead_embed: the sqlite-vec vec0 virtual table storing each Bead's
--     embedding vector, partitioned by patient_root per DESIGN §6 ("patient_root
--     で pre-filtering ネイティブ対応") so a semantic search scoped to one
--     patient can use vec0's native partition-key pre-filter rather than a
--     post-hoc filter over a global KNN scan. patient_root is TEXT and NOT
--     NULL: shared-Pod Beads (patient_root NULL in the `beads` table) are
--     stored here with patient_root = '' (vec0 partition-key columns do not
--     support NULL the same way an ordinary column would for this
--     project's "NULL = shared" convention elsewhere — see write.go's
--     EnqueueEmbed/indexer.go's embedRow for the '' <-> NULL translation at
--     the boundary).
--
-- # Embedding dimension is frozen at 768 in this migration
--
-- vec0's `embedding FLOAT[768]` column width is baked into the CREATE
-- VIRTUAL TABLE statement itself (sqlite-vec has no ALTER-column-width
-- path); 768 is cl-nagoya/ruri-v3-310m's native output dimension, this
-- project's lead-decided default embedding model (see docs/requirements.md
-- R4.2, cmd/medbeadsd's -embed-model/-embedder serve flags). If the
-- embedding model ever changes to a different output dimension, that is a
-- NEW migration (e.g. 0005_embed_v2.sql) that creates a differently-named
-- vec0 table (or drops+recreates this one behind an explicit operator
-- decision) — never an edit to this file: migrations are append-only, and a
-- silently-changed vector width here would corrupt every previously-written
-- embedding row's geometry without any schema-version signal that it
-- happened.
CREATE TABLE bead_embed_queue (
  bead_id      TEXT PRIMARY KEY REFERENCES beads(id),
  patient_root TEXT NOT NULL,   -- '' = shared Pod (see doc comment above)
  search_text  TEXT NOT NULL,
  enqueued_at  TEXT NOT NULL
);

CREATE VIRTUAL TABLE bead_embed USING vec0(
  bead_id TEXT PRIMARY KEY,
  patient_root TEXT PARTITION KEY,
  embedding FLOAT[768]
);
