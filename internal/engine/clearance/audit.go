package clearance

import (
	"encoding/json"
	"fmt"

	"github.com/medbeads/medbeads/internal/engine/index"
)

// LogAction records one clearance-related action to clearance_audit, for
// audit purposes. Ported from v2.2.0's core/store.LogClearanceAction; v2
// relied on the column's DATETIME DEFAULT CURRENT_TIMESTAMP, but this
// project's convention (see internal/engine/apc's nowRFC3339-based
// scanned_at) is to have the caller supply an explicit RFC3339 timestamp
// rather than a SQLite-side default, so every other column in this table can
// be reconstructed/tested deterministically.
//
// v3.0 scope note (docs/requirements.md §8, doc.go): this is the minimal
// "who evaluated what" write path only — there is no query/report API over
// clearance_audit yet (that is M4's "監査完備").
func LogAction(db *index.DB, beadID, action, userID string, userRoles []string, details, timestamp string) error {
	rolesJSON, err := json.Marshal(userRoles)
	if err != nil {
		return fmt.Errorf("clearance: log action %s/%s: marshal user_roles: %w", beadID, action, err)
	}

	_, err = db.SQLDB().Exec(
		`INSERT INTO clearance_audit (bead_id, action, user_id, user_roles, timestamp, details) VALUES (?, ?, ?, ?, ?, ?)`,
		beadID, action, userID, string(rolesJSON), timestamp, details,
	)
	if err != nil {
		return fmt.Errorf("clearance: log action %s/%s: %w", beadID, action, err)
	}
	return nil
}
