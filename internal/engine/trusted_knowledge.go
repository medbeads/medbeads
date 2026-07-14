package engine

import (
	"fmt"
	"sort"
	"time"

	"github.com/medbeads/medbeads/internal/engine/trust"
)

// TrustedKnowledgeActivation combines cryptographic release verification with
// the existing activity-prioritized rolling activation result.
type TrustedKnowledgeActivation struct {
	Rolling    RollingActivation
	Validation trust.ReleaseValidation
}

// ActivateKnowledgeRelease is the safe high-level activation path when a
// trust policy is configured. Every link_rule in knowledgeBeadIDs must be
// covered by one effective knowledge_release and the policy-required number
// of valid signature_attestation Beads. The lower-level ActivateLinkKnowledge
// also enforces this invariant whenever RequireKnowledgeRelease is enabled;
// this method additionally returns the verified release details.
func (e *Engine) ActivateKnowledgeRelease(knowledgeBeadIDs []string, codeVersion, builtAt string, at time.Time) (TrustedKnowledgeActivation, error) {
	if e.trustPolicy == nil {
		return TrustedKnowledgeActivation{}, fmt.Errorf("engine: activate knowledge release: no trust policy configured")
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	ids := append([]string(nil), knowledgeBeadIDs...)
	sort.Strings(ids)
	ids = deduplicateStrings(ids)
	validation, err := trust.ValidateKnowledgeSet(e, ids, *e.trustPolicy, at)
	if err != nil {
		return TrustedKnowledgeActivation{}, fmt.Errorf("engine: activate knowledge release: %w", err)
	}
	rolling, err := e.ActivateLinkKnowledge(ids, codeVersion, builtAt)
	if err != nil {
		return TrustedKnowledgeActivation{}, err
	}
	return TrustedKnowledgeActivation{Rolling: rolling, Validation: validation}, nil
}

// TrustPolicy returns a copy of the public policy configured for this Engine.
// It never contains private key material.
func (e *Engine) TrustPolicy() (*trust.Policy, bool) {
	if e.trustPolicy == nil {
		return nil, false
	}
	copy := e.trustPolicy.Clone()
	return &copy, true
}
