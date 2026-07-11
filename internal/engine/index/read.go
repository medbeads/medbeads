package index

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// BeadRef is one row's worth of location + identifying info from beads,
// enough for a caller to open the owning Pod and read the frame directly
// (pod.Reader.ReadAt(Offset)) without a second query.
type BeadRef struct {
	ID          string
	PatientRoot string // "" for the shared Pod
	Type        string
	Timestamp   string
	PodPath     string
	Offset      int64
	Length      int64
	Summary     string
}

// GetBead resolves a single Bead's storage location (pod_id -> path,
// offset, length) by ID, joining pods once so the caller gets a ready-to-use
// PodPath instead of a bare pod_id (R3 "読み取り API" scope). Returns
// ErrNotFound if no bead with that ID is indexed.
func (d *DB) GetBead(id string) (BeadRef, error) {
	row := d.sqlDB.QueryRow(`
		SELECT b.id, COALESCE(b.patient_root, ''), b.type, b.timestamp,
		       p.path, b.offset, b.length, COALESCE(b.summary, '')
		FROM beads b
		JOIN pods p ON p.pod_id = b.pod_id
		WHERE b.id = ?`, id)

	var ref BeadRef
	err := row.Scan(&ref.ID, &ref.PatientRoot, &ref.Type, &ref.Timestamp,
		&ref.PodPath, &ref.Offset, &ref.Length, &ref.Summary)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return BeadRef{}, fmt.Errorf("index: get bead %s: %w", id, ErrNotFound)
	case err != nil:
		return BeadRef{}, fmt.Errorf("index: get bead %s: %w", id, err)
	}
	return ref, nil
}

// ListPatientBeads returns every Bead indexed under patientRoot, ordered by
// timestamp (idx_beads_root's (patient_root, timestamp) composite index —
// specs/DESIGN_v3.md §5), for the timeline / patient-bundle use case.
// patientRoot must be non-empty; use ListSharedBeads for the shared Pod.
func (d *DB) ListPatientBeads(patientRoot string) ([]BeadRef, error) {
	if patientRoot == "" {
		return nil, fmt.Errorf("index: list patient beads: patientRoot must not be empty (use ListSharedBeads)")
	}
	return d.queryBeadRefs(`
		SELECT b.id, COALESCE(b.patient_root, ''), b.type, b.timestamp,
		       p.path, b.offset, b.length, COALESCE(b.summary, '')
		FROM beads b
		JOIN pods p ON p.pod_id = b.pod_id
		WHERE b.patient_root = ?
		ORDER BY b.timestamp, b.id`, patientRoot)
}

// ListSharedBeads returns every Bead indexed with no patient_root (stored in
// the shared Pod), ordered by timestamp.
func (d *DB) ListSharedBeads() ([]BeadRef, error) {
	return d.queryBeadRefs(`
		SELECT b.id, COALESCE(b.patient_root, ''), b.type, b.timestamp,
		       p.path, b.offset, b.length, COALESCE(b.summary, '')
		FROM beads b
		JOIN pods p ON p.pod_id = b.pod_id
		WHERE b.patient_root IS NULL
		ORDER BY b.timestamp, b.id`)
}

// ListPatients returns every Bead of type 'patient_registration' — one row
// per patient, each Bead's own ID doubling as its patient_root (Ingest's
// resolvePatientRoot makes a registration Bead its own root) — ordered by
// timestamp descending (most recently registered patient first), mirroring
// v2.2.0's core/store.GetPatients. This is the "v2 GetPatients の移植先"
// index addition specs/DESIGN_v3.md §6 / docs/requirements.md R4.3 calls for:
// a caller of package graph's LoadBundle/BuildContext (e.g. a future
// list_patients MCP tool) needs to enumerate patients before it can load any
// one patient's Bundle.
func (d *DB) ListPatients() ([]BeadRef, error) {
	return d.queryBeadRefs(`
		SELECT b.id, COALESCE(b.patient_root, ''), b.type, b.timestamp,
		       p.path, b.offset, b.length, COALESCE(b.summary, '')
		FROM beads b
		JOIN pods p ON p.pod_id = b.pod_id
		WHERE b.type = 'patient_registration'
		ORDER BY b.timestamp DESC, b.id`)
}

// SearchResult is one FTS hit, already resolved to its patient_root so
// callers never need a second per-hit query (specs/DESIGN_v3.md §5:
// "findPatientRoot 廃止" — patient resolution is a single JOIN here, not a
// per-Bead lookup).
type SearchResult struct {
	BeadID      string
	PatientRoot string // "" for the shared Pod
	Type        string
	Timestamp   string
	Summary     string
}

// Search runs an FTS5 trigram query against beads_fts.search_text and
// resolves every hit's patient_root via a single JOIN against beads
// (specs/DESIGN_v3.md §5 / R3.3, R4.1's "JOIN 1回で患者集約" principle
// applied at the anchor-search level). Results are ordered by FTS bm25 rank
// (best match first).
func (d *DB) Search(query string, limit int) ([]SearchResult, error) {
	if limit <= 0 {
		limit = 50
	}
	// beads_fts is a contentless FTS5 table (content=''): it has no id
	// column (see migrations/0001_init.sql), so a hit is resolved back to
	// its Bead via rowid, not a stored id — the single JOIN specs/
	// DESIGN_v3.md §5 calls for ("JOIN 1回で患者集約"), just keyed on
	// rowid.
	rows, err := d.sqlDB.Query(`
		SELECT b.id, COALESCE(b.patient_root, ''), b.type, b.timestamp, COALESCE(b.summary, '')
		FROM beads_fts f
		JOIN beads b ON b.rowid = f.rowid
		WHERE beads_fts MATCH ?
		ORDER BY bm25(beads_fts)
		LIMIT ?`, query, limit)
	if err != nil {
		return nil, fmt.Errorf("index: search %q: %w", query, err)
	}
	defer rows.Close()

	var out []SearchResult
	for rows.Next() {
		var r SearchResult
		if err := rows.Scan(&r.BeadID, &r.PatientRoot, &r.Type, &r.Timestamp, &r.Summary); err != nil {
			return nil, fmt.Errorf("index: search %q: scan: %w", query, err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("index: search %q: %w", query, err)
	}
	return out, nil
}

// queryBeadRefs runs a query expected to return the 8-column BeadRef shape
// and streams results with rows.Next() rather than reading the whole result
// set into memory up front at the driver layer (the []BeadRef this returns
// is still fully materialized for the caller, but per-row scanning avoids
// holding the underlying driver cursor/buffers longer than necessary).
func (d *DB) queryBeadRefs(query string, args ...any) ([]BeadRef, error) {
	rows, err := d.sqlDB.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("index: query bead refs: %w", err)
	}
	defer rows.Close()

	var out []BeadRef
	for rows.Next() {
		var ref BeadRef
		if err := rows.Scan(&ref.ID, &ref.PatientRoot, &ref.Type, &ref.Timestamp,
			&ref.PodPath, &ref.Offset, &ref.Length, &ref.Summary); err != nil {
			return nil, fmt.Errorf("index: query bead refs: scan: %w", err)
		}
		out = append(out, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("index: query bead refs: %w", err)
	}
	return out, nil
}

// GetEdges returns the parent_id values of every 'parent' edge whose
// child_id is beadID (i.e. beadID's direct parents).
func (d *DB) GetEdges(beadID string) ([]string, error) {
	rows, err := d.sqlDB.Query(
		`SELECT parent_id FROM bead_edges WHERE child_id = ? AND edge_type = 'parent'`, beadID)
	if err != nil {
		return nil, fmt.Errorf("index: get edges %s: %w", beadID, err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var parent string
		if err := rows.Scan(&parent); err != nil {
			return nil, fmt.Errorf("index: get edges %s: scan: %w", beadID, err)
		}
		out = append(out, parent)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("index: get edges %s: %w", beadID, err)
	}
	return out, nil
}

// GetTags returns every tag attached to beadID (bead_tags — bead_antigens'
// successor, specs/U2_projection_schema.md / U3a).
func (d *DB) GetTags(beadID string) ([]string, error) {
	rows, err := d.sqlDB.Query(
		`SELECT tag FROM bead_tags WHERE bead_id = ? ORDER BY tag`, beadID)
	if err != nil {
		return nil, fmt.Errorf("index: get tags %s: %w", beadID, err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			return nil, fmt.Errorf("index: get tags %s: scan: %w", beadID, err)
		}
		out = append(out, tag)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("index: get tags %s: %w", beadID, err)
	}
	return out, nil
}

// PatientRootsFor resolves the patient_root each of the given Bead IDs was
// indexed under, as a single IN (...) query rather than one query per ID —
// this is the "N+1 の根治" building block specs/DESIGN_v3.md §3 calls for at
// write time: a new Bead's patient_root is derived from its parents' already-
// indexed patient_root, and a multi-parent Bead must not cost one query per
// parent to find out.
//
// The returned map has one entry per id found in beads (ids not yet indexed
// are simply absent, not an error — the engine layer's ingest protocol
// interprets an absent parent as "unknown parent", a distinct condition it
// checks separately). A value of "" means that id was indexed with a NULL
// (shared-pod) patient_root.
func (d *DB) PatientRootsFor(ids []string) (map[string]string, error) {
	out := make(map[string]string, len(ids))
	if len(ids) == 0 {
		return out, nil
	}

	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	query := fmt.Sprintf(
		`SELECT id, COALESCE(patient_root, '') FROM beads WHERE id IN (%s)`,
		strings.Join(placeholders, ","),
	)

	rows, err := d.sqlDB.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("index: patient roots for %d ids: %w", len(ids), err)
	}
	defer rows.Close()

	for rows.Next() {
		var id, root string
		if err := rows.Scan(&id, &root); err != nil {
			return nil, fmt.Errorf("index: patient roots for %d ids: scan: %w", len(ids), err)
		}
		out[id] = root
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("index: patient roots for %d ids: %w", len(ids), err)
	}
	return out, nil
}

// ClinicalLinkRow is one clinical_links row (specs/U2_projection_schema.md /
// migrations/0006_projection_v31.sql), as read back for a single Bead:
// OtherBeadID is whichever of bead_a/bead_b is not the Bead the caller
// queried by (mirroring GetClinicalLinks' own doc comment on how the query
// resolves it), so callers do not need to re-derive it from BeadA/BeadB
// themselves.
type ClinicalLinkRow struct {
	LinkID          string
	OtherBeadID     string
	PatientRoot     string
	Relation        string
	MatchedTag      string
	Severity        string
	EvidenceBasis   string
	EvidenceBeadIDs string // canonical JSON array, stored/returned verbatim (see migrations/0006's comment on this column)
	RuleID          string
	RuleVersion     string
	ProjectionRunID string
	CreatedAt       string
}

// GetClinicalLinks returns every clinical_links row naming beadID as either
// bead_a or bead_b (specs/U3_link_projector.md's U3c read-side: "get_links
// … clinical_links 読取"), ordered deterministically by created_at then
// matched_tag. This is a plain index-layer read: it does not itself apply
// clearance inheritance (dropping a row whose other Bead is inaccessible) —
// that is the caller's job (mcpserver's get_links tool), exactly as
// GetTags/GetEdges are unfiltered building blocks and mcpserver applies
// clearance.FilterByAccess on top, per this package's existing division of
// responsibility between "resolve rows" (here) and "decide visibility"
// (mcpserver + package clearance).
func (d *DB) GetClinicalLinks(beadID string) ([]ClinicalLinkRow, error) {
	rows, err := d.sqlDB.Query(`
		SELECT link_id, bead_a, bead_b, patient_root, relation, matched_tag,
		       severity, evidence_basis, evidence_bead_ids,
		       COALESCE(rule_id, ''), COALESCE(rule_version, ''),
		       COALESCE(projection_run_id, ''), created_at
		FROM clinical_links
		WHERE bead_a = ? OR bead_b = ?
		ORDER BY created_at, matched_tag`, beadID, beadID)
	if err != nil {
		return nil, fmt.Errorf("index: get clinical links %s: %w", beadID, err)
	}
	defer rows.Close()

	var out []ClinicalLinkRow
	for rows.Next() {
		var r ClinicalLinkRow
		var beadA, beadB string
		if err := rows.Scan(&r.LinkID, &beadA, &beadB, &r.PatientRoot, &r.Relation, &r.MatchedTag,
			&r.Severity, &r.EvidenceBasis, &r.EvidenceBeadIDs,
			&r.RuleID, &r.RuleVersion, &r.ProjectionRunID, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("index: get clinical links %s: scan: %w", beadID, err)
		}
		r.OtherBeadID = beadA
		if beadA == beadID {
			r.OtherBeadID = beadB
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("index: get clinical links %s: %w", beadID, err)
	}
	return out, nil
}

// PodWatermark reports a Pod's current indexed_upto watermark (R1.3), or
// ErrNotFound if podPath has no pods row yet.
func (d *DB) PodWatermark(podPath string) (int64, error) {
	var upto int64
	err := d.sqlDB.QueryRow(`SELECT indexed_upto FROM pods WHERE path = ?`, podPath).Scan(&upto)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return 0, fmt.Errorf("index: pod watermark %s: %w", podPath, ErrNotFound)
	case err != nil:
		return 0, fmt.Errorf("index: pod watermark %s: %w", podPath, err)
	}
	return upto, nil
}
