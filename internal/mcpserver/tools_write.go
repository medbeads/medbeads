package mcpserver

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/medbeads/medbeads/internal/engine/antigen"
	"github.com/medbeads/medbeads/internal/engine/bead"
)

// registerWriteTools adds every tool that durably mutates the data
// directory to s.mcp: create_bead, and apc_trigger. Per the lead's decision
// ("書き込み(create_bead)は system ロール限定" — R6.3), New only calls this
// when Role() == SystemRole; every other role never sees these tools in
// tools/list at all (not merely denied at call time), which is the simplest
// possible enforcement for a single-process, single-role-per-launch server
// (no per-request identity to check against).
//
// apc_trigger belongs here, not among the read tools, even though it takes
// no Bead content as input: apc.Scanner.Scan calls engine.Ingest to
// durably persist any new sibling_link Bead it finds (see apc/scanner.go),
// making it a write path in every sense R6.3 cares about (it appends Pod
// frames and index rows exactly like create_bead does) — a viewer-role
// session must not be able to trigger new, permanent Beads being written
// into the data directory just because the tool's own input schema happens
// to be empty.
func (s *Server) registerWriteTools() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "create_bead",
		Description: "Ingest a new Bead (system role only). antigens are always re-derived " +
			"deterministically from type+content via antigen.Extract (any antigens supplied here " +
			"are ignored) before the content hash is computed, so the resulting ID is reproducible " +
			"from type+content alone.",
	}, s.createBead)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "apc_trigger",
		Description: "Run one APC batch scan (apc.Scanner.Scan) now (system role only: this durably " +
			"ingests any new sibling_link Beads it finds), generating sibling_link Beads for new matches.",
	}, s.apcTrigger)
}

type apcTriggerIn struct{}

type apcTriggerOut struct {
	BeadsScanned        int `json:"beads_scanned"`
	SiblingLinksCreated int `json:"sibling_links_created"`
}

// apcTrigger runs one apc.Scanner.Scan pass. See registerWriteTools' doc
// comment for why this is a write tool (system role only) despite its empty
// input schema: Scan durably ingests any new sibling_link Bead it finds.
func (s *Server) apcTrigger(_ context.Context, _ *mcp.CallToolRequest, _ apcTriggerIn) (*mcp.CallToolResult, apcTriggerOut, error) {
	res, err := s.scan.Scan()
	if err != nil {
		errRes, jerr := toolError("apc_trigger", err)
		return errRes, apcTriggerOut{}, jerr
	}
	return nil, apcTriggerOut{BeadsScanned: res.BeadsScanned, SiblingLinksCreated: res.SiblingLinksCreated}, nil
}

type createBeadIn struct {
	Type      string         `json:"type" jsonschema:"Bead type, e.g. fhir_observation"`
	Timestamp string         `json:"timestamp" jsonschema:"RFC3339 timestamp"`
	Author    string         `json:"author,omitempty" jsonschema:"author DID/identifier"`
	Parents   []string       `json:"parents,omitempty" jsonschema:"parent Bead IDs (sha256: prefix optional); every parent must already be indexed"`
	Content   map[string]any `json:"content" jsonschema:"Bead content (FHIR-shaped for antigen.Extract to find coding[] in)"`
}

type createBeadOut struct {
	Bead beadView `json:"bead"`
}

// createBead builds a bead.Bead from in, applies antigen.Extract to
// determine its Antigens (the task's "antigen.Extract を適用して antigens を
// 決定論生成"), assigns its content-hash ID via bead.WithID (delegated to
// engine.Ingest, which calls this internally for an ID-less Bead — see
// verifyOrAssignID), and ingests it via engine.Ingest.
func (s *Server) createBead(_ context.Context, _ *mcp.CallToolRequest, in createBeadIn) (*mcp.CallToolResult, createBeadOut, error) {
	if in.Type == "" {
		res, jerr := toolError("create_bead", fmt.Errorf("type must not be empty"))
		return res, createBeadOut{}, jerr
	}
	if in.Timestamp == "" {
		res, jerr := toolError("create_bead", fmt.Errorf("timestamp must not be empty"))
		return res, createBeadOut{}, jerr
	}

	parents := make([]string, len(in.Parents))
	for i, p := range in.Parents {
		id, err := bead.ParseID(p)
		if err != nil {
			res, jerr := toolError("create_bead: parse parent", err)
			return res, createBeadOut{}, jerr
		}
		parents[i] = id
	}

	content := in.Content
	if content == nil {
		content = map[string]any{}
	}

	b := bead.Bead{
		Type:      in.Type,
		Timestamp: in.Timestamp,
		Author:    in.Author,
		Parents:   parents,
		Antigens:  antigen.Extract(in.Type, content),
		Content:   content,
	}

	saved, err := s.eng.Ingest(b)
	if err != nil {
		res, jerr := toolError("create_bead", err)
		return res, createBeadOut{}, jerr
	}

	return nil, createBeadOut{Bead: newBeadView(saved)}, nil
}
