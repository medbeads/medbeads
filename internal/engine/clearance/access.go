package clearance

import (
	"time"

	"github.com/medbeads/medbeads/internal/engine/bead"
	"github.com/medbeads/medbeads/internal/engine/index"
)

// now is the wall-clock source for IsRuleActive's default (nil) time
// argument and is a var (not a direct time.Now() call) so tests can override
// it — mirroring package apc's nowRFC3339 mockable-clock convention
// (internal/engine/apc/link.go).
var now = time.Now

// IsRuleActive reports whether a clearance rule is currently in effect, i.e.
// it has no expiry or its expiry is in the future as of t. An unparseable
// expiry is treated as active (fail-closed for security). Ported verbatim
// from v2.2.0's core/store.IsRuleActive.
func IsRuleActive(rule Rule, t time.Time) bool {
	if rule.ExpiresAt == nil || *rule.ExpiresAt == "" {
		return true
	}
	expiresAt, err := parseTime(*rule.ExpiresAt)
	if err != nil {
		return true
	}
	return !t.After(expiresAt)
}

// parseTime parses a clearance rule's expires_at string against the same
// format set v2.2.0's core/store.parseTime accepted.
func parseTime(s string) (time.Time, error) {
	formats := []string{
		time.RFC3339,
		"2006-01-02T15:04:05Z",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}
	var lastErr error
	for _, format := range formats {
		if t, err := time.Parse(format, s); err == nil {
			return t, nil
		} else {
			lastErr = err
		}
	}
	return time.Time{}, lastErr
}

// embeddedRule converts a Bead's own embedded bead.Clearance overlay (if
// set) into a Rule, so HasAccessWithRules can evaluate it via the exact same
// blacklist/whitelist logic as a DB clearance_rules row — this is the "two
// layers, one evaluation" design (see doc.go). A nil c yields ok=false (no
// embedded rule to add).
func embeddedRule(beadID string, c *bead.Clearance) (rule Rule, ok bool) {
	if c == nil {
		return Rule{}, false
	}
	if len(c.DeniedRoles) == 0 && len(c.AllowedRoles) == 0 {
		// An embedded Clearance with neither list set carries no access
		// restriction (e.g. Reason/ExpiresAt alone, with no lists, would be
		// a no-op rule) — do not manufacture a vacuous constraint.
		return Rule{}, false
	}
	return Rule{
		BeadID:       beadID,
		DeniedRoles:  c.DeniedRoles,
		AllowedRoles: c.AllowedRoles,
		ExpiresAt:    c.ExpiresAt,
	}, true
}

// HasAccess checks whether a viewer holding viewerRoles may access the Bead
// identified by beadID, combining its DB clearance_rules rows with its own
// embedded bead.Clearance overlay (see doc.go). Ported from v2.2.0's
// core/store.HasAccess, extended to also fetch b's embedded Clearance.
func HasAccess(db *index.DB, b bead.Bead, viewerRoles []string) (bool, error) {
	for _, role := range viewerRoles {
		if role == RoleEmergency || role == RoleSystem {
			return true, nil
		}
	}

	dbRules, err := GetRules(db, b.ID)
	if err != nil {
		return false, err
	}

	rules := dbRules
	if r, ok := embeddedRule(b.ID, b.Clearance); ok {
		rules = append(rules, r)
	}

	if len(rules) == 0 {
		return true, nil
	}

	return HasAccessWithRules(rules, viewerRoles), nil
}

// HasAccessWithRules checks access using a pre-fetched rule set (for
// efficiency with bulk operations, e.g. FilterByAccess). Ported verbatim
// from v2.2.0's core/store.HasAccessWithRules.
func HasAccessWithRules(rules []Rule, viewerRoles []string) bool {
	// Emergency role always has access.
	for _, role := range viewerRoles {
		if role == RoleEmergency || role == RoleSystem {
			return true
		}
	}

	// No rules = no restrictions (Blacklist model).
	if len(rules) == 0 {
		return true
	}

	t := now()

	// A viewer with no identified role may not view any bead that has an
	// active clearance rule. This closes the bypass where omitting the
	// viewer-roles context would otherwise expose restricted data.
	if len(viewerRoles) == 0 {
		for _, rule := range rules {
			if IsRuleActive(rule, t) {
				return false
			}
		}
		return true
	}

	// Check each active rule. Each rule is an independent constraint; the
	// viewer must satisfy all of them.
	for _, rule := range rules {
		if !IsRuleActive(rule, t) {
			continue
		}

		// Blacklist: any of the viewer's roles being denied blocks access.
		for _, viewerRole := range viewerRoles {
			for _, deniedRole := range rule.DeniedRoles {
				if viewerRole == deniedRole {
					return false
				}
			}
		}

		// Whitelist: when allowed_roles is set, the viewer must hold at
		// least one of those roles, otherwise this rule blocks access.
		if len(rule.AllowedRoles) > 0 && !rolesIntersect(viewerRoles, rule.AllowedRoles) {
			return false
		}
	}

	return true
}

// rolesIntersect reports whether the two role slices share at least one
// role.
func rolesIntersect(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}

// FilterByAccess masks the content of Beads the viewer is not allowed to
// see, combining each Bead's DB clearance_rules rows with its own embedded
// bead.Clearance overlay. A restricted Bead keeps its ID/Type/Timestamp/
// Parents (so the DAG structure and a future "locked node" UI rendering
// stay intact) but its Content is replaced with {"_restricted": true} and
// its Clearance/Antigens/Evidence/Author/Signature are dropped. Accessible
// Beads are returned unchanged. Ported from v2.2.0's
// core/store.FilterByAccess.
func FilterByAccess(db *index.DB, beads []bead.Bead, viewerRoles []string) ([]bead.Bead, error) {
	if len(beads) == 0 {
		return beads, nil
	}

	beadIDs := make([]string, len(beads))
	for i, b := range beads {
		beadIDs[i] = b.ID
	}

	rulesMap, err := GetRulesForBeads(db, beadIDs)
	if err != nil {
		return nil, err
	}

	result := make([]bead.Bead, len(beads))
	for i, b := range beads {
		rules := rulesMap[b.ID]
		if r, ok := embeddedRule(b.ID, b.Clearance); ok {
			rules = append(rules, r)
		}

		if HasAccessWithRules(rules, viewerRoles) {
			result[i] = b
			continue
		}
		// Mask the content but preserve graph metadata.
		result[i] = bead.Bead{
			ID:        b.ID,
			Type:      b.Type,
			Timestamp: b.Timestamp,
			Parents:   b.Parents,
			Content:   map[string]any{"_restricted": true},
		}
	}
	return result, nil
}
