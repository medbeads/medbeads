package bead

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/gowebpki/jcs"
)

// IDLen is the length in bytes of a Bead ID (SHA-256 digest).
const IDLen = sha256.Size

// HexIDLen is the length of the lower-case hex encoding of an ID.
const HexIDLen = IDLen * 2

// Evidence references a large binary artifact (DICOM, PDF, image, ...) that
// lives outside the Bead itself. See specs/DESIGN_v3.md §4 and
// specs/MEDBEADS_SPECIFICATION_v2.1.md §4.1.
type Evidence struct {
	URI      string `json:"uri"`
	MimeType string `json:"mime_type"`
	Hash     string `json:"hash"`
}

// Clearance carries access-control settings for a Bead. It is intentionally
// excluded from the hash: clearance is a mutable overlay, not part of the
// tamper-evident content (specs/DESIGN_v3.md §4).
type Clearance struct {
	DeniedRoles  []string `json:"denied_roles,omitempty"`
	AllowedRoles []string `json:"allowed_roles,omitempty"`
	Reason       string   `json:"reason,omitempty"`
	ExpiresAt    *string  `json:"expires_at,omitempty"`
}

// Bead is the fundamental content-addressed unit in MedBeads v3.
//
// ID = sha256(JCS({type, timestamp, author, parents, amends, retracts,
// content, evidence})). clearance and signature are deliberately excluded
// from the hash. patient_root is NOT a field of Bead: it is derived (Pod
// frame meta + index column only). See specs/DESIGN_v3.1_draft.md §2.
//
// # No Antigens field (v3.1: 事実と解釈の二層分離)
//
// v3.0 carried an Antigens []string hash-target field. v3.1 removes it
// entirely: antigen/tag derivation is knowledge-based interpretation, not an
// immutable fact about the Bead, so it now lives exclusively in the index
// projection (antigen.Extract, run at IndexBead time from Type+Content —
// see internal/engine/index.IndexBead). A Bead's hash is therefore
// independent of which tagging dictionary happens to be current when it was
// ingested, and re-tagging (a dictionary update) never changes any Bead's
// ID — only its projection. specs/DESIGN_v3.1_draft.md §0/§2.
//
// # amends / retracts and cycle-freedom
//
// amends and retracts name other Bead IDs (existing corrections/retraction
// facts), mirroring Parents' hash-target-normalization treatment (dedup +
// lexicographic sort — see Normalize). A cycle (Bead A amends/retracts B
// while B amends/retracts A) is structurally impossible, not merely
// forbidden by convention: computing A's ID requires content-hash B's ID to
// already be known (B must exist, fully hashed, before A can reference it),
// so no chain of amends/retracts references can ever loop back to a
// not-yet-hashed Bead. This follows directly from content-addressing's
// forward-reference-only property, the same structural argument that already
// makes the Parents DAG acyclic (see engine.Ingest's doc comment) — no
// runtime cycle check is needed or implemented here.
type Bead struct {
	ID        string         `json:"id,omitempty"`
	Type      string         `json:"type"`
	Timestamp string         `json:"timestamp"`
	Author    string         `json:"author,omitempty"`
	Parents   []string       `json:"parents"`
	Amends    []string       `json:"amends"`
	Retracts  []string       `json:"retracts"`
	Content   map[string]any `json:"content"`
	Evidence  []Evidence     `json:"evidence,omitempty"`

	// Clearance and Signature are excluded from the hash (see hashPayload).
	Clearance *Clearance `json:"clearance,omitempty"`
	Signature string     `json:"signature,omitempty"`
}

// hashPayload is the exact set of hash-target fields, in the field order
// mandated by specs/DESIGN_v3.1_draft.md §2: {type, timestamp, author,
// parents, amends, retracts, content, evidence}. JCS re-sorts object keys
// lexicographically regardless of struct field/json order, but keeping this
// order documents the spec and keeps json.Marshal output stable prior to
// canonicalization.
type hashPayload struct {
	Type      string         `json:"type"`
	Timestamp string         `json:"timestamp"`
	Author    string         `json:"author,omitempty"`
	Parents   []string       `json:"parents"`
	Amends    []string       `json:"amends"`
	Retracts  []string       `json:"retracts"`
	Content   map[string]any `json:"content"`
	Evidence  []Evidence     `json:"evidence,omitempty"`
}

// normalizeStrings returns a deduplicated, lexicographically sorted copy of
// ss. A nil or empty input yields a non-nil empty slice so that Bead.Parents /
// Bead.Amends / Bead.Retracts serialize as JSON `[]` rather than `null` (RFC
// 8785 has no canonical form for JSON null-vs-missing arrays, and `null`
// would change the hash payload's shape). See specs/DESIGN_v3.1_draft.md §2.
func normalizeStrings(ss []string) []string {
	out := make([]string, 0, len(ss))
	seen := make(map[string]struct{}, len(ss))
	for _, s := range ss {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// Normalize returns a copy of b with Parents, Amends, and Retracts each
// deduplicated and sorted into lexicographic order, per
// specs/DESIGN_v3.1_draft.md §2 ("amends: dedup+辞書順 = parents と同じ正規化").
// Canonicalize and ComputeID always apply this normalization internally, so
// callers never need to call it themselves merely to get a correct ID — it
// is exported so callers (e.g. ingest code) can normalize a Bead once before
// repeated use.
func Normalize(b Bead) Bead {
	out := b
	out.Parents = normalizeStrings(b.Parents)
	out.Amends = normalizeStrings(b.Amends)
	out.Retracts = normalizeStrings(b.Retracts)
	return out
}

// Canonicalize returns the RFC 8785 (JCS) canonical JSON encoding of the
// hash-target fields of b: {type, timestamp, author, parents, amends,
// retracts, content, evidence}. clearance and signature are never included.
// parents, amends, and retracts are normalized (dedup + sort) before
// encoding so that the result is order-independent, per
// specs/DESIGN_v3.1_draft.md §2.
func Canonicalize(b Bead) ([]byte, error) {
	n := Normalize(b)
	payload := hashPayload{
		Type:      n.Type,
		Timestamp: n.Timestamp,
		Author:    n.Author,
		Parents:   n.Parents,
		Amends:    n.Amends,
		Retracts:  n.Retracts,
		Content:   n.Content,
		Evidence:  n.Evidence,
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("bead: marshal hash payload: %w", err)
	}

	canonical, err := jcs.Transform(raw)
	if err != nil {
		return nil, fmt.Errorf("bead: JCS canonicalize: %w", err)
	}
	return canonical, nil
}

// ComputeID returns the content-hash ID of b: sha256(JCS(hash-target
// fields)), as lower-case hex (no "sha256:" prefix — see ID Notation below).
// It does not read or mutate b.ID.
func ComputeID(b Bead) (string, error) {
	canonical, err := Canonicalize(b)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

// WithID returns a copy of b with ID set to its computed content hash.
func WithID(b Bead) (Bead, error) {
	id, err := ComputeID(b)
	if err != nil {
		return Bead{}, err
	}
	out := b
	out.ID = id
	return out, nil
}

// Verify reports whether b.ID matches the content hash recomputed from b's
// hash-target fields. A tampered Content, Type, Timestamp, Author, Parents,
// Amends, Retracts, or Evidence — or a mismatched ID — makes Verify return
// an error. Verify never fails due to Clearance or Signature, since those
// are outside the hash by design.
func Verify(b Bead) error {
	if b.ID == "" {
		return fmt.Errorf("bead: verify: empty ID")
	}
	want, err := ComputeID(b)
	if err != nil {
		return fmt.Errorf("bead: verify: %w", err)
	}
	got := strings.ToLower(b.ID)
	if got != want {
		return fmt.Errorf("bead: verify: ID mismatch: bead.ID=%s recomputed=%s", b.ID, want)
	}
	return nil
}

// --- ID notation helpers -----------------------------------------------
//
// Internally, IDs are always plain 64-char lower-case hex (no prefix); see
// specs/DESIGN_v3.md §4 ("内部は素の 64 hex"). The "sha256:" display prefix is
// an API/presentation-layer concern and does not belong in this package's
// core Bead/ID plumbing. These two helpers exist only so that callers at the
// edges (REST/MCP request parsing) have a single, shared place to convert.

// IDPrefix is the display-layer prefix documented in
// specs/MEDBEADS_SPECIFICATION_v2.1.md §4.1 (e.g. "sha256:e3b0c4...").
const IDPrefix = "sha256:"

// FormatID adds the "sha256:" display prefix to a plain hex ID.
func FormatID(id string) string {
	return IDPrefix + id
}

// ParseID accepts either a plain 64-hex-char ID or one prefixed with
// "sha256:", validates that it is well-formed lower-case hex of the correct
// length, and returns the plain (unprefixed) form.
func ParseID(s string) (string, error) {
	id := strings.TrimPrefix(s, IDPrefix)
	if len(id) != HexIDLen {
		return "", fmt.Errorf("bead: parse ID: want %d hex chars, got %d (%q)", HexIDLen, len(id), s)
	}
	decoded, err := hex.DecodeString(id)
	if err != nil {
		return "", fmt.Errorf("bead: parse ID: %w", err)
	}
	if len(decoded) != IDLen {
		return "", fmt.Errorf("bead: parse ID: decoded length %d, want %d", len(decoded), IDLen)
	}
	// Reject upper-case / mixed-case hex: canonical storage is lower-case,
	// and silently accepting other cases would let two textually different
	// strings address the same Bead without any normalization step here.
	if id != strings.ToLower(id) {
		return "", fmt.Errorf("bead: parse ID: must be lower-case hex (%q)", s)
	}
	return id, nil
}
