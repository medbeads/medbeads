package index

import (
	"database/sql"
	"fmt"
	"time"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
)

// EmbedDim is the embedding vector width this package's index.db schema is
// frozen to (migrations/0004_embed.sql's `embedding FLOAT[768]` column,
// cl-nagoya/ruri-v3-310m's native output dimension — docs/requirements.md
// R4.2's default embed model). SerializeEmbedding/SemanticSearch both
// reject a vector of any other length rather than silently truncating or
// zero-padding it: a wrong-dimension vector is a caller bug (an embedder
// misconfigured relative to this schema), not something to paper over here
// — see 0004_embed.sql's own doc comment on why a model/dimension change is
// a new migration, never a runtime coercion.
const EmbedDim = 768

// nowRFC3339 is bead_embed_queue.enqueued_at's wall-clock source, a var
// (not a direct time.Now() call) so tests can override it — mirrors package
// apc's identical nowRFC3339 var (internal/engine/apc/link.go).
var nowRFC3339 = func() string { return time.Now().UTC().Format(time.RFC3339) }

// EnqueueEmbed records that beadID (search text searchText, under
// patientRoot — "" for the shared Pod) needs an embedding computed, by
// upserting a bead_embed_queue row. Called by IndexBead for every freshly-
// indexed Bead (see write.go), including during Reindex/CatchUp's replay of
// every Bead in the store from the Pods — so a full Reindex naturally
// re-populates bead_embed_queue for every Bead as a side effect of its own
// per-Bead IndexBead calls, satisfying the lead's "queue を全 Bead で再充填"
// decision (docs/requirements.md R4.2) without a separate bulk-refill step.
// This is idempotent under replay by design: ON CONFLICT (bead_id) DO
// UPDATE overwrites search_text/patient_root/enqueued_at rather than
// erroring, so a Bead that already has a current, correct embedding in
// bead_embed (untouched by this call — EnqueueEmbed only ever writes to
// bead_embed_queue) being briefly re-queued for a redundant re-embed after a
// Reindex is wasted embedder work, not a correctness bug (the indexer's
// UpsertEmbedAndDequeue overwrite is itself idempotent — see indexer.go).
// This is exactly the tradeoff the lead decision explicitly accepts:
// "reindex 往復のカウント検査対象外であることをコメント明記" — this is that
// comment; a Reindex round-trip test must not assert bead_embed_queue's
// post-Reindex depth equals its pre-Reindex depth, since Reindex always
// re-enqueues every Bead regardless of whether it was already embedded.
func EnqueueEmbed(tx *sql.Tx, beadID, patientRoot, searchText string) error {
	if _, err := tx.Exec(`
		INSERT INTO bead_embed_queue (bead_id, patient_root, search_text, enqueued_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (bead_id) DO UPDATE SET
			patient_root = excluded.patient_root,
			search_text  = excluded.search_text,
			enqueued_at  = excluded.enqueued_at`,
		beadID, patientRoot, searchText, nowRFC3339(),
	); err != nil {
		return fmt.Errorf("index: enqueue embed %s: %w", beadID, err)
	}
	return nil
}

// EmbedQueueItem is one row of pending embedding work (bead_embed_queue),
// enough for a caller (the async indexer) to build an embedder request and,
// on success, write the result to bead_embed without a second query.
type EmbedQueueItem struct {
	BeadID      string
	PatientRoot string // "" = shared Pod
	SearchText  string
}

// DequeueEmbedBatch returns up to limit pending bead_embed_queue rows,
// oldest-enqueued first (FIFO — so a queue that briefly exceeds the
// indexer's per-batch throughput still drains in enqueue order rather than
// starving old entries indefinitely). It does not remove the returned rows;
// the caller (the async indexer) removes each one via UpsertEmbedAndDequeue
// only after successfully writing its embedding to bead_embed, within the
// same transaction (see indexer.go's drainBatch) — a row must never be
// removed from the queue before its embedding is durably recorded, or a
// crash between the two would silently lose that Bead's embedding forever
// with no way to notice (unlike Ingest's Pod-then-index ordering, there is
// no separate "source of truth" for an embedding to recover it from).
func (d *DB) DequeueEmbedBatch(limit int) ([]EmbedQueueItem, error) {
	if limit <= 0 {
		limit = 64
	}
	rows, err := d.sqlDB.Query(`
		SELECT bead_id, patient_root, search_text
		FROM bead_embed_queue
		ORDER BY enqueued_at, bead_id
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("index: dequeue embed batch: %w", err)
	}
	defer rows.Close()

	var out []EmbedQueueItem
	for rows.Next() {
		var item EmbedQueueItem
		if err := rows.Scan(&item.BeadID, &item.PatientRoot, &item.SearchText); err != nil {
			return nil, fmt.Errorf("index: dequeue embed batch: scan: %w", err)
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("index: dequeue embed batch: %w", err)
	}
	return out, nil
}

// EmbedQueueDepth reports how many bead_embed_queue rows are currently
// pending, for diagnostics/tests (e.g. asserting a drain actually emptied
// the queue, or that an embedder outage leaves rows queued rather than
// dropping them).
func (d *DB) EmbedQueueDepth() (int, error) {
	var n int
	if err := d.sqlDB.QueryRow(`SELECT COUNT(*) FROM bead_embed_queue`).Scan(&n); err != nil {
		return 0, fmt.Errorf("index: embed queue depth: %w", err)
	}
	return n, nil
}

// SerializeEmbedding converts vec into the little-endian float32 BLOB
// sqlite-vec's vec0 columns store (sqlite_vec.SerializeFloat32), rejecting
// any vector whose length is not exactly EmbedDim — see EmbedDim's doc
// comment on why this is a hard error, not silent truncation/padding.
func SerializeEmbedding(vec []float32) ([]byte, error) {
	if len(vec) != EmbedDim {
		return nil, fmt.Errorf("index: serialize embedding: got %d dims, want %d", len(vec), EmbedDim)
	}
	blob, err := sqlite_vec.SerializeFloat32(vec)
	if err != nil {
		return nil, fmt.Errorf("index: serialize embedding: %w", err)
	}
	return blob, nil
}

// UpsertEmbedAndDequeue writes beadID's embedding to the bead_embed vec0
// table and removes its bead_embed_queue row, in the caller-supplied
// transaction — the one atomic unit indexer.go's drainBatch commits per
// batch, so a crash mid-batch never leaves a Bead's queue row deleted
// without its embedding durably written (or vice versa: an embedding
// written but the queue row still present, which would just mean the next
// drain redundantly re-embeds it — harmless, unlike the reverse).
//
// vec0 has no native UPSERT (INSERT ... ON CONFLICT is not supported on a
// vec0 virtual table's own PRIMARY KEY the way it is on an ordinary table —
// verified directly: attempting one against this schema returns "ON
// CONFLICT clause does not match any PRIMARY KEY or UNIQUE constraint"),
// so this deletes any prior row for beadID first (a re-embed after a
// Reindex-triggered re-queue, or an operator-forced re-embed, is the only
// case where one would already exist) and then inserts fresh — both
// statements are idempotent-safe within this same transaction regardless of
// whether a prior row existed.
func UpsertEmbedAndDequeue(tx *sql.Tx, beadID, patientRoot string, embedding []byte) error {
	if _, err := tx.Exec(`DELETE FROM bead_embed WHERE bead_id = ?`, beadID); err != nil {
		return fmt.Errorf("index: upsert embed %s: delete prior: %w", beadID, err)
	}
	if _, err := tx.Exec(
		`INSERT INTO bead_embed (bead_id, patient_root, embedding) VALUES (?, ?, ?)`,
		beadID, patientRoot, embedding,
	); err != nil {
		return fmt.Errorf("index: upsert embed %s: insert: %w", beadID, err)
	}
	if _, err := tx.Exec(`DELETE FROM bead_embed_queue WHERE bead_id = ?`, beadID); err != nil {
		return fmt.Errorf("index: upsert embed %s: dequeue: %w", beadID, err)
	}
	return nil
}

// SemanticResult is one SemanticSearch hit: a Bead ID plus its vector
// distance from the query embedding (lower = more similar; vec0's default
// metric is L2/Euclidean distance over the raw stored vectors — see
// SemanticSearch's doc comment). patientRoot is echoed back so a caller
// merging semantic hits with FTS anchors (retrieve, R6.2) does not need a
// second lookup to scope-check each hit.
type SemanticResult struct {
	BeadID      string
	PatientRoot string // "" = shared Pod
	Distance    float64
}

// SemanticSearch runs a vec0 K-nearest-neighbor query against bead_embed for
// queryEmbedding (already serialized via SerializeEmbedding), returning the
// k closest Beads ordered by ascending distance. If patientRoot is non-
// empty, the query is scoped to that patient via vec0's native
// PARTITION KEY pre-filter (migrations/0004_embed.sql's `patient_root TEXT
// PARTITION KEY`) — WHERE patient_root = ? alongside the MATCH/k clause,
// which vec0 evaluates as a partition restriction *before* the KNN search
// rather than as a post-hoc filter over a global top-k (DESIGN §6's
// "patient_root で pre-filtering ネイティブ対応"), so a patient-scoped query's
// cost scales with that patient's own embedding count, not the whole
// store's. An empty patientRoot searches every partition (every Bead in
// bead_embed, patient-scoped or shared alike) — retrieve/rag_search's
// "no patient_id given" case.
//
// index.go/embed.go deliberately have no dependency on any embedder HTTP
// client (see the lead's "embedder を index 層に持ち込まない" decision,
// docs/requirements.md R4.2): SemanticSearch's caller is responsible for
// turning a query string into queryEmbedding before calling this.
func (d *DB) SemanticSearch(queryEmbedding []byte, k int, patientRoot string) ([]SemanticResult, error) {
	if k <= 0 {
		k = 10
	}

	var rows *sql.Rows
	var err error
	if patientRoot != "" {
		rows, err = d.sqlDB.Query(`
			SELECT bead_id, patient_root, distance
			FROM bead_embed
			WHERE embedding MATCH ? AND patient_root = ? AND k = ?
			ORDER BY distance`, queryEmbedding, patientRoot, k)
	} else {
		rows, err = d.sqlDB.Query(`
			SELECT bead_id, patient_root, distance
			FROM bead_embed
			WHERE embedding MATCH ? AND k = ?
			ORDER BY distance`, queryEmbedding, k)
	}
	if err != nil {
		return nil, fmt.Errorf("index: semantic search: %w", err)
	}
	defer rows.Close()

	var out []SemanticResult
	for rows.Next() {
		var r SemanticResult
		if err := rows.Scan(&r.BeadID, &r.PatientRoot, &r.Distance); err != nil {
			return nil, fmt.Errorf("index: semantic search: scan: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("index: semantic search: %w", err)
	}
	return out, nil
}
