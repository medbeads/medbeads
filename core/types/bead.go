package types

// Bead is the fundamental data unit in MedBeads.
type Bead struct {
	ID        string                 `json:"id,omitempty"`
	Type      string                 `json:"type"`
	Timestamp string                 `json:"timestamp"`
	Parents   []string               `json:"parents"`
	Content   map[string]interface{} `json:"content"`
}
