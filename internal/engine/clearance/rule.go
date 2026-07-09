package clearance

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/medbeads/medbeads/internal/engine/index"
)

// Rule defines DB-backed access restrictions for a Bead, ported from
// v2.2.0's core/types.ClearanceRule. It supports a hybrid model: DeniedRoles
// is a blacklist (those roles are blocked), and the optional AllowedRoles is
// a whitelist (when non-empty, ONLY those roles may access the bead). The
// roles RoleSystem/RoleEmergency always bypass both (see HasAccessWithRules).
type Rule struct {
	ID           string
	BeadID       string
	DeniedRoles  []string
	AllowedRoles []string // empty/nil = no whitelist
	CreatedBy    string
	CreatedAt    string
	Reason       string
	ExpiresAt    *string // nil = permanent
}

// SaveRule saves a clearance rule to clearance_rules (INSERT OR REPLACE by
// ID, mirroring v2's SaveClearanceRule). An unset (empty) AllowedRoles is
// stored as SQL NULL, not an empty JSON array, so GetRules can distinguish
// "no whitelist" from "whitelist of zero roles" (both behave the same in
// HasAccessWithRules today, but NULL is the more honest representation of
// "unset").
func SaveRule(db *index.DB, rule Rule) error {
	deniedRolesJSON, err := json.Marshal(rule.DeniedRoles)
	if err != nil {
		return fmt.Errorf("clearance: save rule %s: marshal denied_roles: %w", rule.ID, err)
	}

	var allowedRolesArg any
	if len(rule.AllowedRoles) > 0 {
		allowedRolesJSON, err := json.Marshal(rule.AllowedRoles)
		if err != nil {
			return fmt.Errorf("clearance: save rule %s: marshal allowed_roles: %w", rule.ID, err)
		}
		allowedRolesArg = string(allowedRolesJSON)
	}

	_, err = db.SQLDB().Exec(
		`INSERT OR REPLACE INTO clearance_rules (id, bead_id, denied_roles, allowed_roles, created_by, created_at, reason, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		rule.ID, rule.BeadID, string(deniedRolesJSON), allowedRolesArg, rule.CreatedBy, rule.CreatedAt, rule.Reason, rule.ExpiresAt,
	)
	if err != nil {
		return fmt.Errorf("clearance: save rule %s: %w", rule.ID, err)
	}
	return nil
}

// GetRules retrieves all clearance_rules rows for a single Bead ID.
func GetRules(db *index.DB, beadID string) ([]Rule, error) {
	rows, err := db.SQLDB().Query(
		`SELECT id, bead_id, denied_roles, allowed_roles, created_by, created_at, reason, expires_at
		 FROM clearance_rules WHERE bead_id = ?`, beadID)
	if err != nil {
		return nil, fmt.Errorf("clearance: get rules %s: %w", beadID, err)
	}
	defer rows.Close()

	var out []Rule
	for rows.Next() {
		rule, err := scanRule(rows)
		if err != nil {
			return nil, fmt.Errorf("clearance: get rules %s: scan: %w", beadID, err)
		}
		out = append(out, rule)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("clearance: get rules %s: %w", beadID, err)
	}
	return out, nil
}

// GetRulesForBeads retrieves clearance_rules rows for multiple Bead IDs in a
// single query (v2's GetAllClearanceRulesForBeads), keyed by bead_id, so
// bulk callers (FilterByAccess) never pay one query per Bead.
func GetRulesForBeads(db *index.DB, beadIDs []string) (map[string][]Rule, error) {
	result := make(map[string][]Rule)
	if len(beadIDs) == 0 {
		return result, nil
	}

	placeholders := make([]string, len(beadIDs))
	args := make([]any, len(beadIDs))
	for i, id := range beadIDs {
		placeholders[i] = "?"
		args[i] = id
	}

	query := fmt.Sprintf(
		`SELECT id, bead_id, denied_roles, allowed_roles, created_by, created_at, reason, expires_at
		 FROM clearance_rules WHERE bead_id IN (%s)`, strings.Join(placeholders, ","))
	rows, err := db.SQLDB().Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("clearance: get rules for %d beads: %w", len(beadIDs), err)
	}
	defer rows.Close()

	for rows.Next() {
		rule, err := scanRule(rows)
		if err != nil {
			return nil, fmt.Errorf("clearance: get rules for %d beads: scan: %w", len(beadIDs), err)
		}
		result[rule.BeadID] = append(result[rule.BeadID], rule)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("clearance: get rules for %d beads: %w", len(beadIDs), err)
	}
	return result, nil
}

// DeleteRule deletes a clearance rule by its own ID.
func DeleteRule(db *index.DB, ruleID string) error {
	if _, err := db.SQLDB().Exec(`DELETE FROM clearance_rules WHERE id = ?`, ruleID); err != nil {
		return fmt.Errorf("clearance: delete rule %s: %w", ruleID, err)
	}
	return nil
}

// scanRule scans one clearance_rules row (column order: id, bead_id,
// denied_roles, allowed_roles, created_by, created_at, reason, expires_at).
func scanRule(rows *sql.Rows) (Rule, error) {
	var rule Rule
	var deniedRolesStr string
	var allowedRolesStr sql.NullString
	var reason sql.NullString
	var expiresAt sql.NullString

	if err := rows.Scan(&rule.ID, &rule.BeadID, &deniedRolesStr, &allowedRolesStr, &rule.CreatedBy, &rule.CreatedAt, &reason, &expiresAt); err != nil {
		return Rule{}, err
	}
	if err := json.Unmarshal([]byte(deniedRolesStr), &rule.DeniedRoles); err != nil {
		return Rule{}, fmt.Errorf("unmarshal denied_roles: %w", err)
	}
	if allowedRolesStr.Valid && allowedRolesStr.String != "" {
		// Best-effort, matching v2: a malformed allowed_roles value degrades
		// to "no whitelist" rather than failing the whole row scan.
		_ = json.Unmarshal([]byte(allowedRolesStr.String), &rule.AllowedRoles)
	}
	if reason.Valid {
		rule.Reason = reason.String
	}
	if expiresAt.Valid {
		rule.ExpiresAt = &expiresAt.String
	}
	return rule, nil
}
