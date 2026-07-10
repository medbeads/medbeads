package mcpserver

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/medbeads/medbeads/internal/engine/bead"
	"github.com/medbeads/medbeads/internal/engine/clearance"
	"github.com/medbeads/medbeads/internal/engine/index"
)

// defaultRagSearchK is rag_search's default top-k when the caller omits k.
const defaultRagSearchK = 10

// ragSearchIn is R6.3's experimental comparison tool: query -> embed -> pure
// vector top-k (no chain/graph expansion at all — see rag_search's own doc
// comment for why this is deliberately NOT retrieve).
type ragSearchIn struct {
	Query     string `json:"query" jsonschema:"text to embed and search for nearest neighbors"`
	PatientID string `json:"patient_id,omitempty" jsonschema:"restrict results to this patient (sha256: prefix optional); omit to search every patient"`
	K         int    `json:"k,omitempty" jsonschema:"number of nearest neighbors to return (default 10)"`
}

type ragSearchResultView struct {
	ID          string  `json:"id"`
	PatientRoot string  `json:"patient_root,omitempty"`
	Type        string  `json:"type"`
	Timestamp   string  `json:"timestamp"`
	Distance    float64 `json:"distance"`
	// Content is the Bead's full L0 content (R6.3: "L0 content + distance を
	// 返す") — no L1/L2 tiering, no token budget, no graph expansion at all:
	// rag_search is intentionally the simplest possible retrieval baseline
	// (pure vector top-k) for the bench/ RAG-arm comparison experiments
	// (DESIGN §8/§9, docs/requirements.md R8.2), not a production agent tool.
	Content map[string]any `json:"content"`
}

type ragSearchOut struct {
	Results []ragSearchResultView `json:"results"`
}

// ragSearch implements R6.3's rag_search: embed query via s.embedder, run a
// pure index.DB.SemanticSearch top-k (no FTS, no antigen filter, no
// graph/chain expansion whatsoever — the "chain 拡張なし" the lead's task
// spec calls for, distinguishing this from retrieve's own anchor->expand->
// chain pipeline), clearance-filter the hits (mask-then-drop, per this
// package's uniform policy — see render.go's accessible doc comment), and
// return each surviving hit's full L0 content plus its vector distance.
//
// Registered as a read tool (see registerReadTools): rag_search never
// mutates the data directory, even though DESIGN §8 calls it out as
// "比較実験用" (an experimental comparison baseline for bench/'s RAG arm, R8.2)
// rather than part of the main agent retrieval path.
func (s *Server) ragSearch(ctx context.Context, _ *mcp.CallToolRequest, in ragSearchIn) (*mcp.CallToolResult, ragSearchOut, error) {
	if s.embedder == nil {
		res, jerr := toolError("rag_search", fmt.Errorf(
			"rag_search requires an embedder, but this server has none configured (see serve's -embedder flag; docs/requirements.md R4.2/R6.3)"))
		return res, ragSearchOut{}, jerr
	}
	if in.Query == "" {
		res, jerr := toolError("rag_search", fmt.Errorf("query must not be empty"))
		return res, ragSearchOut{}, jerr
	}

	var patientRoot string
	if in.PatientID != "" {
		root, err := bead.ParseID(in.PatientID)
		if err != nil {
			res, jerr := toolError("rag_search: parse patient_id", err)
			return res, ragSearchOut{}, jerr
		}
		patientRoot = root
	}

	k := in.K
	if k <= 0 {
		k = defaultRagSearchK
	}

	vectors, err := s.embedder.Embed(ctx, []string{in.Query})
	if err != nil {
		res, jerr := toolError("rag_search: embed query", err)
		return res, ragSearchOut{}, jerr
	}
	if len(vectors) != 1 {
		res, jerr := toolError("rag_search: embed query", fmt.Errorf("embedder returned %d vector(s), want 1", len(vectors)))
		return res, ragSearchOut{}, jerr
	}
	queryBlob, err := index.SerializeEmbedding(vectors[0])
	if err != nil {
		res, jerr := toolError("rag_search: embed query", err)
		return res, ragSearchOut{}, jerr
	}

	hits, err := s.eng.Index().SemanticSearch(queryBlob, k, patientRoot)
	if err != nil {
		res, jerr := toolError("rag_search", err)
		return res, ragSearchOut{}, jerr
	}
	if len(hits) == 0 {
		return nil, ragSearchOut{}, nil
	}

	beads := make([]bead.Bead, len(hits))
	for i, h := range hits {
		b, err := s.eng.GetBead(h.BeadID)
		if err != nil {
			res, jerr := toolError("rag_search: get_bead "+h.BeadID, err)
			return res, ragSearchOut{}, jerr
		}
		beads[i] = b
	}

	// mask-then-drop (see render.go's accessible doc comment): a restricted
	// Bead's mere existence-as-a-search-hit — and its distance, which itself
	// leaks how semantically close it is to the query — must not appear in
	// the response at all, not merely have its Content masked in place.
	filtered, err := clearance.FilterByAccess(s.eng.Index(), beads, s.viewerRoles())
	if err != nil {
		res, jerr := toolError("rag_search: filter", err)
		return res, ragSearchOut{}, jerr
	}

	var out ragSearchOut
	for i, b := range filtered {
		if !accessible(b) {
			continue
		}
		view := ragSearchResultView{
			ID:        bead.FormatID(b.ID),
			Type:      b.Type,
			Timestamp: b.Timestamp,
			Distance:  hits[i].Distance,
			Content:   b.Content,
		}
		if hits[i].PatientRoot != "" {
			view.PatientRoot = bead.FormatID(hits[i].PatientRoot)
		}
		out.Results = append(out.Results, view)
	}
	return nil, out, nil
}
