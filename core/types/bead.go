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

// DeptRolePrefix is the namespace prefix for department-scoped roles, e.g.
// "dept:psychiatry". Department roles are an axis orthogonal to the functional
// roles above: a viewer may hold both (e.g. "specialist" and "dept:psychiatry").
const DeptRolePrefix = "dept:"

// Department describes a clinical department that can scope a clearance role.
type Department struct {
	Value   string `json:"value"`    // identifier used in the dept:<value> role string
	LabelJa string `json:"label_ja"` // Japanese display label
}

// Departments is the list of recognized clinical departments. A department
// role string is DeptRolePrefix + Value (e.g. "dept:genetics").
var Departments = []Department{
	{Value: "psychiatry", LabelJa: "精神科"},
	{Value: "obstetrics_gynecology", LabelJa: "産婦人科"},
	{Value: "genetics", LabelJa: "遺伝診療科"},
	{Value: "oncology", LabelJa: "腫瘍内科"},
	{Value: "cardiology", LabelJa: "循環器内科"},
	{Value: "radiology", LabelJa: "放射線科"},
	{Value: "general_medicine", LabelJa: "総合診療科"},
}

// IsValidRole reports whether role is a recognized clearance role: either one
// of the functional roles in AllRoles, or a department-scoped role of the form
// "dept:<known department>".
func IsValidRole(role string) bool {
	for _, r := range AllRoles {
		if r == role {
			return true
		}
	}
	for _, d := range Departments {
		if DeptRolePrefix+d.Value == role {
			return true
		}
	}
	return false
}

// ClearanceRule defines access restrictions for a Bead. It supports a hybrid
// model: DeniedRoles is a blacklist (those roles are blocked), and the optional
// AllowedRoles is a whitelist (when non-empty, ONLY those roles may access the
// bead). The roles `system`/`emergency` always bypass both.
type ClearanceRule struct {
	ID           string   `json:"id"`
	BeadID       string   `json:"bead_id"`
	DeniedRoles  []string `json:"denied_roles"`            // Roles blocked from this bead
	AllowedRoles []string `json:"allowed_roles,omitempty"` // If non-empty, ONLY these roles may access
	CreatedBy    string   `json:"created_by"`
	CreatedAt    string   `json:"created_at"`
	Reason       string   `json:"reason,omitempty"`
	ExpiresAt    *string  `json:"expires_at,omitempty"` // nil = permanent
}

// ViewerContext represents the current viewer's access context
type ViewerContext struct {
	UserID    string   `json:"user_id"`
	Roles     []string `json:"roles"`
	PatientID string   `json:"patient_id,omitempty"` // The patient ID if the viewer is related to a specific patient
}
