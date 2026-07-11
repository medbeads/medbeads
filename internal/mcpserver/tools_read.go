package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/medbeads/medbeads/internal/engine/bead"
	"github.com/medbeads/medbeads/internal/engine/clearance"
	"github.com/medbeads/medbeads/internal/engine/graph"
	"github.com/medbeads/medbeads/internal/engine/pod"
)

// registerReadTools adds every read-only tool (list_patients, search_beads,
// get_bead, get_context, get_timeline, get_links, search_antigens,
// verify_integrity, retrieve) to s.mcp. This is called unconditionally by
// New, regardless of role — clearance filtering (not tool registration) is
// what limits what a non-system role actually sees, per the task's "system
// はバイパス" / viewer-is-filtered design. create_bead is deliberately NOT
// here: it durably ingests Beads (a write), so it is registered by
// registerWriteTools instead — see that function's doc comment.
func (s *Server) registerReadTools() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "list_patients",
		Description: "List every patient (patient_registration Bead), most recently registered first.",
	}, s.listPatients)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "search_beads",
		Description: "FTS5 trigram search over Bead search_text, optionally scoped to one patient. " +
			"Returns beadRefs (no full content) ranked by match quality.",
	}, s.searchBeads)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "get_bead",
		Description: "Fetch one Bead's full, hash-verified content by ID (accepts sha256:-prefixed or plain hex).",
	}, s.getBead)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "get_context",
		Description: "Ancestor BFS from one Bead within its patient bundle, up to max_depth parent-hops " +
			"(id itself is included at depth 0).",
	}, s.getContext)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "get_timeline",
		Description: "Every Bead indexed under a patient_root, ordered by timestamp.",
	}, s.getTimeline)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "search_antigens",
		Description: "Inverted-index lookup: every Bead tagged with the given antigen (bead_tags).",
	}, s.searchAntigens)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "get_links",
		Description: "The clinical_links rows (relation/severity/evidence_basis/matched_tag/rule_version " +
			"plus the other Bead's ID) for one Bead — the U3b-projected clinical_links table, the sole " +
			"link mechanism since U5a removed the old sibling_link/sibling_pairs apparatus. Clearance is " +
			"inherited: a link whose other endpoint is inaccessible to this session's role is dropped " +
			"entirely, not masked.",
	}, s.getLinks)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "verify_integrity",
		Description: "Run pod.VerifyAll over every Pod file in the data directory: CRC + self-hash verification.",
	}, s.verifyIntegrity)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "retrieve",
		Description: "Single-round-trip agent retrieval: FTS/structured (+ optional L2 semantic vector) " +
			"anchor -> patient resolution -> graph expansion -> token-budgeted context bundle with " +
			"provenance. semantic=true requires this server to have an embedder configured (serve's " +
			"-embedder flag), or it is a tool-level error.",
	}, s.retrieve)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "rag_search",
		Description: "Experimental pure-vector-search baseline (no FTS, no antigen filter, no graph/chain " +
			"expansion): embed query, return the k nearest Beads' full L0 content plus vector distance. " +
			"Requires this server to have an embedder configured (serve's -embedder flag), or it is a " +
			"tool-level error. For agent retrieval, prefer retrieve instead.",
	}, s.ragSearch)
}

// --- list_patients -------------------------------------------------------

type listPatientsIn struct{}

type listPatientsOut struct {
	Patients []beadRefView `json:"patients"`
}

func (s *Server) listPatients(_ context.Context, _ *mcp.CallToolRequest, _ listPatientsIn) (*mcp.CallToolResult, listPatientsOut, error) {
	refs, err := s.eng.Index().ListPatients()
	if err != nil {
		res, jerr := toolError("list_patients", err)
		return res, listPatientsOut{}, jerr
	}

	beads := make([]bead.Bead, 0, len(refs))
	for _, ref := range refs {
		b, err := s.eng.GetBead(ref.ID)
		if err != nil {
			res, jerr := toolError("list_patients: get_bead "+ref.ID, err)
			return res, listPatientsOut{}, jerr
		}
		beads = append(beads, b)
	}
	filtered, err := clearance.FilterByAccess(s.eng.Index(), beads, s.viewerRoles())
	if err != nil {
		res, jerr := toolError("list_patients: filter", err)
		return res, listPatientsOut{}, jerr
	}

	var out listPatientsOut
	for i, b := range filtered {
		if !accessible(b) {
			continue
		}
		out.Patients = append(out.Patients, newBeadRefView(b, refs[i].PatientRoot, refs[i].Summary))
	}
	return nil, out, nil
}

// --- search_beads ---------------------------------------------------------

type searchBeadsIn struct {
	Query     string `json:"query" jsonschema:"FTS5 trigram query text"`
	PatientID string `json:"patient_id,omitempty" jsonschema:"restrict results to this patient (sha256: prefix optional)"`
	Limit     int    `json:"limit,omitempty" jsonschema:"max results (default 50)"`
}

type searchBeadsOut struct {
	Results []beadRefView `json:"results"`
}

func (s *Server) searchBeads(_ context.Context, _ *mcp.CallToolRequest, in searchBeadsIn) (*mcp.CallToolResult, searchBeadsOut, error) {
	if in.Query == "" {
		res, jerr := toolError("search_beads", fmt.Errorf("query must not be empty"))
		return res, searchBeadsOut{}, jerr
	}

	var patientRoot string
	if in.PatientID != "" {
		root, err := bead.ParseID(in.PatientID)
		if err != nil {
			res, jerr := toolError("search_beads: parse patient_id", err)
			return res, searchBeadsOut{}, jerr
		}
		patientRoot = root
	}

	hits, err := s.eng.Index().Search(in.Query, in.Limit)
	if err != nil {
		res, jerr := toolError("search_beads", err)
		return res, searchBeadsOut{}, jerr
	}

	beads := make([]bead.Bead, 0, len(hits))
	refIndex := make([]int, 0, len(hits))
	for i, hit := range hits {
		if patientRoot != "" && hit.PatientRoot != patientRoot {
			continue
		}
		b, err := s.eng.GetBead(hit.BeadID)
		if err != nil {
			res, jerr := toolError("search_beads: get_bead "+hit.BeadID, err)
			return res, searchBeadsOut{}, jerr
		}
		beads = append(beads, b)
		refIndex = append(refIndex, i)
	}
	filtered, err := clearance.FilterByAccess(s.eng.Index(), beads, s.viewerRoles())
	if err != nil {
		res, jerr := toolError("search_beads: filter", err)
		return res, searchBeadsOut{}, jerr
	}

	var out searchBeadsOut
	for i, b := range filtered {
		if !accessible(b) {
			continue
		}
		hit := hits[refIndex[i]]
		out.Results = append(out.Results, newBeadRefView(b, hit.PatientRoot, hit.Summary))
	}
	return nil, out, nil
}

// --- get_bead ---------------------------------------------------------

type getBeadIn struct {
	ID string `json:"id" jsonschema:"Bead ID (sha256: prefix optional)"`
}

type getBeadOut struct {
	Bead beadView `json:"bead"`
}

func (s *Server) getBead(_ context.Context, _ *mcp.CallToolRequest, in getBeadIn) (*mcp.CallToolResult, getBeadOut, error) {
	id, err := bead.ParseID(in.ID)
	if err != nil {
		res, jerr := toolError("get_bead: parse id", err)
		return res, getBeadOut{}, jerr
	}

	b, err := s.eng.GetBead(id)
	if err != nil {
		res, jerr := toolError("get_bead", err)
		return res, getBeadOut{}, jerr
	}

	filtered, err := clearance.FilterByAccess(s.eng.Index(), []bead.Bead{b}, s.viewerRoles())
	if err != nil {
		res, jerr := toolError("get_bead: filter", err)
		return res, getBeadOut{}, jerr
	}

	return nil, getBeadOut{Bead: newBeadView(filtered[0])}, nil
}

// --- get_context (ancestor BFS) -------------------------------------------

type getContextIn struct {
	ID       string `json:"id" jsonschema:"Bead ID to walk ancestors from (sha256: prefix optional)"`
	MaxDepth int    `json:"max_depth,omitempty" jsonschema:"max parent-hops to walk (default 5)"`
}

type getContextOut struct {
	Ancestors []beadView `json:"ancestors"`
}

func (s *Server) getContext(_ context.Context, _ *mcp.CallToolRequest, in getContextIn) (*mcp.CallToolResult, getContextOut, error) {
	id, err := bead.ParseID(in.ID)
	if err != nil {
		res, jerr := toolError("get_context: parse id", err)
		return res, getContextOut{}, jerr
	}
	depth := in.MaxDepth
	if depth <= 0 {
		depth = 5
	}

	bd, err := s.loadBundleForBead(id)
	if err != nil {
		res, jerr := toolError("get_context", err)
		return res, getContextOut{}, jerr
	}

	ancestors := bd.Ancestors(id, depth)
	filtered, err := clearance.FilterByAccess(s.eng.Index(), ancestors, s.viewerRoles())
	if err != nil {
		res, jerr := toolError("get_context: filter", err)
		return res, getContextOut{}, jerr
	}

	out := getContextOut{Ancestors: make([]beadView, len(filtered))}
	for i, b := range filtered {
		out.Ancestors[i] = newBeadView(b)
	}
	return nil, out, nil
}

// --- get_timeline -----------------------------------------------------

type getTimelineIn struct {
	PatientID string `json:"patient_id" jsonschema:"patient_root Bead ID (sha256: prefix optional)"`
}

type getTimelineOut struct {
	Beads []beadRefView `json:"beads"`
}

func (s *Server) getTimeline(_ context.Context, _ *mcp.CallToolRequest, in getTimelineIn) (*mcp.CallToolResult, getTimelineOut, error) {
	root, err := bead.ParseID(in.PatientID)
	if err != nil {
		res, jerr := toolError("get_timeline: parse patient_id", err)
		return res, getTimelineOut{}, jerr
	}

	refs, err := s.eng.Index().ListPatientBeads(root)
	if err != nil {
		res, jerr := toolError("get_timeline", err)
		return res, getTimelineOut{}, jerr
	}

	beads := make([]bead.Bead, 0, len(refs))
	for _, ref := range refs {
		b, err := s.eng.GetBead(ref.ID)
		if err != nil {
			res, jerr := toolError("get_timeline: get_bead "+ref.ID, err)
			return res, getTimelineOut{}, jerr
		}
		beads = append(beads, b)
	}
	filtered, err := clearance.FilterByAccess(s.eng.Index(), beads, s.viewerRoles())
	if err != nil {
		res, jerr := toolError("get_timeline: filter", err)
		return res, getTimelineOut{}, jerr
	}

	var out getTimelineOut
	for i, b := range filtered {
		if !accessible(b) {
			continue
		}
		out.Beads = append(out.Beads, newBeadRefView(b, refs[i].PatientRoot, refs[i].Summary))
	}
	return nil, out, nil
}

// --- get_links (clinical_links, U3c) --------------------------------------

type getLinksIn struct {
	ID string `json:"id" jsonschema:"Bead ID (sha256: prefix optional)"`
}

// clinicalLinkView is get_links' JSON shape for one clinical_links row: the
// interpretation-layer relation/severity/evidence fields the link projector
// (package projector, U3b) computed, plus the other Bead's ID. No
// score_breakdown/patient_root field is surfaced here — those are internal
// projector bookkeeping (score_breakdown) or already implied by the caller's
// own patient context (patient_root), not something a link consumer needs to
// re-derive its trust in the relation from ("just enough to act on" shape).
type clinicalLinkView struct {
	LinkID          string   `json:"link_id"`
	OtherBeadID     string   `json:"other_bead_id"`
	Relation        string   `json:"relation"`
	MatchedTag      string   `json:"matched_tag"`
	Severity        string   `json:"severity"`
	EvidenceBasis   string   `json:"evidence_basis"`
	EvidenceBeadIDs []string `json:"evidence_bead_ids,omitempty"`
	RuleID          string   `json:"rule_id,omitempty"`
	RuleVersion     string   `json:"rule_version,omitempty"`
	CreatedAt       string   `json:"created_at"`
}

type getLinksOut struct {
	Links []clinicalLinkView `json:"links"`
}

// getLinks is U3c's read path for clinical_links (specs/U3_link_projector.md
// 's U3c section): the link-projector successor to the old get_sibling_links
// tool, which read the now-inert sibling_pairs table and was removed in U5a
// (specs/U5_api_retrieve.md) once package apc (the scanner that produced
// sibling_link Beads) was deleted — clinical_links is now the sole link
// mechanism.
//
// # Clearance inheritance
//
// Per specs/DESIGN_v3.1_draft.md §0 point 6 / §3 ("クリアランス継承…既存の
// get_sibling_links の drop 原則を一般化"): a clinical_links row names another
// Bead (OtherBeadID) whose mere existence-as-a-link — and its MatchedTag,
// which is itself often clinically informative (e.g. a risk:/atc: tag) —
// is information about that other Bead, not just about id. This mirrors the
// old get_sibling_links tool's identical reasoning verbatim, so it resolves
// every candidate other Bead via engine.GetBead, runs the batch through
// clearance.FilterByAccess exactly like every other read tool, and drops the
// whole row (not just the OtherBeadID field) for any link whose other Bead
// is not accessible to this session's role.
func (s *Server) getLinks(_ context.Context, _ *mcp.CallToolRequest, in getLinksIn) (*mcp.CallToolResult, getLinksOut, error) {
	id, err := bead.ParseID(in.ID)
	if err != nil {
		res, jerr := toolError("get_links: parse id", err)
		return res, getLinksOut{}, jerr
	}

	rows, err := s.eng.Index().GetClinicalLinks(id)
	if err != nil {
		res, jerr := toolError("get_links", err)
		return res, getLinksOut{}, jerr
	}
	if len(rows) == 0 {
		return nil, getLinksOut{}, nil
	}

	otherBeads := make([]bead.Bead, len(rows))
	for i, r := range rows {
		b, err := s.eng.GetBead(r.OtherBeadID)
		if err != nil {
			res, jerr := toolError("get_links: get_bead "+r.OtherBeadID, err)
			return res, getLinksOut{}, jerr
		}
		otherBeads[i] = b
	}
	filtered, err := clearance.FilterByAccess(s.eng.Index(), otherBeads, s.viewerRoles())
	if err != nil {
		res, jerr := toolError("get_links: filter", err)
		return res, getLinksOut{}, jerr
	}

	var out getLinksOut
	for i, r := range rows {
		if !accessible(filtered[i]) {
			continue
		}
		var evidenceIDs []string
		if r.EvidenceBeadIDs != "" && r.EvidenceBeadIDs != "[]" {
			if err := json.Unmarshal([]byte(r.EvidenceBeadIDs), &evidenceIDs); err != nil {
				res, jerr := toolError("get_links: decode evidence_bead_ids for "+r.LinkID, err)
				return res, getLinksOut{}, jerr
			}
		}
		formattedEvidence := make([]string, len(evidenceIDs))
		for j, eid := range evidenceIDs {
			formattedEvidence[j] = bead.FormatID(eid)
		}
		out.Links = append(out.Links, clinicalLinkView{
			LinkID:          bead.FormatID(r.LinkID),
			OtherBeadID:     bead.FormatID(r.OtherBeadID),
			Relation:        r.Relation,
			MatchedTag:      r.MatchedTag,
			Severity:        r.Severity,
			EvidenceBasis:   r.EvidenceBasis,
			EvidenceBeadIDs: formattedEvidence,
			RuleID:          r.RuleID,
			RuleVersion:     r.RuleVersion,
			CreatedAt:       r.CreatedAt,
		})
	}
	return nil, out, nil
}

// --- search_antigens (bead_tags inverted index) -----------------------------

type searchAntigensIn struct {
	Antigen   string `json:"antigen" jsonschema:"antigen tag, e.g. snomed:44054006"`
	PatientID string `json:"patient_id,omitempty" jsonschema:"restrict results to this patient (sha256: prefix optional)"`
}

type searchAntigensOut struct {
	Beads []beadRefView `json:"beads"`
}

func (s *Server) searchAntigens(_ context.Context, _ *mcp.CallToolRequest, in searchAntigensIn) (*mcp.CallToolResult, searchAntigensOut, error) {
	if in.Antigen == "" {
		res, jerr := toolError("search_antigens", fmt.Errorf("antigen must not be empty"))
		return res, searchAntigensOut{}, jerr
	}

	var patientRoot string
	if in.PatientID != "" {
		root, err := bead.ParseID(in.PatientID)
		if err != nil {
			res, jerr := toolError("search_antigens: parse patient_id", err)
			return res, searchAntigensOut{}, jerr
		}
		patientRoot = root
	}

	query := `
		SELECT b.id, COALESCE(b.patient_root, ''), b.type, b.timestamp, COALESCE(b.summary, '')
		FROM bead_tags ba
		JOIN beads b ON b.id = ba.bead_id
		WHERE ba.tag = ?`
	args := []any{in.Antigen}
	if patientRoot != "" {
		query += " AND ba.patient_root = ?"
		args = append(args, patientRoot)
	}
	query += " ORDER BY b.timestamp, b.id"

	rows, err := s.eng.Index().SQLDB().Query(query, args...)
	if err != nil {
		res, jerr := toolError("search_antigens", err)
		return res, searchAntigensOut{}, jerr
	}
	defer rows.Close()

	type row struct{ id, root, typ, ts, summary string }
	var found []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.root, &r.typ, &r.ts, &r.summary); err != nil {
			res, jerr := toolError("search_antigens: scan", err)
			return res, searchAntigensOut{}, jerr
		}
		found = append(found, r)
	}
	if err := rows.Err(); err != nil {
		res, jerr := toolError("search_antigens", err)
		return res, searchAntigensOut{}, jerr
	}

	beads := make([]bead.Bead, 0, len(found))
	for _, r := range found {
		b, err := s.eng.GetBead(r.id)
		if err != nil {
			res, jerr := toolError("search_antigens: get_bead "+r.id, err)
			return res, searchAntigensOut{}, jerr
		}
		beads = append(beads, b)
	}
	filtered, err := clearance.FilterByAccess(s.eng.Index(), beads, s.viewerRoles())
	if err != nil {
		res, jerr := toolError("search_antigens: filter", err)
		return res, searchAntigensOut{}, jerr
	}

	var out searchAntigensOut
	for i, b := range filtered {
		if !accessible(b) {
			continue
		}
		out.Beads = append(out.Beads, newBeadRefView(b, found[i].root, found[i].summary))
	}
	return nil, out, nil
}

// --- verify_integrity -------------------------------------------------

type verifyIntegrityIn struct{}

type verifyIntegrityOut struct {
	OK          bool     `json:"ok"`
	PodsChecked int      `json:"pods_checked"`
	FramesTotal int      `json:"frames_total"`
	Failures    []string `json:"failures,omitempty"`
}

func (s *Server) verifyIntegrity(_ context.Context, _ *mcp.CallToolRequest, _ verifyIntegrityIn) (*mcp.CallToolResult, verifyIntegrityOut, error) {
	report, err := pod.VerifyAll(s.store)
	if err != nil {
		res, jerr := toolError("verify_integrity", err)
		return res, verifyIntegrityOut{}, jerr
	}

	out := verifyIntegrityOut{
		OK:          report.OK(),
		PodsChecked: len(report.Pods),
		FramesTotal: report.TotalFrames(),
	}
	for _, p := range report.FailedPods() {
		for _, f := range p.FailedFrames() {
			out.Failures = append(out.Failures, fmt.Sprintf("%s: frame at offset %d (bead_id=%s): %v", p.Path, f.Offset, f.BeadID, f.Err))
		}
		if p.Truncated {
			out.Failures = append(out.Failures, fmt.Sprintf("%s: truncated at offset %d: %v", p.Path, p.TruncatedAt, p.TruncationErr))
		}
	}
	return nil, out, nil
}

// --- shared helpers ---------------------------------------------------

// loadBundleForBead resolves id's patient_root via the index and loads its
// graph.Bundle (graph.LoadBundle), for tools (get_context) that need
// ancestor/descendant BFS over a single patient's sub-graph.
func (s *Server) loadBundleForBead(id string) (*graph.Bundle, error) {
	ref, err := s.eng.Index().GetBead(id)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", id, err)
	}
	if ref.PatientRoot == "" {
		return nil, fmt.Errorf("bead %s has no patient_root (shared Pod Beads have no bundle to traverse)", id)
	}
	bd, err := graph.LoadBundle(s.store, ref.PatientRoot)
	if err != nil {
		return nil, fmt.Errorf("load bundle for patient %s: %w", ref.PatientRoot, err)
	}
	return bd, nil
}
