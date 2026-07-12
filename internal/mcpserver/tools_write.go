package mcpserver

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/medbeads/medbeads/internal/engine/bead"
)

// registerWriteTools adds every tool that durably mutates the data
// directory to s.mcp: create_bead. Per the lead's decision ("書き込み
// (create_bead)は system ロール限定" — R6.3), New only calls this when
// Role() == SystemRole; every other role never sees these tools in
// tools/list at all (not merely denied at call time), which is the simplest
// possible enforcement for a single-process, single-role-per-launch server
// (no per-request identity to check against).
func (s *Server) registerWriteTools() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "create_bead",
		Description: "Ingest a new Bead (system role only). This tool does not accept antigens/tags " +
			"at all: tag derivation (antigen.Extract) runs only at index-projection time, from the " +
			"stored type+content, so a Bead's content hash never depends on which tagging dictionary " +
			"happened to be current when it was ingested (specs/DESIGN_v3.1_draft.md §2).",
	}, s.createBead)
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

// requiresAuthor reports whether this Bead is a CORRECTION — one that changes
// what an earlier record means, rather than merely recording a new clinical fact.
//
// Three shapes qualify:
//   - an amendment (Amends non-empty): supersedes an earlier record
//   - a retraction (Retracts non-empty, or type "retraction"): withdraws one as
//     entered-in-error
//   - an attestation (type "attestation"): the clinician sign-off that decides
//     whether an amendment becomes current at all (projector/resolve.go)
//
// An ordinary observation is deliberately NOT covered: the Synthea-imported facts
// in this store have no author, and demanding one would be a false claim of
// provenance about bulk-imported data. The line is drawn at corrections, because
// that is precisely where accountability is the point.
func requiresAuthor(beadType string, amends, retracts []string) bool {
	switch beadType {
	case "attestation", "retraction":
		return true
	}
	return len(amends) > 0 || len(retracts) > 0
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
	// A correction must name who made it.
	//
	// An attestation with an empty author asserts that NOBODY approved the record
	// it gates — and resolvePatientState honours it anyway, because it reads only
	// content.verdict. That turns "unattested -> attested", the one transition in
	// this system carrying clinical accountability, into a rubber stamp. The same
	// holds for a retraction (withdrawing a record as entered-in-error) and for
	// any Bead amending another: the question an institution actually asks is
	// "who changed this, and who signed off?", and a fact layer that cannot
	// answer it is not auditable, however immutable it is.
	//
	// Author is inside the content hash (bead.hashPayload), so it is part of the
	// Bead's identity and can never be back-filled or altered — which is exactly
	// why it has to be right at write time.
	//
	// TRUST BOUNDARY: Author is an assertion by a caller this server already
	// trusts (create_bead is registered only under -role system). It is
	// ATTRIBUTABLE, not AUTHENTICATED — the same trust model as X-User-ID on
	// POST /clearance. bead.Bead.Signature, which sits OUTSIDE the hash, is where
	// a future DID/JWS binding belongs. Requiring a non-empty Author does not make
	// the claim authenticated; it makes an unattributable correction impossible,
	// which is a strictly weaker but real property.
	if requiresAuthor(in.Type, in.Amends, in.Retracts) && in.Author == "" {
		res, jerr := toolError("create_bead", fmt.Errorf(
			"author must not be empty for a correction Bead (type=%q, amends=%d, retracts=%d): "+
				"an amendment, retraction or attestation that cannot name who made it is not auditable",
			in.Type, len(in.Amends), len(in.Retracts)))
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
