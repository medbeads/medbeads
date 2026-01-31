package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sojin25/medbeads/core/types"

	_ "github.com/mattn/go-sqlite3" // SQLite Driver
)

const (
	StorageDir = "./medbeads_data/objects"
	DBSource   = "./medbeads_data/metadata.db"
)

var DB *sql.DB

// EnsureStorageDir ensures the storage directory exists.
func EnsureStorageDir() error {
	return os.MkdirAll(StorageDir, 0755)
}

// InitDB initializes SQLite database and creates the necessary tables including FTS.
func InitDB() error {
	var err error
	DB, err = sql.Open("sqlite3", DBSource)
	if err != nil {
		return err
	}

	// Performance & Concurrency Tuning
	if _, err := DB.Exec("PRAGMA journal_mode=WAL;"); err != nil {
		return fmt.Errorf("failed to enable WAL mode: %w", err)
	}
	if _, err := DB.Exec("PRAGMA busy_timeout=5000;"); err != nil { // 5000ms
		return fmt.Errorf("failed to set busy timeout: %w", err)
	}
	if _, err := DB.Exec("PRAGMA synchronous=NORMAL;"); err != nil {
		return fmt.Errorf("failed to set synchronous mode: %w", err)
	}

	// 1. Main Metadata Table
	query := `
	CREATE TABLE IF NOT EXISTS beads (
		id TEXT PRIMARY KEY,
		type TEXT NOT NULL,
		timestamp TEXT NOT NULL,
		parents TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		content_text TEXT
	);
	CREATE INDEX IF NOT EXISTS idx_type ON beads(type);
	CREATE INDEX IF NOT EXISTS idx_timestamp ON beads(timestamp);
	`
	if _, err := DB.Exec(query); err != nil {
		return fmt.Errorf("failed to create base table: %w", err)
	}

	// 2. FTS Virtual Table for Full Text Search
	// We specifically look for fts5, fallback to fts4/3 logic implied if needed,
	// but standard go-sqlite3 usually supports fts5.
	ftsQuery := `
	CREATE VIRTUAL TABLE IF NOT EXISTS beads_fts USING fts5(id UNINDEXED, content);
	`
	if _, err := DB.Exec(ftsQuery); err != nil {
		fmt.Printf("⚠️ FTS5 creation failed: %v. Trying FTS4...\n", err)
		ftsQuery = `CREATE VIRTUAL TABLE IF NOT EXISTS beads_fts USING fts4(id, content);`
		if _, err := DB.Exec(ftsQuery); err != nil {
			return fmt.Errorf("failed to create FTS table: %w", err)
		}
	}

	// 3. Edges Table for Graph Traversal (Performance Optimization)
	// Maps child_id -> parent_id for fast reverse lookups
	edgeQuery := `
	CREATE TABLE IF NOT EXISTS bead_edges (
		child_id TEXT NOT NULL,
		parent_id TEXT NOT NULL,
		PRIMARY KEY (child_id, parent_id)
	);
	CREATE INDEX IF NOT EXISTS idx_edge_parent ON bead_edges(parent_id);
	`
	if _, err := DB.Exec(edgeQuery); err != nil {
		return fmt.Errorf("failed to create edge table: %w", err)
	}

	fmt.Println("🗄️  SQLite Metadata Index & FTS initialized.")

	// Migration: Ensure content_text exists (for existing DBs)
	_, err = DB.Exec("ALTER TABLE beads ADD COLUMN content_text TEXT")
	if err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		// Ignore if column already exists
		fmt.Printf("ℹ️ Column check: %v\n", err)
	}

	// 4. Clearance Rules Table (Security Clearance feature)
	clearanceQuery := `
	CREATE TABLE IF NOT EXISTS clearance_rules (
		id TEXT PRIMARY KEY,
		bead_id TEXT NOT NULL,
		denied_roles TEXT NOT NULL,
		created_by TEXT NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		reason TEXT,
		expires_at DATETIME
	);
	CREATE INDEX IF NOT EXISTS idx_clearance_bead ON clearance_rules(bead_id);
	`
	if _, err := DB.Exec(clearanceQuery); err != nil {
		return fmt.Errorf("failed to create clearance_rules table: %w", err)
	}

	// 5. Clearance Audit Log Table
	auditQuery := `
	CREATE TABLE IF NOT EXISTS clearance_audit (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		bead_id TEXT NOT NULL,
		action TEXT NOT NULL,
		user_id TEXT NOT NULL,
		user_roles TEXT NOT NULL,
		timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,
		details TEXT
	);
	CREATE INDEX IF NOT EXISTS idx_audit_bead ON clearance_audit(bead_id);
	`
	if _, err := DB.Exec(auditQuery); err != nil {
		return fmt.Errorf("failed to create clearance_audit table: %w", err)
	}

	fmt.Println("🔒 Security Clearance tables initialized.")

	return nil
}

// SaveToCAS saves a Bead to CAS and indexes its metadata in SQLite.
func SaveToCAS(b types.Bead) (string, error) {
	// 1. Serialize and Save to CAS
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(data)
	hashString := hex.EncodeToString(hash[:])
	filePath := filepath.Join(StorageDir, hashString)

	if err := os.WriteFile(filePath, data, 0644); err != nil {
		return "", err
	}

	// 2. Index Metadata in SQLite
	err = indexMetadata(hashString, b)
	if err != nil {
		fmt.Printf("⚠️ Warning: Failed to index metadata for %s: %v\n", hashString, err)
	}

	return hashString, nil
}

// indexMetadata inserts the bead metadata into SQLite (Main + FTS).
func indexMetadata(id string, b types.Bead) error {
	parentsJSON, _ := json.Marshal(b.Parents)
	contentJSON, _ := json.Marshal(b.Content)
	contentStr := string(contentJSON)

	// Transaction to ensure consistency
	tx, err := DB.Begin()
	if err != nil {
		return err
	}

	// 1. Main Table
	query := `INSERT OR REPLACE INTO beads (id, type, timestamp, parents, content_text) VALUES (?, ?, ?, ?, ?)`
	if _, err := tx.Exec(query, id, b.Type, b.Timestamp, string(parentsJSON), contentStr); err != nil {
		tx.Rollback()
		return err
	}

	// 2. FTS Table (Delete old then Insert new to handle updates)
	if _, err := tx.Exec("DELETE FROM beads_fts WHERE id = ?", id); err != nil {
		tx.Rollback()
		return err
	}
	if _, err := tx.Exec("INSERT INTO beads_fts (id, content) VALUES (?, ?)", id, contentStr); err != nil {
		tx.Rollback()
		return err
	}

	// 3. Edges Table
	// Remove old edges first (in case of update)
	if _, err := tx.Exec("DELETE FROM bead_edges WHERE child_id = ?", id); err != nil {
		tx.Rollback()
		return err
	}
	// Insert new edges
	for _, parentID := range b.Parents {
		if _, err := tx.Exec("INSERT INTO bead_edges (child_id, parent_id) VALUES (?, ?)", id, parentID); err != nil {
			tx.Rollback()
			return err
		}
	}

	return tx.Commit()
}

// GetFromCAS retrieves raw Bead data from the Content Addressable Storage by ID.
func GetFromCAS(id string) ([]byte, error) {
	filePath := filepath.Join(StorageDir, id)
	return os.ReadFile(filePath)
}

// LoadFromCAS loads a Bead from CAS by ID
func LoadFromCAS(id string) (types.Bead, error) {
	var bead types.Bead
	data, err := GetFromCAS(id)
	if err != nil {
		return bead, err
	}
	err = json.Unmarshal(data, &bead)
	if err == nil {
		bead.ID = id // Set the ID from the CAS hash
	}
	return bead, err
}

// ReindexStorage scans the CAS directory and ensures all files are indexed.
func ReindexStorage() error {
	fmt.Println("🔄 Starting CAS Re-indexing (including FTS)...")
	files, err := os.ReadDir(StorageDir)
	if err != nil {
		return fmt.Errorf("failed to read storage directory: %w", err)
	}

	count := 0
	for _, file := range files {
		if file.IsDir() {
			continue
		}

		id := file.Name()

		// Check if beads table has content_text AND if edges presumably exist
		// We use a heuristic: if bead exists and has content_text, we assume it was indexed.
		// However, for the new edge table, we might want to force check edges.
		// For simplicity/performance in this fix, we'll check if edges exist for this ID.
		// Check if edges exist for this bead to avoid re-processing.
		// NOTE: This causes re-indexing for beads with no parents (orphans) because they won't have edges.
		// This is acceptable overhead for ensuring correctness during this migration.
		var edgeExists int
		errEdge := DB.QueryRow("SELECT 1 FROM bead_edges WHERE child_id = ? LIMIT 1", id).Scan(&edgeExists)

		if errEdge == nil {
			continue // Edges exist, so we assume it's indexed
		}

		// Read file content and index
		data, err := GetFromCAS(id)
		if err != nil {
			continue
		}

		var bead types.Bead
		if err := json.Unmarshal(data, &bead); err != nil {
			continue
		}

		if err := indexMetadata(id, bead); err != nil {
			fmt.Printf("⚠️ Failed to index bead %s: %v\n", id, err)
		}
		count++
	}

	fmt.Printf("✅ Re-indexing complete. Processed %d entries.\n", count)
	return nil
}

// GetPatients retrieves all beads of type 'patient_registration'.
func GetPatients() ([]types.Bead, error) {
	query := `SELECT id FROM beads WHERE type = 'patient_registration' ORDER BY timestamp DESC`
	rows, err := DB.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var patients []types.Bead
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			continue
		}
		data, err := GetFromCAS(id)
		if err != nil {
			continue
		}
		var bead types.Bead
		if err := json.Unmarshal(data, &bead); err != nil {
			continue
		}
		bead.ID = id
		patients = append(patients, bead)
	}
	return patients, nil
}

// SearchPatientsByContentWithResourceTypes searches for patients with FHIR resource type filtering
func SearchPatientsByContentWithResourceTypes(queryText string, resourceTypes []string) ([]types.Bead, error) {
	// 1. Build resource type filter
	var typeFilter string
	if len(resourceTypes) > 0 {
		var typeClauses []string
		for _, rt := range resourceTypes {
			// Map UI resource type to actual bead types
			switch rt {
			case "encounter":
				typeClauses = append(typeClauses, "type = 'encounter' OR type = 'fhir_encounter'")
			case "medication":
				typeClauses = append(typeClauses, "type = 'medication' OR type = 'fhir_medicationrequest'")
			case "observation":
				typeClauses = append(typeClauses, "type = 'observation' OR type = 'fhir_observation'")
			case "condition":
				typeClauses = append(typeClauses, "type = 'condition' OR type = 'fhir_condition'")
			case "diagnostic_report":
				typeClauses = append(typeClauses, "type = 'diagnostic_report' OR type = 'fhir_diagnosticreport' OR type = 'fhir_documentreference'")
			case "procedure":
				typeClauses = append(typeClauses, "type = 'fhir_procedure'")
			case "immunization":
				typeClauses = append(typeClauses, "type = 'fhir_immunization'")
			case "imaging_study":
				typeClauses = append(typeClauses, "type = 'fhir_imagingstudy'")
			}
		}
		if len(typeClauses) > 0 {
			typeFilter = " AND (" + strings.Join(typeClauses, " OR ") + ")"
		}
	}

	// If no query text and only resource types, search by type only
	if queryText == "" && typeFilter != "" {
		return searchByResourceTypes(typeFilter)
	}

	// Otherwise use the existing search logic with type filter
	return searchWithTypeFilter(queryText, typeFilter)
}

// searchByResourceTypes searches for patients that have specific resource types
func searchByResourceTypes(typeFilter string) ([]types.Bead, error) {
	// Find all beads matching the type filter
	query := "SELECT DISTINCT id, type, parents FROM beads WHERE 1=1" + typeFilter
	rows, err := DB.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	patientSet := make(map[string]bool)
	for rows.Next() {
		var id, bType, parentsStr string
		if err := rows.Scan(&id, &bType, &parentsStr); err != nil {
			continue
		}

		var matchedPatientID string
		if bType == "patient_registration" {
			matchedPatientID = id
		} else {
			var parents []string
			_ = json.Unmarshal([]byte(parentsStr), &parents)
			matchedPatientID = findPatientRoot(append(parents, id))
		}

		if matchedPatientID != "" {
			patientSet[matchedPatientID] = true
		}
	}

	// Load patient beads
	var patients []types.Bead
	for patientID := range patientSet {
		bead, err := LoadFromCAS(patientID)
		if err == nil {
			patients = append(patients, bead)
		}
	}

	return patients, nil
}

// searchWithTypeFilter performs text search with optional type filtering
func searchWithTypeFilter(queryText string, typeFilter string) ([]types.Bead, error) {
	// Use the existing search logic but add type filter
	// This is essentially the old SearchPatientsByContent with type filtering
	// 1. Split query by comma for AND logic
	terms := strings.Split(queryText, ",")
	var patientSets []map[string]bool

	// Store snippets: map[PatientID] -> Snippet HTML
	snippetMap := make(map[string]string)

	for _, term := range terms {
		term = strings.TrimSpace(term)
		if term == "" {
			continue
		}

		// Search for this specific term
		safeTerm := strings.ReplaceAll(term, "\"", "\"\"")

		// Modified FTS query with type filter
		var ftsSQL string
		if typeFilter != "" {
			ftsSQL = fmt.Sprintf(`
				SELECT f.id, snippet(f, 0, '<b>', '</b>', '...', 10) as snip
				FROM beads_fts f
				JOIN beads b ON f.id = b.id
				WHERE f.content MATCH ? %s
				ORDER BY bm25(f)`, typeFilter)
		} else {
			ftsSQL = `SELECT id, snippet(beads_fts, 0, '<b>', '</b>', '...', 10) as snip FROM beads_fts WHERE content MATCH ? ORDER BY bm25(beads_fts)`
		}

		rows, err := DB.Query(ftsSQL, fmt.Sprintf("\"%s\"", safeTerm))
		if err != nil {
			// Fallback to LIKE with type filter
			fmt.Printf("⚠️ FTS failed for '%s', falling back to LIKE: %v\n", term, err)
			likeSQL := "SELECT id, content_text FROM beads WHERE content_text LIKE ?"
			if typeFilter != "" {
				likeSQL += typeFilter
			}
			rows, err = DB.Query(likeSQL, "%"+term+"%")
			if err != nil {
				return nil, err
			}
		}

		currentSet := make(map[string]bool)

		// Map matched beads to patients (rest of the logic remains the same)
		for rows.Next() {
			if len(currentSet) > 2000 {
				break
			}

			var id string
			var val string
			if err := rows.Scan(&id, &val); err != nil {
				continue
			}

			// Fallback snippet generation for LIKE query
			if !strings.Contains(val, "<b>") && len(val) > 0 {
				lowerVal := strings.ToLower(val)
				lowerTerm := strings.ToLower(term)
				idx := strings.Index(lowerVal, lowerTerm)
				if idx != -1 {
					start := idx - 20
					if start < 0 {
						start = 0
					}
					end := idx + len(term) + 50
					if end > len(val) {
						end = len(val)
					}
					val = "..." + val[start:end] + "..."
				} else if len(val) > 50 {
					val = val[:50] + "..."
				}
			}

			// Resolve to Patient ID
			var bType, parentsStr string
			err := DB.QueryRow("SELECT type, parents FROM beads WHERE id = ?", id).Scan(&bType, &parentsStr)
			if err != nil {
				continue
			}

			var matchedPatientID string
			if bType == "patient_registration" {
				matchedPatientID = id
			} else {
				var parents []string
				_ = json.Unmarshal([]byte(parentsStr), &parents)
				matchedPatientID = findPatientRoot(append(parents, id))
			}

			if matchedPatientID != "" {
				currentSet[matchedPatientID] = true

				// Accumulate Snippets
				if existing, ok := snippetMap[matchedPatientID]; ok {
					if !strings.Contains(existing, val) && len(existing) < 300 {
						snippetMap[matchedPatientID] = existing + " <br/> " + val
					}
				} else {
					snippetMap[matchedPatientID] = val
				}
			}
		}
		rows.Close()

		patientSets = append(patientSets, currentSet)
	}

	// 2. AND Logic: Find intersection of all term sets
	if len(patientSets) == 0 {
		return []types.Bead{}, nil
	}

	finalSet := patientSets[0]
	for i := 1; i < len(patientSets); i++ {
		nextSet := make(map[string]bool)
		for pid := range finalSet {
			if patientSets[i][pid] {
				nextSet[pid] = true
			}
		}
		finalSet = nextSet
	}

	// 3. Load Patient Beads
	var patients []types.Bead
	for patientID := range finalSet {
		bead, err := LoadFromCAS(patientID)
		if err == nil {
			// Inject Snippet into Content
			if snippet, ok := snippetMap[patientID]; ok {
				if bead.Content == nil {
					bead.Content = make(map[string]interface{})
				}
				bead.Content["_snippet"] = snippet
			}
			patients = append(patients, bead)
		}
	}

	return patients, nil
}

// SearchPatientsByContent searches for patients that match ALL query terms (comma separated).
func SearchPatientsByContent(queryText string) ([]types.Bead, error) {
	// 1. Split query by comma for AND logic
	terms := strings.Split(queryText, ",")
	var patientSets []map[string]bool

	// Store snippets: map[PatientID] -> Snippet HTML
	snippetMap := make(map[string]string)

	for _, term := range terms {
		term = strings.TrimSpace(term)
		if term == "" {
			continue
		}

		// Search for this specific term
		// Treat it as a phrase search by wrapping in quotes
		safeTerm := strings.ReplaceAll(term, "\"", "\"\"")

		// Use snippet() function from FTS5
		ftsSQL := `SELECT id, snippet(beads_fts, 0, '<b>', '</b>', '...', 10) as snip FROM beads_fts WHERE content MATCH ? ORDER BY bm25(beads_fts)`

		rows, err := DB.Query(ftsSQL, fmt.Sprintf("\"%s\"", safeTerm))
		if err != nil {
			// Fallback to LIKE
			fmt.Printf("⚠️ FTS failed for '%s', falling back to LIKE: %v\n", term, err)
			rows, err = DB.Query("SELECT id, content_text FROM beads WHERE content_text LIKE ?", "%"+term+"%")
			if err != nil {
				return nil, err
			}
		}

		currentSet := make(map[string]bool)

		// Map matched beads to patients
		for rows.Next() {
			// Max limit per term to avoid massive processing
			if len(currentSet) > 2000 {
				break
			}

			var id string
			var val string // This receives snippet or content_text
			if err := rows.Scan(&id, &val); err != nil {
				continue
			}

			// Fallback snippet generation for LIKE query
			if !strings.Contains(val, "<b>") && len(val) > 0 {
				lowerVal := strings.ToLower(val)
				lowerTerm := strings.ToLower(term)
				idx := strings.Index(lowerVal, lowerTerm)
				if idx != -1 {
					start := idx - 20
					if start < 0 {
						start = 0
					}
					end := idx + len(term) + 50
					if end > len(val) {
						end = len(val)
					}
					val = "..." + val[start:end] + "..."
				} else if len(val) > 50 {
					val = val[:50] + "..."
				}
			}

			// Resolve to Patient ID
			var bType, parentsStr string
			err := DB.QueryRow("SELECT type, parents FROM beads WHERE id = ?", id).Scan(&bType, &parentsStr)
			if err != nil {
				continue
			}

			var matchedPatientID string
			if bType == "patient_registration" {
				matchedPatientID = id
			} else {
				var parents []string
				_ = json.Unmarshal([]byte(parentsStr), &parents)
				matchedPatientID = findPatientRoot(append(parents, id))
			}

			if matchedPatientID != "" {
				currentSet[matchedPatientID] = true

				// Accumulate Snippets
				if existing, ok := snippetMap[matchedPatientID]; ok {
					if !strings.Contains(existing, val) && len(existing) < 300 {
						snippetMap[matchedPatientID] = existing + " <br/> " + val
					}
				} else {
					snippetMap[matchedPatientID] = val
				}
			}
		}
		rows.Close()

		patientSets = append(patientSets, currentSet)
	}

	if len(patientSets) == 0 {
		return []types.Bead{}, nil
	}

	// 2. Compute Intersection
	finalSet := patientSets[0]

	for i := 1; i < len(patientSets); i++ {
		intersection := make(map[string]bool)
		for pid := range finalSet {
			if patientSets[i][pid] {
				intersection[pid] = true
			}
		}
		finalSet = intersection
	}

	// 3. Fetch Patient Objects
	var patients []types.Bead
	for pid := range finalSet {
		data, err := GetFromCAS(pid)
		if err != nil {
			continue
		}
		var bead types.Bead
		if err := json.Unmarshal(data, &bead); err != nil {
			continue
		}
		bead.ID = pid

		// Inject Snippet
		if snip, ok := snippetMap[pid]; ok {
			if bead.Content == nil {
				bead.Content = make(map[string]interface{})
			}
			bead.Content["_snippet"] = snip
		}

		patients = append(patients, bead)
	}

	return patients, nil
}

// findPatientRoot does a BFS to find the ancestor with type 'patient_registration'
func findPatientRoot(initialParents []string) string {
	queue := make([]string, len(initialParents))
	copy(queue, initialParents)
	visited := make(map[string]bool)
	for _, p := range initialParents {
		visited[p] = true
	}

	depth := 0
	maxDepth := 15

	for len(queue) > 0 && depth < maxDepth {
		levelSize := len(queue)
		for i := 0; i < levelSize; i++ {
			curr := queue[0]
			queue = queue[1:]

			var bType, parentsStr string
			// Optimize: cache this?
			err := DB.QueryRow("SELECT type, parents FROM beads WHERE id = ?", curr).Scan(&bType, &parentsStr)
			if err != nil {
				continue
			}

			if bType == "patient_registration" {
				return curr
			}

			var parents []string
			_ = json.Unmarshal([]byte(parentsStr), &parents)
			for _, p := range parents {
				if !visited[p] {
					visited[p] = true
					queue = append(queue, p)
				}
			}
		}
		depth++
	}
	return ""
}

func GetBeadsByParent(rootID string, depth int) ([]types.Bead, error) {
	var results []types.Bead

	// Include the root bead itself so the graph is connected
	rootData, err := GetFromCAS(rootID)
	if err == nil {
		var rootBead types.Bead
		if err := json.Unmarshal(rootData, &rootBead); err == nil {
			rootBead.ID = rootID
			results = append(results, rootBead)
		}
	}

	visited := make(map[string]bool)
	queue := []string{rootID}
	visited[rootID] = true

	currentDepth := 0
	for len(queue) > 0 && currentDepth <= depth {
		levelSize := len(queue)
		for i := 0; i < levelSize; i++ {
			parentID := queue[0]
			queue = queue[1:]

			children, err := getChildrenSimple(parentID)
			if err != nil {
				continue
			}

			for _, child := range children {
				if !visited[child.ID] {
					visited[child.ID] = true
					queue = append(queue, child.ID)
					results = append(results, child)
				}
			}
		}
		currentDepth++
	}
	return results, nil
}

func getChildrenSimple(parentID string) ([]types.Bead, error) {
	// Optimization: Use bead_edges table instead of LIKE query
	// This changes complexity from O(N) scan to O(1) index lookup
	query := `SELECT child_id FROM bead_edges WHERE parent_id = ?`
	rows, err := DB.Query(query, parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var beads []types.Bead
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			continue
		}
		// Load from CAS
		data, err := GetFromCAS(id)
		if err != nil {
			continue
		}
		var bead types.Bead
		if err := json.Unmarshal(data, &bead); err != nil {
			continue
		}
		bead.ID = id
		beads = append(beads, bead)
	}

	// Sort by timestamp if possible (requires loading all, which we do)
	// For strictly better performance, should sort in SQL or memory.
	// Since we load all, memory sort is fine.
	// (Sorting logic omitted for brevity as it was implicit in SQL 'ORDER BY' before,
	// but strictly speaking map iteration or unordered fetch changes order.
	// The caller `GetBeadsByParent` usually doesn't strictly depend on order for BFS
	// but UI might. Let's keep it simple.)

	return beads, nil
}

func GetContext(startID string, depth int) ([]types.Bead, error) {
	visited := make(map[string]bool)
	var queue []string
	var results []types.Bead

	queue = append(queue, startID)
	visited[startID] = true

	currentDepth := 0
	for len(queue) > 0 && currentDepth <= depth {
		levelSize := len(queue)
		for i := 0; i < levelSize; i++ {
			id := queue[0]
			queue = queue[1:]
			data, err := GetFromCAS(id)
			if err != nil {
				continue
			}
			var bead types.Bead
			if err := json.Unmarshal(data, &bead); err != nil {
				continue
			}
			bead.ID = id
			results = append(results, bead)
			for _, parentID := range bead.Parents {
				if !visited[parentID] {
					visited[parentID] = true
					queue = append(queue, parentID)
				}
			}
		}
		currentDepth++
	}
	return results, nil
}

// ResourceTypeCount represents the count of patients for a resource type
type ResourceTypeCount struct {
	ResourceType string `json:"resourceType"`
	PatientCount int    `json:"patientCount"`
}

// ============================================================
// Security Clearance Functions
// ============================================================

// SaveClearanceRule saves a clearance rule to the database
func SaveClearanceRule(rule types.ClearanceRule) error {
	deniedRolesJSON, err := json.Marshal(rule.DeniedRoles)
	if err != nil {
		return fmt.Errorf("failed to marshal denied_roles: %w", err)
	}

	query := `INSERT OR REPLACE INTO clearance_rules (id, bead_id, denied_roles, created_by, created_at, reason, expires_at)
	          VALUES (?, ?, ?, ?, ?, ?, ?)`
	_, err = DB.Exec(query, rule.ID, rule.BeadID, string(deniedRolesJSON), rule.CreatedBy, rule.CreatedAt, rule.Reason, rule.ExpiresAt)
	if err != nil {
		return fmt.Errorf("failed to save clearance rule: %w", err)
	}
	return nil
}

// GetClearanceRules retrieves all clearance rules for a bead
func GetClearanceRules(beadID string) ([]types.ClearanceRule, error) {
	query := `SELECT id, bead_id, denied_roles, created_by, created_at, reason, expires_at
	          FROM clearance_rules WHERE bead_id = ?`
	rows, err := DB.Query(query, beadID)
	if err != nil {
		return nil, fmt.Errorf("failed to query clearance rules: %w", err)
	}
	defer rows.Close()

	var rules []types.ClearanceRule
	for rows.Next() {
		var rule types.ClearanceRule
		var deniedRolesStr string
		var expiresAt sql.NullString
		var reason sql.NullString

		if err := rows.Scan(&rule.ID, &rule.BeadID, &deniedRolesStr, &rule.CreatedBy, &rule.CreatedAt, &reason, &expiresAt); err != nil {
			continue
		}

		if err := json.Unmarshal([]byte(deniedRolesStr), &rule.DeniedRoles); err != nil {
			continue
		}

		if reason.Valid {
			rule.Reason = reason.String
		}
		if expiresAt.Valid {
			rule.ExpiresAt = &expiresAt.String
		}

		rules = append(rules, rule)
	}

	return rules, nil
}

// GetAllClearanceRulesForBeads retrieves clearance rules for multiple beads efficiently
func GetAllClearanceRulesForBeads(beadIDs []string) (map[string][]types.ClearanceRule, error) {
	if len(beadIDs) == 0 {
		return make(map[string][]types.ClearanceRule), nil
	}

	// Build placeholders for IN clause
	placeholders := make([]string, len(beadIDs))
	args := make([]interface{}, len(beadIDs))
	for i, id := range beadIDs {
		placeholders[i] = "?"
		args[i] = id
	}

	query := fmt.Sprintf(`SELECT id, bead_id, denied_roles, created_by, created_at, reason, expires_at
	          FROM clearance_rules WHERE bead_id IN (%s)`, strings.Join(placeholders, ","))
	rows, err := DB.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query clearance rules: %w", err)
	}
	defer rows.Close()

	result := make(map[string][]types.ClearanceRule)
	for rows.Next() {
		var rule types.ClearanceRule
		var deniedRolesStr string
		var expiresAt sql.NullString
		var reason sql.NullString

		if err := rows.Scan(&rule.ID, &rule.BeadID, &deniedRolesStr, &rule.CreatedBy, &rule.CreatedAt, &reason, &expiresAt); err != nil {
			continue
		}

		if err := json.Unmarshal([]byte(deniedRolesStr), &rule.DeniedRoles); err != nil {
			continue
		}

		if reason.Valid {
			rule.Reason = reason.String
		}
		if expiresAt.Valid {
			rule.ExpiresAt = &expiresAt.String
		}

		result[rule.BeadID] = append(result[rule.BeadID], rule)
	}

	return result, nil
}

// DeleteClearanceRule deletes a clearance rule by ID
func DeleteClearanceRule(ruleID string) error {
	query := `DELETE FROM clearance_rules WHERE id = ?`
	_, err := DB.Exec(query, ruleID)
	if err != nil {
		return fmt.Errorf("failed to delete clearance rule: %w", err)
	}
	return nil
}

// HasAccess checks if a viewer has access to a bead based on clearance rules
// Returns true if access is allowed, false otherwise
func HasAccess(beadID string, viewerRoles []string) (bool, error) {
	// Emergency role always has access
	for _, role := range viewerRoles {
		if role == types.RoleEmergency || role == types.RoleSystem {
			return true, nil
		}
	}

	rules, err := GetClearanceRules(beadID)
	if err != nil {
		return false, err
	}

	// No rules = no restrictions (Blacklist model)
	if len(rules) == 0 {
		return true, nil
	}

	// Check each rule
	now := currentTime()
	for _, rule := range rules {
		// Skip expired rules
		if rule.ExpiresAt != nil && *rule.ExpiresAt != "" {
			expiresAt, err := parseTime(*rule.ExpiresAt)
			if err == nil && now.After(expiresAt) {
				continue
			}
		}

		// Check if any of viewer's roles are denied
		for _, viewerRole := range viewerRoles {
			for _, deniedRole := range rule.DeniedRoles {
				if viewerRole == deniedRole {
					return false, nil
				}
			}
		}
	}

	return true, nil
}

// HasAccessWithRules checks access using pre-fetched rules (for efficiency with bulk operations)
func HasAccessWithRules(rules []types.ClearanceRule, viewerRoles []string) bool {
	// Emergency role always has access
	for _, role := range viewerRoles {
		if role == types.RoleEmergency || role == types.RoleSystem {
			return true
		}
	}

	// No rules = no restrictions (Blacklist model)
	if len(rules) == 0 {
		return true
	}

	// Check each rule
	now := currentTime()
	for _, rule := range rules {
		// Skip expired rules
		if rule.ExpiresAt != nil && *rule.ExpiresAt != "" {
			expiresAt, err := parseTime(*rule.ExpiresAt)
			if err == nil && now.After(expiresAt) {
				continue
			}
		}

		// Check if any of viewer's roles are denied
		for _, viewerRole := range viewerRoles {
			for _, deniedRole := range rule.DeniedRoles {
				if viewerRole == deniedRole {
					return false
				}
			}
		}
	}

	return true
}

// FilterByAccess filters a list of beads based on viewer's access permissions
func FilterByAccess(beads []types.Bead, viewerRoles []string) ([]types.Bead, error) {
	if len(beads) == 0 {
		return beads, nil
	}

	// Emergency/System role sees everything
	for _, role := range viewerRoles {
		if role == types.RoleEmergency || role == types.RoleSystem {
			return beads, nil
		}
	}

	// Collect all bead IDs
	beadIDs := make([]string, len(beads))
	for i, bead := range beads {
		beadIDs[i] = bead.ID
	}

	// Fetch all rules at once
	rulesMap, err := GetAllClearanceRulesForBeads(beadIDs)
	if err != nil {
		return nil, err
	}

	// Filter beads
	var filtered []types.Bead
	for _, bead := range beads {
		rules := rulesMap[bead.ID]
		if HasAccessWithRules(rules, viewerRoles) {
			filtered = append(filtered, bead)
		}
	}

	return filtered, nil
}

// LogClearanceAction logs a clearance-related action for audit purposes
func LogClearanceAction(beadID, action, userID string, userRoles []string, details string) error {
	rolesJSON, err := json.Marshal(userRoles)
	if err != nil {
		return fmt.Errorf("failed to marshal user_roles: %w", err)
	}

	query := `INSERT INTO clearance_audit (bead_id, action, user_id, user_roles, details) VALUES (?, ?, ?, ?, ?)`
	_, err = DB.Exec(query, beadID, action, userID, string(rolesJSON), details)
	if err != nil {
		return fmt.Errorf("failed to log clearance action: %w", err)
	}
	return nil
}

// Helper function to get current time (can be mocked for testing)
func currentTime() time.Time {
	return time.Now()
}

// Helper function to parse time strings
func parseTime(s string) (time.Time, error) {
	formats := []string{
		time.RFC3339,
		"2006-01-02T15:04:05Z",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}
	for _, format := range formats {
		if t, err := time.Parse(format, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unable to parse time: %s", s)
}

// GetResourceTypeCounts returns the number of patients that have each resource type
func GetResourceTypeCounts() ([]ResourceTypeCount, error) {
	resourceTypes := []struct {
		name       string
		typeClause string
	}{
		{"encounter", "type = 'encounter' OR type = 'fhir_encounter'"},
		{"medication", "type = 'medication' OR type = 'fhir_medicationrequest'"},
		{"observation", "type = 'observation' OR type = 'fhir_observation'"},
		{"condition", "type = 'condition' OR type = 'fhir_condition'"},
		{"diagnostic_report", "type = 'diagnostic_report' OR type = 'fhir_diagnosticreport' OR type = 'fhir_documentreference'"},
		{"procedure", "type = 'fhir_procedure'"},
		{"immunization", "type = 'fhir_immunization'"},
		{"imaging_study", "type = 'fhir_imagingstudy'"},
	}

	var results []ResourceTypeCount

	for _, rt := range resourceTypes {
		// Find all beads of this type and count unique patients
		query := fmt.Sprintf("SELECT DISTINCT id, type, parents FROM beads WHERE %s", rt.typeClause)
		rows, err := DB.Query(query)
		if err != nil {
			results = append(results, ResourceTypeCount{ResourceType: rt.name, PatientCount: 0})
			continue
		}

		patientSet := make(map[string]bool)
		for rows.Next() {
			var id, bType, parentsStr string
			if err := rows.Scan(&id, &bType, &parentsStr); err != nil {
				continue
			}

			var matchedPatientID string
			if bType == "patient_registration" {
				matchedPatientID = id
			} else {
				var parents []string
				_ = json.Unmarshal([]byte(parentsStr), &parents)
				matchedPatientID = findPatientRoot(append(parents, id))
			}

			if matchedPatientID != "" {
				patientSet[matchedPatientID] = true
			}
		}
		rows.Close()

		results = append(results, ResourceTypeCount{
			ResourceType: rt.name,
			PatientCount: len(patientSet),
		})
	}

	return results, nil
}
