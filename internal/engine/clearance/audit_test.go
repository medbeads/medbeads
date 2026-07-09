package clearance_test

import (
	"testing"

	"github.com/medbeads/medbeads/internal/engine/clearance"
)

// TestLogAction_WritesAuditRow checks the minimal M1 audit write path
// (doc.go's v3.0 scope note): LogAction inserts one clearance_audit row,
// readable back via a direct SQL query (there is no query API of this
// package's own yet — that is M4 scope).
func TestLogAction_WritesAuditRow(t *testing.T) {
	e := openT(t)

	patient := seedPatient(t, e, "Patient")

	if err := clearance.LogAction(e.Index(), patient.ID, "view", "user-1", []string{"primary_care"}, "viewed via MCP retrieve", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatalf("LogAction: %v", err)
	}

	var (
		beadID, action, userID, userRoles, timestamp, details string
	)
	row := e.Index().SQLDB().QueryRow(
		`SELECT bead_id, action, user_id, user_roles, timestamp, details FROM clearance_audit WHERE bead_id = ?`,
		patient.ID,
	)
	if err := row.Scan(&beadID, &action, &userID, &userRoles, &timestamp, &details); err != nil {
		t.Fatalf("scan clearance_audit row: %v", err)
	}

	if beadID != patient.ID {
		t.Errorf("bead_id = %q, want %q", beadID, patient.ID)
	}
	if action != "view" {
		t.Errorf("action = %q, want %q", action, "view")
	}
	if userID != "user-1" {
		t.Errorf("user_id = %q, want %q", userID, "user-1")
	}
	if userRoles != `["primary_care"]` {
		t.Errorf("user_roles = %q, want %q", userRoles, `["primary_care"]`)
	}
	if timestamp != "2026-01-01T00:00:00Z" {
		t.Errorf("timestamp = %q, want %q", timestamp, "2026-01-01T00:00:00Z")
	}
	if details != "viewed via MCP retrieve" {
		t.Errorf("details = %q, want %q", details, "viewed via MCP retrieve")
	}
}
