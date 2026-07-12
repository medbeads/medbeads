package mcpserver

import (
	"context"
	"testing"
)

// TestCreateBead_CorrectionRequiresAuthor pins the write-boundary rule that makes
// the correction chain accountable: a Bead that changes what an earlier record
// MEANS must name who made it.
//
// The attestation case is the sharpest. An attestation's entire clinical content
// is "a named clinician signed off on this record", and projector/resolve.go
// promotes an amendment to `current` on the strength of one. With an empty author
// it asserts that NOBODY signed off — and the resolver would honour it, because
// it reads only content.verdict. That would make the one transition in this
// system carrying clinical accountability a rubber stamp.
//
// The check lives here, at the WRITE boundary, and deliberately NOT in the
// projector: the fact layer is append-only, so refusing authorless attestations
// at projection time would silently revoke approvals that were legitimately
// written before this rule existed — the interpretation layer would be rewriting
// the meaning of history rather than deriving it.
func TestCreateBead_CorrectionRequiresAuthor(t *testing.T) {
	e := openT(t)
	s := newServerT(t, e, SystemRole)
	p := seedPatient(t, e, "Correction Patient")

	// A plain clinical fact needs no author: the Synthea-imported Beads in the
	// production store have none, and demanding one would be a false claim of
	// provenance about bulk-imported data. The line is drawn at corrections,
	// because that is where accountability is the point.
	// createBead returns a nil *CallToolResult on success and a non-nil one
	// (carrying IsError) on failure, so "did it fail?" is `res != nil`.
	res, out, err := s.createBead(context.Background(), nil, createBeadIn{
		Type:      "fhir_observation",
		Timestamp: "2026-01-01T00:00:00Z",
		Parents:   []string{p.ID},
		Content:   map[string]any{"code": "glucose"},
	})
	if err != nil || res != nil {
		t.Fatalf("createBead(plain observation, no author) must succeed: err=%v res=%+v", err, res)
	}
	original := out.Bead.ID

	corrections := []struct {
		name string
		in   createBeadIn
	}{
		{
			name: "amendment",
			in: createBeadIn{
				Type:      "fhir_observation",
				Timestamp: "2026-01-02T00:00:00Z",
				Parents:   []string{p.ID},
				Amends:    []string{original},
				Content:   map[string]any{"code": "glucose", "value": 5.5},
			},
		},
		{
			name: "retraction",
			in: createBeadIn{
				Type:      "retraction",
				Timestamp: "2026-01-02T00:00:00Z",
				Parents:   []string{original},
				Retracts:  []string{original},
				Content:   map[string]any{"reason": "entered-in-error"},
			},
		},
		{
			name: "attestation",
			in: createBeadIn{
				Type:      "attestation",
				Timestamp: "2026-01-02T00:00:00Z",
				Parents:   []string{original},
				Content:   map[string]any{"verdict": "approved"},
			},
		},
	}

	for _, tc := range corrections {
		t.Run(tc.name+"_without_author_is_refused", func(t *testing.T) {
			res, _, _ := s.createBead(context.Background(), nil, tc.in)
			if res == nil {
				t.Errorf("createBead(%s, author=\"\") succeeded; want an error.\n"+
					"  A correction that cannot name who made it is not auditable, and an\n"+
					"  attestation nobody signed is not an approval.", tc.name)
			}
		})

		t.Run(tc.name+"_with_author_is_accepted", func(t *testing.T) {
			in := tc.in
			in.Author = "dr-attending"
			res, out, err := s.createBead(context.Background(), nil, in)
			if err != nil || res != nil {
				t.Fatalf("createBead(%s, author=%q) must succeed: err=%v res=%+v", tc.name, in.Author, err, res)
			}
			if out.Bead.ID == "" {
				t.Errorf("createBead(%s) returned an empty Bead ID", tc.name)
			}
		})
	}
}
