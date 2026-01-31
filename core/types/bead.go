package types

// Bead is the fundamental data unit in MedBeads.
type Bead struct {
	ID        string                 `json:"id,omitempty"`
	Type      string                 `json:"type"`
	Timestamp string                 `json:"timestamp"`
	Parents   []string               `json:"parents"`
	Content   map[string]interface{} `json:"content"`
}

// Security Clearance Roles
const (
	RolePatient     = "patient"      // 患者本人
	RoleFamily      = "family"       // 家族
	RolePrimaryCare = "primary_care" // 主治医
	RoleSpecialist  = "specialist"   // 専門医
	RoleNurse       = "nurse"        // 看護師
	RoleInsurance   = "insurance"    // 保険会社
	RoleResearcher  = "researcher"   // 研究者
	RoleEmergency   = "emergency"    // 緊急時オーバーライド
	RoleSystem      = "system"       // システム/AI
)

// AllRoles returns all available roles
var AllRoles = []string{
	RolePatient,
	RoleFamily,
	RolePrimaryCare,
	RoleSpecialist,
	RoleNurse,
	RoleInsurance,
	RoleResearcher,
	RoleEmergency,
	RoleSystem,
}

// ClearanceRule defines access restrictions for a Bead (Blacklist model)
type ClearanceRule struct {
	ID          string   `json:"id"`
	BeadID      string   `json:"bead_id"`
	DeniedRoles []string `json:"denied_roles"` // Roles that are blocked from accessing this bead
	CreatedBy   string   `json:"created_by"`
	CreatedAt   string   `json:"created_at"`
	Reason      string   `json:"reason,omitempty"`
	ExpiresAt   *string  `json:"expires_at,omitempty"` // nil = permanent
}

// ViewerContext represents the current viewer's access context
type ViewerContext struct {
	UserID    string   `json:"user_id"`
	Roles     []string `json:"roles"`
	PatientID string   `json:"patient_id,omitempty"` // The patient ID if the viewer is related to a specific patient
}
