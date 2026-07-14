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

// ParentEdge is one bead_edges('parent') row, named ChildID/ParentID rather
// than reusing GetEdges' bare []string return shape, since a patient-scoped
// batch (unlike GetEdges' single-beadID query) must report which child each
// parent_id belongs to.
type ParentEdge struct {
	ChildID  string
	ParentID string
}

// GetParentEdgesForPatient returns every 'parent' bead_edges row whose child
// is indexed under patientRoot — one query (JOIN against beads on child_id,
// scoped by patient_root), not one GetEdges(childID) call per Bead in the
// patient's timeline. This is R7a's edge-fetch building block for the graph
// view's vertical axis (specs/R7_graph_view.md: "edges: bead_edges の
// edge_type='parent' のみ"), mirroring GetClinicalLinksForPatient's identical
// "batch, don't N+1" discipline for the horizontal axis.
func (d *DB) GetParentEdgesForPatient(patientRoot string) ([]ParentEdge, error) {
	rows, err := d.sqlDB.Query(`
		SELECT e.child_id, e.parent_id
		FROM bead_edges e
		JOIN beads b ON b.id = e.child_id
		WHERE b.patient_root = ? AND e.edge_type = 'parent'
		ORDER BY e.child_id, e.parent_id`, patientRoot)
	if err != nil {
		return nil, fmt.Errorf("index: get parent edges for patient %s: %w", patientRoot, err)
	}
	defer rows.Close()

	var out []ParentEdge
	for rows.Next() {
		var e ParentEdge
		if err := rows.Scan(&e.ChildID, &e.ParentID); err != nil {
			return nil, fmt.Errorf("index: get parent edges for patient %s: scan: %w", patientRoot, err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("index: get parent edges for patient %s: %w", patientRoot, err)
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

// BeadStatusRow is one bead_status row's read-relevant fields (specs/
// U4_state_derivation.md's §2 resolution outcome, migrations/0006+0007), as
// consulted by retrieve/get_links' status-normalization pass (specs/
// U5_api_retrieve.md's U5b section): whether a Bead is still current
// (Status), and if it was amended, which Bead ID replaces it (CurrentBeadID —
// "" when NULL, i.e. either the retracted case or an amended chain that
// terminates at a retracted leaf; see resolve.go's beadState doc comment).
type BeadStatusRow struct {
	Status        string // "active" | "amended" | "retracted" | "unattested"
	CurrentBeadID string // "" means NULL
}

// BeadStatusFor resolves bead_status.status/current_bead_id for every one of
// the given Bead IDs as a single `WHERE bead_id IN (?,...)` query (mirroring
// PatientRootsFor's placeholder-generation above verbatim — no string
// concatenation of the IDs themselves, only of the fixed "?" placeholder
// count), so a caller checking status for a whole anchor or item batch never
// pays one query per Bead (the same N+1-avoidance discipline as
// PatientRootsFor).
//
// The returned map has one entry per id actually found in bead_status; an id
// absent from this low-level map is not itself a SQL error. The MCP retrieve
// caller distinguishes a completely empty development store (fallback to
// active) from a partial gap in a populated projection (controlled error),
// as required by specs/U5_api_retrieve.md's crux 2 ruling.
//
// # Why this does not join projection_manifest
//
// Unlike a query that wants only "the currently active projection run"'s
// rows, BeadStatusFor intentionally does NOT filter or join by
// projection_manifest.status='active': writePatientState (record_state.go)
// DELETEs all of a patient's old bead_status rows inside the same per-patient
// transaction in which it INSERTs the replacement rows, so at most one
// generation's rows for a given patient_root ever physically exist in this
// table at once — a plain
// bead_id lookup already returns the current generation without needing to
// cross-check projection_manifest at all (peer-confirmed invariant, specs/
// U5_api_retrieve.md's "合意点" #5).
func (d *DB) BeadStatusFor(ids []string) (map[string]BeadStatusRow, error) {
	out := make(map[string]BeadStatusRow, len(ids))
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
		`SELECT bead_id, status, COALESCE(current_bead_id, '') FROM bead_status WHERE bead_id IN (%s)`,
		strings.Join(placeholders, ","),
	)

	rows, err := d.sqlDB.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("index: bead status for %d ids: %w", len(ids), err)
	}
	defer rows.Close()

	for rows.Next() {
		var id string
		var r BeadStatusRow
		if err := rows.Scan(&id, &r.Status, &r.CurrentBeadID); err != nil {
			return nil, fmt.Errorf("index: bead status for %d ids: scan: %w", len(ids), err)
		}
		out[id] = r
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("index: bead status for %d ids: %w", len(ids), err)
	}
	return out, nil
}

// BeadStatusTableEmpty reports whether bead_status has zero rows in total —
// the signal specs/U5_api_retrieve.md's crux 2 ruling uses to distinguish "the
// record_state projector has simply never run on this store" (a dev/fresh
// store, where retrieve should still behave normally rather than exhibiting
// every read as status-filtered-to-nothing) from an individual absent id
// within an otherwise-populated table (see BeadStatusFor's own doc comment on
// how it distinguishes those two cases).
// This is a single COUNT(*) query, called at most once per retrieve/get_links
// call (not per id), so it adds no N+1 risk of its own.
func (d *DB) BeadStatusTableEmpty() (bool, error) {
	var n int
	if err := d.sqlDB.QueryRow(`SELECT COUNT(*) FROM bead_status`).Scan(&n); err != nil {
		return false, fmt.Errorf("index: bead status table empty check: %w", err)
	}
	return n == 0, nil
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

// PatientLinkRow is one clinical_links row scoped to a patient (R7a's
// GetClinicalLinksForPatient), naming BOTH endpoints explicitly (BeadA/BeadB,
// already normalized BeadA < BeadB by the table's own CHECK constraint —
// migrations/0006_projection_v31.sql) rather than resolving an "other" side
// relative to some caller-supplied anchor Bead, the way ClinicalLinkRow does
// for GetClinicalLinks(beadID). A patient-scoped graph view has no single
// anchor Bead to be relative to: it wants every link in the patient's
// bundle as an undirected (bead_a, bead_b) pair, per specs/R7_graph_view.md's
// contract shape (`{"bead_a":..., "bead_b":...}`).
type PatientLinkRow struct {
	LinkID          string
	BeadA           string
	BeadB           string
	Relation        string
	MatchedTag      string
	Severity        string
	EvidenceBasis   string
	EvidenceBeadIDs string // canonical JSON array, stored/returned verbatim (see migrations/0006's comment on this column)
	RuleID          string
	RuleVersion     string
	// ProjectionRunID identifies the projection run that wrote this row. It is
	// the far end of the provenance chain the two-layer design promises: a row
	// names its run, projection_manifest names that run's frozen inputs (the
	// knowledge Bead IDs, code version, config hash), and those knowledge Beads
	// are themselves immutable, content-addressed facts. Without it, an
	// interpretation cannot be traced back to the knowledge that produced it,
	// which is the whole claim.
	ProjectionRunID string
	CreatedAt       string
}

// GetClinicalLinksForPatient returns every clinical_links row whose
// patient_root equals patientRoot, ordered deterministically by created_at
// then matched_tag (mirroring GetClinicalLinks' own tie-break) — the R7a
// "縦=DAG / 横=clinical_links" graph view's per-patient link fetch
// (specs/R7_graph_view.md), a single query using idx_clinical_links_patient_sev
// (patient_root, severity, relation) rather than N calls to GetClinicalLinks
// per Bead in the patient's timeline (the "N 回呼ばない" requirement the spec
// calls out explicitly). Like GetClinicalLinks, this is a plain index-layer
// read: it does not itself apply clearance inheritance or bead_status
// normalization to either endpoint — that is the caller's job (rest package),
// exactly as GetClinicalLinks leaves it to mcpserver.
func (d *DB) GetClinicalLinksForPatient(patientRoot string) ([]PatientLinkRow, error) {
	rows, err := d.sqlDB.Query(`
		SELECT link_id, bead_a, bead_b, relation, matched_tag,
		       severity, evidence_basis, evidence_bead_ids,
		       COALESCE(rule_id, ''), COALESCE(rule_version, ''),
		       COALESCE(projection_run_id, ''), created_at
		FROM clinical_links
		WHERE patient_root = ?
		ORDER BY created_at, matched_tag`, patientRoot)
	if err != nil {
		return nil, fmt.Errorf("index: get clinical links for patient %s: %w", patientRoot, err)
	}
	defer rows.Close()

	var out []PatientLinkRow
	for rows.Next() {
		var r PatientLinkRow
		if err := rows.Scan(&r.LinkID, &r.BeadA, &r.BeadB, &r.Relation, &r.MatchedTag,
			&r.Severity, &r.EvidenceBasis, &r.EvidenceBeadIDs,
			&r.RuleID, &r.RuleVersion, &r.ProjectionRunID, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("index: get clinical links for patient %s: scan: %w", patientRoot, err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("index: get clinical links for patient %s: %w", patientRoot, err)
	}
	return out, nil
}

// GraphBeadRow is one Bead's worth of R7a graph-view fields: BeadRef's
// identifying/location columns are not needed here (the graph view never
// opens a Pod frame directly — the caller resolves full Content via
// engine.GetBead only for Beads that survive clearance, same as every other
// REST handler), so this is a narrower, purpose-built row rather than an
// extension of BeadRef. RecordedAt is "" when NULL (a pre-U3 Bead this store
// has never reprojected — see migrations/0006's comment on recorded_at
// starting NULL for existing rows). Status/CurrentBeadID are LEFT JOINed
// from bead_status and are the zero value ("") when absent, mirroring
// BeadStatusFor's own "absent = active" convention (the caller applies that
// fallback, not this query).
type GraphBeadRow struct {
	ID            string
	Type          string
	Timestamp     string
	RecordedAt    string
	Summary       string
	Status        string
	CurrentBeadID string
}

// ListPatientBeadsForGraph returns every Bead indexed under patientRoot,
// ordered by timestamp (matching ListPatientBeads' own order — R7a's
// contract wants beads[] "timestamp 昇順"), with RecordedAt and its
// bead_status fields (Status/CurrentBeadID) already joined in — one query,
// not ListPatientBeads followed by a separate per-patient BeadStatusFor
// batch, since a LEFT JOIN here costs the same one round trip either way and
// keeps R7a's handler from having to build its own id-keyed map afterward.
// A Bead with no bead_status row (reproject never ran, or ran before this
// Bead existed) gets Status="" / CurrentBeadID="" — the caller (rest
// package's handleGraph) is responsible for applying the same "absent =
// active" fallback specs/U5_api_retrieve.md's U5b section establishes,
// exactly as BeadStatusFor's own callers do.
func (d *DB) ListPatientBeadsForGraph(patientRoot string) ([]GraphBeadRow, error) {
	if patientRoot == "" {
		return nil, fmt.Errorf("index: list patient beads for graph: patientRoot must not be empty")
	}
	rows, err := d.sqlDB.Query(`
		SELECT b.id, b.type, b.timestamp, COALESCE(b.recorded_at, ''), COALESCE(b.summary, ''),
		       COALESCE(s.status, ''), COALESCE(s.current_bead_id, '')
		FROM beads b
		LEFT JOIN bead_status s ON s.bead_id = b.id
		WHERE b.patient_root = ?
		ORDER BY b.timestamp, b.id`, patientRoot)
	if err != nil {
		return nil, fmt.Errorf("index: list patient beads for graph %s: %w", patientRoot, err)
	}
	defer rows.Close()

	var out []GraphBeadRow
	for rows.Next() {
		var r GraphBeadRow
		if err := rows.Scan(&r.ID, &r.Type, &r.Timestamp, &r.RecordedAt, &r.Summary,
			&r.Status, &r.CurrentBeadID); err != nil {
			return nil, fmt.Errorf("index: list patient beads for graph %s: scan: %w", patientRoot, err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("index: list patient beads for graph %s: %w", patientRoot, err)
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
