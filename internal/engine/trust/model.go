// Package trust provides cryptographic provenance for immutable MedBeads.
//
// A signature is itself stored in a signature_attestation Bead that names the
// signed subject as its parent.  The subject therefore keeps the same content
// hash when another clinician or organization adds an approval.  Trust anchors
// (organizations and public keys) deliberately live in an operator-controlled
// policy rather than being accepted from the attestation that they verify.
package trust

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/gowebpki/jcs"

	"github.com/medbeads/medbeads/internal/engine/bead"
)

const (
	PolicySchema               = "medbeads.trust_policy.v1"
	PrivateKeySchema           = "medbeads.ed25519_private_key.v1"
	SignatureAttestationSchema = "medbeads.signature_attestation.v1"
	SigningStatementSchema     = "medbeads.signing_statement.v1"
	SignatureAttestationType   = "signature_attestation"
	AlgorithmEd25519           = "Ed25519"

	PurposeClinicalOrigin = "clinical_origin"
	// PurposeFHIRImport proves which connector retrieved and transformed a
	// FHIR resource. It deliberately does not claim that the source clinician
	// personally applied a digital signature.
	PurposeFHIRImport       = "fhir_import"
	PurposeKnowledgeRelease = "knowledge_release"
)

// Organization is a stable trust-domain identity. Name is a display snapshot;
// only ID participates in key ownership and authorization decisions.
type Organization struct {
	ID   string `json:"organization_id"`
	Name string `json:"organization_name"`
}

// Actor records the authenticated EHR user on whose behalf the hospital
// system signed. The actor does not need to possess a private key in the
// single-hospital deployment model.
type Actor struct {
	ID          string `json:"actor_id"`
	DisplayName string `json:"display_name,omitempty"`
	Role        string `json:"role,omitempty"`
}

// TrustedKey is a public trust anchor. TrustedFor prevents a partner key that
// is accepted for clinical provenance from automatically gaining permission
// to publish link-rule knowledge.
type TrustedKey struct {
	KeyID          string   `json:"key_id"`
	OrganizationID string   `json:"organization_id"`
	Algorithm      string   `json:"algorithm"`
	PublicKey      string   `json:"public_key"`
	TrustedFor     []string `json:"trusted_for"`
	ValidFrom      string   `json:"valid_from,omitempty"`
	ValidTo        string   `json:"valid_to,omitempty"`
	RevokedAt      string   `json:"revoked_at,omitempty"`
}

// Policy is public operational configuration. TenantID identifies the cloud
// tenant/storage boundary; it is intentionally distinct from Organization.ID
// because one tenant may eventually trust several hospitals.
type Policy struct {
	Schema                          string         `json:"schema"`
	TenantID                        string         `json:"tenant_id"`
	RequireKnowledgeRelease         bool           `json:"require_knowledge_release"`
	MinimumKnowledgeApprovals       int            `json:"minimum_knowledge_approvals"`
	AllowCrossOrganizationApprovals bool           `json:"allow_cross_organization_approvals,omitempty"`
	Organizations                   []Organization `json:"organizations"`
	Keys                            []TrustedKey   `json:"keys"`
}

// PrivateKeyFile is the local development/single-hospital key-file shape.
// Production deployments can implement the same signing operation in a KMS or
// HSM and should not place private key material beside patient Pods.
type PrivateKeyFile struct {
	Schema           string `json:"schema"`
	KeyID            string `json:"key_id"`
	OrganizationID   string `json:"organization_id"`
	OrganizationName string `json:"organization_name"`
	Algorithm        string `json:"algorithm"`
	PrivateKey       string `json:"private_key"`
}

// SigningStatement is the exact, canonicalized message covered by a
// signature_attestation. All identity and attribution fields are inside this
// signed object; the human-readable organization name is a signed snapshot,
// while OrganizationID and KeyID are the security identities.
type SigningStatement struct {
	Schema           string `json:"schema"`
	Purpose          string `json:"purpose"`
	SubjectBeadID    string `json:"subject_bead_id"`
	OrganizationID   string `json:"organization_id"`
	OrganizationName string `json:"organization_name"`
	SourceSystemID   string `json:"source_system_id"`
	Actor            Actor  `json:"actor"`
	KeyID            string `json:"key_id"`
	Algorithm        string `json:"algorithm"`
	SignedAt         string `json:"signed_at"`
}

type signatureEnvelope struct {
	Encoding string `json:"encoding"`
	Value    string `json:"value"`
}

type signatureAttestationContent struct {
	Schema    string            `json:"schema"`
	Statement SigningStatement  `json:"statement"`
	Signature signatureEnvelope `json:"signature"`
}

// VerifiedAttestation is returned only after the policy, key lifetime and
// Ed25519 signature have all been checked.
type VerifiedAttestation struct {
	BeadID    string
	Statement SigningStatement
}

// Clone returns a deep copy suitable for retaining as immutable process
// configuration while the caller continues to own its original slices.
func (p Policy) Clone() Policy {
	out := p
	out.Organizations = append([]Organization(nil), p.Organizations...)
	out.Keys = make([]TrustedKey, len(p.Keys))
	for i, key := range p.Keys {
		out.Keys[i] = key
		out.Keys[i].TrustedFor = append([]string(nil), key.TrustedFor...)
	}
	return out
}

// Validate fails closed on duplicate identities, unknown purposes and invalid
// public-key material.
func (p Policy) Validate() error {
	if p.Schema != PolicySchema {
		return fmt.Errorf("trust: policy schema %q, want %q", p.Schema, PolicySchema)
	}
	if strings.TrimSpace(p.TenantID) == "" {
		return errors.New("trust: policy tenant_id is required")
	}
	if p.MinimumKnowledgeApprovals < 1 {
		return errors.New("trust: policy minimum_knowledge_approvals must be >= 1")
	}
	organizations := make(map[string]Organization, len(p.Organizations))
	for _, org := range p.Organizations {
		if org.ID == "" || org.Name == "" {
			return errors.New("trust: every organization requires organization_id and organization_name")
		}
		if _, exists := organizations[org.ID]; exists {
			return fmt.Errorf("trust: duplicate organization_id %q", org.ID)
		}
		organizations[org.ID] = org
	}
	if len(organizations) == 0 {
		return errors.New("trust: policy requires at least one organization")
	}

	keyIDs := make(map[string]bool, len(p.Keys))
	for _, key := range p.Keys {
		if key.KeyID == "" || key.OrganizationID == "" {
			return errors.New("trust: every key requires key_id and organization_id")
		}
		if keyIDs[key.KeyID] {
			return fmt.Errorf("trust: duplicate key_id %q", key.KeyID)
		}
		keyIDs[key.KeyID] = true
		if _, ok := organizations[key.OrganizationID]; !ok {
			return fmt.Errorf("trust: key %q references unknown organization %q", key.KeyID, key.OrganizationID)
		}
		if key.Algorithm != AlgorithmEd25519 {
			return fmt.Errorf("trust: key %q uses unsupported algorithm %q", key.KeyID, key.Algorithm)
		}
		decoded, err := base64.StdEncoding.DecodeString(key.PublicKey)
		if err != nil || len(decoded) != ed25519.PublicKeySize {
			return fmt.Errorf("trust: key %q public_key must be base64 Ed25519 public key", key.KeyID)
		}
		if len(key.TrustedFor) == 0 {
			return fmt.Errorf("trust: key %q trusted_for must not be empty", key.KeyID)
		}
		seenPurposes := map[string]bool{}
		for _, purpose := range key.TrustedFor {
			if !supportedPurpose(purpose) {
				return fmt.Errorf("trust: key %q has unsupported trusted_for purpose %q", key.KeyID, purpose)
			}
			if seenPurposes[purpose] {
				return fmt.Errorf("trust: key %q repeats trusted_for purpose %q", key.KeyID, purpose)
			}
			seenPurposes[purpose] = true
		}
		if err := validateTimeRange("key "+key.KeyID, key.ValidFrom, key.ValidTo); err != nil {
			return err
		}
		if key.RevokedAt != "" {
			if _, err := time.Parse(time.RFC3339Nano, key.RevokedAt); err != nil {
				return fmt.Errorf("trust: key %q revoked_at must be RFC3339: %w", key.KeyID, err)
			}
		}
	}
	if len(p.Keys) == 0 {
		return errors.New("trust: policy requires at least one trusted key")
	}
	return nil
}

// LoadPolicy reads and strictly validates an operator-controlled public trust
// policy. Unknown JSON fields are rejected so a misspelled security setting
// cannot be silently ignored.
func LoadPolicy(path string) (*Policy, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("trust: read policy %s: %w", path, err)
	}
	var policy Policy
	if err := decodeStrict(raw, &policy); err != nil {
		return nil, fmt.Errorf("trust: decode policy %s: %w", path, err)
	}
	if err := policy.Validate(); err != nil {
		return nil, fmt.Errorf("trust: validate policy %s: %w", path, err)
	}
	return &policy, nil
}

// LoadPrivateKey reads a local Ed25519 key file. The file is a bootstrap
// implementation for a single hospital; signing code consumes the parsed key
// through functions that can later be backed by a KMS/HSM.
func LoadPrivateKey(path string) (*PrivateKeyFile, ed25519.PrivateKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("trust: read private key %s: %w", path, err)
	}
	var keyFile PrivateKeyFile
	if err := decodeStrict(raw, &keyFile); err != nil {
		return nil, nil, fmt.Errorf("trust: decode private key %s: %w", path, err)
	}
	if keyFile.Schema != PrivateKeySchema || keyFile.Algorithm != AlgorithmEd25519 ||
		keyFile.KeyID == "" || keyFile.OrganizationID == "" || keyFile.OrganizationName == "" {
		return nil, nil, fmt.Errorf("trust: invalid private key metadata in %s", path)
	}
	decoded, err := base64.StdEncoding.DecodeString(keyFile.PrivateKey)
	if err != nil || len(decoded) != ed25519.PrivateKeySize {
		return nil, nil, fmt.Errorf("trust: private_key in %s must be base64 Ed25519 private key", path)
	}
	return &keyFile, ed25519.PrivateKey(decoded), nil
}

// GenerateLocalKey returns matching local private-key and public-policy
// records. Callers are responsible for persisting the private part with mode
// 0600 or placing it in a proper secret manager.
func GenerateLocalKey(org Organization, keyID string, trustedFor []string) (PrivateKeyFile, TrustedKey, error) {
	if org.ID == "" || org.Name == "" || keyID == "" {
		return PrivateKeyFile{}, TrustedKey{}, errors.New("trust: organization id/name and key_id are required")
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return PrivateKeyFile{}, TrustedKey{}, fmt.Errorf("trust: generate Ed25519 key: %w", err)
	}
	privateFile := PrivateKeyFile{
		Schema:           PrivateKeySchema,
		KeyID:            keyID,
		OrganizationID:   org.ID,
		OrganizationName: org.Name,
		Algorithm:        AlgorithmEd25519,
		PrivateKey:       base64.StdEncoding.EncodeToString(privateKey),
	}
	publicRecord := TrustedKey{
		KeyID:          keyID,
		OrganizationID: org.ID,
		Algorithm:      AlgorithmEd25519,
		PublicKey:      base64.StdEncoding.EncodeToString(publicKey),
		TrustedFor:     append([]string(nil), trustedFor...),
	}
	return privateFile, publicRecord, nil
}

// BuildSignatureAttestation signs one immutable subject Bead ID and returns
// an unsaved signature_attestation Bead. The signature does not use the legacy
// hash-excluded bead.Signature overlay.
func BuildSignatureAttestation(subjectID, purpose, sourceSystemID string, actor Actor, keyFile PrivateKeyFile, privateKey ed25519.PrivateKey, signedAt string) (bead.Bead, error) {
	parsedSubject, err := bead.ParseID(subjectID)
	if err != nil {
		return bead.Bead{}, fmt.Errorf("trust: signature subject: %w", err)
	}
	if !supportedPurpose(purpose) {
		return bead.Bead{}, fmt.Errorf("trust: unsupported signature purpose %q", purpose)
	}
	if sourceSystemID == "" || actor.ID == "" {
		return bead.Bead{}, errors.New("trust: source_system_id and actor_id are required")
	}
	if _, err := time.Parse(time.RFC3339Nano, signedAt); err != nil {
		return bead.Bead{}, fmt.Errorf("trust: signed_at must be RFC3339: %w", err)
	}
	if keyFile.Schema != PrivateKeySchema || keyFile.Algorithm != AlgorithmEd25519 ||
		keyFile.KeyID == "" || keyFile.OrganizationID == "" || keyFile.OrganizationName == "" {
		return bead.Bead{}, errors.New("trust: invalid private key metadata")
	}
	if len(privateKey) != ed25519.PrivateKeySize {
		return bead.Bead{}, errors.New("trust: invalid Ed25519 private key length")
	}

	statement := SigningStatement{
		Schema:           SigningStatementSchema,
		Purpose:          purpose,
		SubjectBeadID:    parsedSubject,
		OrganizationID:   keyFile.OrganizationID,
		OrganizationName: keyFile.OrganizationName,
		SourceSystemID:   sourceSystemID,
		Actor:            actor,
		KeyID:            keyFile.KeyID,
		Algorithm:        AlgorithmEd25519,
		SignedAt:         signedAt,
	}
	message, err := canonicalStatement(statement)
	if err != nil {
		return bead.Bead{}, err
	}
	signature := ed25519.Sign(privateKey, message)
	content, err := structToMap(signatureAttestationContent{
		Schema:    SignatureAttestationSchema,
		Statement: statement,
		Signature: signatureEnvelope{
			Encoding: "base64",
			Value:    base64.StdEncoding.EncodeToString(signature),
		},
	})
	if err != nil {
		return bead.Bead{}, fmt.Errorf("trust: build signature content: %w", err)
	}
	return bead.Bead{
		Type:      SignatureAttestationType,
		Timestamp: signedAt,
		Author:    actor.ID,
		Parents:   []string{parsedSubject},
		Content:   content,
	}, nil
}

// VerifySignatureAttestation verifies attribution, trust purpose, key state
// and the Ed25519 signature at the supplied decision time.
func VerifySignatureAttestation(b bead.Bead, policy Policy, at time.Time) (VerifiedAttestation, error) {
	if err := policy.Validate(); err != nil {
		return VerifiedAttestation{}, err
	}
	if err := bead.Verify(b); err != nil {
		return VerifiedAttestation{}, fmt.Errorf("trust: signature attestation content hash: %w", err)
	}
	if b.Type != SignatureAttestationType {
		return VerifiedAttestation{}, fmt.Errorf("trust: Bead %s has type %q, want %q", b.ID, b.Type, SignatureAttestationType)
	}
	raw, err := json.Marshal(b.Content)
	if err != nil {
		return VerifiedAttestation{}, fmt.Errorf("trust: encode attestation content: %w", err)
	}
	var content signatureAttestationContent
	if err := decodeStrict(raw, &content); err != nil {
		return VerifiedAttestation{}, fmt.Errorf("trust: decode signature attestation %s: %w", b.ID, err)
	}
	statement := content.Statement
	if content.Schema != SignatureAttestationSchema || statement.Schema != SigningStatementSchema {
		return VerifiedAttestation{}, fmt.Errorf("trust: unsupported signature attestation schema in %s", b.ID)
	}
	if !supportedPurpose(statement.Purpose) || statement.Algorithm != AlgorithmEd25519 {
		return VerifiedAttestation{}, fmt.Errorf("trust: unsupported purpose/algorithm in attestation %s", b.ID)
	}
	if _, err := bead.ParseID(statement.SubjectBeadID); err != nil {
		return VerifiedAttestation{}, fmt.Errorf("trust: attestation %s subject: %w", b.ID, err)
	}
	if !contains(b.Parents, statement.SubjectBeadID) {
		return VerifiedAttestation{}, fmt.Errorf("trust: attestation %s must name subject %s in parents", b.ID, statement.SubjectBeadID)
	}
	if statement.Actor.ID == "" || b.Author != statement.Actor.ID || b.Timestamp != statement.SignedAt {
		return VerifiedAttestation{}, fmt.Errorf("trust: attestation %s author/timestamp do not match signed actor and signed_at", b.ID)
	}
	signedAt, err := time.Parse(time.RFC3339Nano, statement.SignedAt)
	if err != nil {
		return VerifiedAttestation{}, fmt.Errorf("trust: attestation %s signed_at: %w", b.ID, err)
	}
	if signedAt.After(at) {
		return VerifiedAttestation{}, fmt.Errorf("trust: attestation %s is dated in the future", b.ID)
	}
	key, err := policy.keyFor(statement.KeyID, statement.Purpose, at)
	if err != nil {
		return VerifiedAttestation{}, fmt.Errorf("trust: attestation %s: %w", b.ID, err)
	}
	if key.OrganizationID != statement.OrganizationID {
		return VerifiedAttestation{}, fmt.Errorf("trust: attestation %s key organization mismatch", b.ID)
	}
	if statement.OrganizationName == "" || statement.SourceSystemID == "" {
		return VerifiedAttestation{}, fmt.Errorf("trust: attestation %s organization_name and source_system_id are required", b.ID)
	}
	if err := key.validAtSigningTime(signedAt); err != nil {
		return VerifiedAttestation{}, fmt.Errorf("trust: attestation %s: %w", b.ID, err)
	}
	if content.Signature.Encoding != "base64" {
		return VerifiedAttestation{}, fmt.Errorf("trust: attestation %s signature encoding must be base64", b.ID)
	}
	publicKey, _ := base64.StdEncoding.DecodeString(key.PublicKey) // policy.Validate already checked
	signature, err := base64.StdEncoding.DecodeString(content.Signature.Value)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return VerifiedAttestation{}, fmt.Errorf("trust: attestation %s has invalid Ed25519 signature encoding", b.ID)
	}
	message, err := canonicalStatement(statement)
	if err != nil {
		return VerifiedAttestation{}, err
	}
	if !ed25519.Verify(ed25519.PublicKey(publicKey), message, signature) {
		return VerifiedAttestation{}, fmt.Errorf("trust: attestation %s signature verification failed", b.ID)
	}
	return VerifiedAttestation{BeadID: b.ID, Statement: statement}, nil
}

func (p Policy) keyFor(keyID, purpose string, at time.Time) (TrustedKey, error) {
	for _, key := range p.Keys {
		if key.KeyID != keyID {
			continue
		}
		if !contains(key.TrustedFor, purpose) {
			return TrustedKey{}, fmt.Errorf("key %q is not trusted for %q", keyID, purpose)
		}
		if key.RevokedAt != "" {
			revokedAt, _ := time.Parse(time.RFC3339Nano, key.RevokedAt)
			if !at.Before(revokedAt) {
				return TrustedKey{}, fmt.Errorf("key %q was revoked at %s", keyID, key.RevokedAt)
			}
		}
		return key, nil
	}
	return TrustedKey{}, fmt.Errorf("untrusted key_id %q", keyID)
}

func (key TrustedKey) validAtSigningTime(signedAt time.Time) error {
	if key.ValidFrom != "" {
		from, _ := time.Parse(time.RFC3339Nano, key.ValidFrom)
		if signedAt.Before(from) {
			return fmt.Errorf("key %q was not valid at signed_at", key.KeyID)
		}
	}
	if key.ValidTo != "" {
		to, _ := time.Parse(time.RFC3339Nano, key.ValidTo)
		if signedAt.After(to) {
			return fmt.Errorf("key %q was expired at signed_at", key.KeyID)
		}
	}
	return nil
}

func canonicalStatement(statement SigningStatement) ([]byte, error) {
	raw, err := json.Marshal(statement)
	if err != nil {
		return nil, fmt.Errorf("trust: marshal signing statement: %w", err)
	}
	canonical, err := jcs.Transform(raw)
	if err != nil {
		return nil, fmt.Errorf("trust: canonicalize signing statement: %w", err)
	}
	return canonical, nil
}

func supportedPurpose(purpose string) bool {
	return purpose == PurposeClinicalOrigin || purpose == PurposeFHIRImport || purpose == PurposeKnowledgeRelease
}

func validateTimeRange(name, fromRaw, toRaw string) error {
	var from, to time.Time
	var err error
	if fromRaw != "" {
		from, err = time.Parse(time.RFC3339Nano, fromRaw)
		if err != nil {
			return fmt.Errorf("trust: %s valid_from must be RFC3339: %w", name, err)
		}
	}
	if toRaw != "" {
		to, err = time.Parse(time.RFC3339Nano, toRaw)
		if err != nil {
			return fmt.Errorf("trust: %s valid_to must be RFC3339: %w", name, err)
		}
	}
	if !from.IsZero() && !to.IsZero() && to.Before(from) {
		return fmt.Errorf("trust: %s valid_to precedes valid_from", name)
	}
	return nil
}

func decodeStrict(raw []byte, dst any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}

func structToMap(value any) (map[string]any, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func normalizedIDs(ids []string) ([]string, error) {
	out := make([]string, 0, len(ids))
	seen := map[string]bool{}
	for _, raw := range ids {
		id, err := bead.ParseID(raw)
		if err != nil {
			return nil, err
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out, nil
}
