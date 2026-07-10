package mcpserver

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

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
		Description: "Ingest a new Bead (system role only). This tool does not accept antigens/tags " +
			"at all: tag derivation (antigen.Extract) runs only at index-projection time, from the " +
			"stored type+content, so a Bead's content hash never depends on which tagging dictionary " +
			"happened to be current when it was ingested (specs/DESIGN_v3.1_draft.md §2).",
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
	Type      string         `json:"type" jsonschema:"Bead type, e.g. fhir_observation, clinical_note, assessment, attestation, retraction"`
	Timestamp string         `json:"timestamp" jsonschema:"RFC3339 timestamp"`
	Author    string         `json:"author,omitempty" jsonschema:"author DID/identifier"`
	Parents   []string       `json:"parents,omitempty" jsonschema:"parent Bead IDs (sha256: prefix optional); every parent must already be indexed"`
	Amends    []string       `json:"amends,omitempty" jsonschema:"Bead IDs this Bead corrects (sha256: prefix optional); every target must already be indexed and share this Bead's patient_root"`
	Retracts  []string       `json:"retracts,omitempty" jsonschema:"Bead IDs this Bead retracts as entered-in-error (sha256: prefix optional); every target must already be indexed and share this Bead's patient_root"`
	Content   map[string]any `json:"content" jsonschema:"Bead content (FHIR-shaped for antigen.Extract to find coding[] in)"`
}

type createBeadOut struct {
	Bead beadView `json:"bead"`
}

// createBead builds a bead.Bead from in, assigns its content-hash ID via
// bead.WithID (delegated to engine.Ingest, which calls this internally for
// an ID-less Bead — see verifyOrAssignID), and ingests it via engine.Ingest.
// It does not compute or accept antigens/tags at all (v3.1: tag derivation
// runs only at index-projection time — see index.IndexBead).
func (s *Server) createBead(_ context.Context, _ *mcp.CallToolRequest, in createBeadIn) (*mcp.CallToolResult, createBeadOut, error) {
	if in.Type == "" {
		res, jerr := toolError("create_bead", fmt.Errorf("type must not be empty"))
		return res, createBeadOut{}, jerr
	}
	if in.Timestamp == "" {
		res, jerr := toolError("create_bead", fmt.Errorf("timestamp must not be empty"))
		return res, createBeadOut{}, jerr
	}

	parents, err := parseIDs(in.Parents)
	if err != nil {
		res, jerr := toolError("create_bead: parse parent", err)
		return res, createBeadOut{}, jerr
	}
	amends, err := parseIDs(in.Amends)
	if err != nil {
		res, jerr := toolError("create_bead: parse amends", err)
		return res, createBeadOut{}, jerr
	}
	retracts, err := parseIDs(in.Retracts)
	if err != nil {
		res, jerr := toolError("create_bead: parse retracts", err)
		return res, createBeadOut{}, jerr
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
		Amends:    amends,
		Retracts:  retracts,
		Content:   content,
	}

	saved, err := s.eng.Ingest(b)
	if err != nil {
		res, jerr := toolError("create_bead", err)
		return res, createBeadOut{}, jerr
	}

	return nil, createBeadOut{Bead: newBeadView(saved)}, nil
}

// parseIDs runs bead.ParseID over every element of ids, normalizing each to
// its plain-hex form (accepting an optional "sha256:" prefix on input — see
// bead.ParseID). Returns the first parse error encountered, wrapped with
// enough context to identify which ID was malformed.
func parseIDs(ids []string) ([]string, error) {
	out := make([]string, len(ids))
	for i, id := range ids {
		parsed, err := bead.ParseID(id)
		if err != nil {
			return nil, fmt.Errorf("id %q: %w", id, err)
		}
		out[i] = parsed
	}
	return out, nil
}
