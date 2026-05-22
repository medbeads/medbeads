package store

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sojin25/medbeads/core/types"

	_ "github.com/mattn/go-sqlite3" // SQLite Driver
)

// ErrCycleDetected is returned by SaveToCAS when storing a bead would make it
// its own ancestor (a cycle in the DAG).
var ErrCycleDetected = errors.New("cycle detected: bead would be its own ancestor")

// StorageDir and DBSource are vars (not consts) so that tests can point them
// at an isolated temp location. They default to the production paths and can
// also be overridden via the MEDBEADS_STORAGE_DIR / MEDBEADS_DB_SOURCE env
// vars for non-default deployments.
var (
	StorageDir = envOr("MEDBEADS_STORAGE_DIR", "./medbeads_data/objects")
	DBSource   = envOr("MEDBEADS_DB_SOURCE", "./medbeads_data/metadata.db")
)

var DB *sql.DB

// envOr returns the value of the environment variable key, or fallback if it
// is unset or empty.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

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
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
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

	// Migration: drop the legacy content_text column. The bead content is
	// already stored in the CAS object and the beads_fts index, so this column
	// was a third redundant copy. Dropping it reclaims space on existing DBs.
	_, err = DB.Exec("ALTER TABLE beads DROP COLUMN content_text")
	if err != nil && !strings.Contains(err.Error(), "no such column") {
		// Ignore if the column was already dropped / never existed.
		fmt.Printf("ℹ️ content_text drop: %v\n", err)
	}

	// 4. Clearance Rules Table (Security Clearance feature)
	clearanceQuery := `
	CREATE TABLE IF NOT EXISTS clearance_rules (
		id TEXT PRIMARY KEY,
		bead_id TEXT NOT NULL,
		denied_roles TEXT NOT NULL,
		allowed_roles TEXT,
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

	// Migration: add allowed_roles (whitelist) to clearance_rules for existing DBs.
	_, err = DB.Exec("ALTER TABLE clearance_rules ADD COLUMN allowed_roles TEXT")
	if err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		fmt.Printf("ℹ️ allowed_roles column check: %v\n", err)
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

	// Cycle detection: a bead must never be reachable from its own parents.
	// (Content-addressing makes this practically impossible, but a malformed
	// or hand-crafted parents list is rejected here as a safety net.)
	if hasAncestor(hashString, b.Parents) {
		return "", ErrCycleDetected
	}

	filePath := filepath.Join(StorageDir, hashString)

	// Store the object gzip-compressed. The hash above is computed over the
	// uncompressed JSON, so the file name (= Bead ID) is unchanged; only the
	// on-disk bytes shrink. GetFromCAS transparently decompresses on read.
	compressed, err := gzipBytes(data)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filePath, compressed, 0644); err != nil {
		return "", err
	}

	// 2. Index Metadata in SQLite
	err = indexMetadata(hashString, b)
	if err != nil {
		fmt.Printf("⚠️ Warning: Failed to index metadata for %s: %v\n", hashString, err)
	}

	return hashString, nil
}

// hasAncestor reports whether targetID is reachable by walking up the parent
// edges starting from startParents. Used for cycle detection on save.
func hasAncestor(targetID string, startParents []string) bool {
	if targetID == "" {
		return false
	}
	visited := make(map[string]bool)
	queue := append([]string{}, startParents...)
	for _, p := range startParents {
		visited[p] = true
	}
	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]
		if curr == targetID {
			return true
		}
		var parentsStr string
		if err := DB.QueryRow("SELECT parents FROM beads WHERE id = ?", curr).Scan(&parentsStr); err != nil {
			continue
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
	return false
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

	// 1. Main Table (metadata only; content lives in CAS + beads_fts)
	query := `INSERT OR REPLACE INTO beads (id, type, timestamp, parents) VALUES (?, ?, ?, ?)`
	if _, err := tx.Exec(query, id, b.Type, b.Timestamp, string(parentsJSON)); err != nil {
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
// Objects are stored gzip-compressed; legacy uncompressed objects are still
// readable because the gzip magic bytes are checked before decompressing.
func GetFromCAS(id string) ([]byte, error) {
	filePath := filepath.Join(StorageDir, id)
	raw, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	// gzip magic number: 0x1f 0x8b. Legacy objects are plain JSON ('{').
	if len(raw) >= 2 && raw[0] == 0x1f && raw[1] == 0x8b {
		return gunzipBytes(raw)
	}
	return raw, nil
}

// gzipBytes returns data compressed with gzip.
func gzipBytes(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write(data); err != nil {
		gw.Close()
		return nil, err
	}
	if err := gw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// gunzipBytes returns data decompressed from gzip.
func gunzipBytes(data []byte) ([]byte, error) {
	gr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer gr.Close()
	return io.ReadAll(gr)
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

		// Heuristic: if edges exist for this ID, assume it is already indexed.
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
	if err := Compact(); err != nil {
		fmt.Printf("⚠️ Compact after reindex failed: %v\n", err)
	}
	return nil
}

// Compact reclaims storage and defragments the metadata DB. It optimizes the
// FTS5 index, folds the write-ahead log back into the main database file, and
// runs VACUUM to release space freed by deletes (e.g. the dropped content_text
// column). Safe to call while the DB is idle; intended for shutdown / post-bulk.
func Compact() error {
	if DB == nil {
		return errors.New("Compact: DB is not initialized")
	}
	if _, err := DB.Exec("INSERT INTO beads_fts(beads_fts) VALUES('optimize')"); err != nil {
		return fmt.Errorf("FTS optimize failed: %w", err)
	}
	if _, err := DB.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return fmt.Errorf("wal_checkpoint failed: %w", err)
	}
	if _, err := DB.Exec("VACUUM"); err != nil {
		return fmt.Errorf("VACUUM failed: %w", err)
	}
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
			likeSQL := "SELECT f.id, f.content FROM beads_fts f"
			if typeFilter != "" {
				likeSQL += " JOIN beads b ON f.id = b.id WHERE f.content LIKE ?" + typeFilter
			} else {
				likeSQL += " WHERE f.content LIKE ?"
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
			rows, err = DB.Query("SELECT id, content FROM beads_fts WHERE content LIKE ?", "%"+term+"%")
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

	// allowed_roles is nullable: an unset whitelist is stored as SQL NULL.
	var allowedRolesArg interface{}
	if len(rule.AllowedRoles) > 0 {
		allowedRolesJSON, err := json.Marshal(rule.AllowedRoles)
		if err != nil {
			return fmt.Errorf("failed to marshal allowed_roles: %w", err)
		}
		allowedRolesArg = string(allowedRolesJSON)
	}

	query := `INSERT OR REPLACE INTO clearance_rules (id, bead_id, denied_roles, allowed_roles, created_by, created_at, reason, expires_at)
	          VALUES (?, ?, ?, ?, ?, ?, ?, ?)`
	_, err = DB.Exec(query, rule.ID, rule.BeadID, string(deniedRolesJSON), allowedRolesArg, rule.CreatedBy, rule.CreatedAt, rule.Reason, rule.ExpiresAt)
	if err != nil {
		return fmt.Errorf("failed to save clearance rule: %w", err)
	}
	return nil
}

// GetClearanceRules retrieves all clearance rules for a bead
func GetClearanceRules(beadID string) ([]types.ClearanceRule, error) {
	query := `SELECT id, bead_id, denied_roles, allowed_roles, created_by, created_at, reason, expires_at
	          FROM clearance_rules WHERE bead_id = ?`
	rows, err := DB.Query(query, beadID)
	if err != nil {
		return nil, fmt.Errorf("failed to query clearance rules: %w", err)
	}
	defer rows.Close()

	var rules []types.ClearanceRule
	for rows.Next() {
		rule, err := scanClearanceRule(rows)
		if err != nil {
			continue
		}
		rules = append(rules, rule)
	}

	return rules, nil
}

// scanClearanceRule scans one clearance_rules row (column order: id, bead_id,
// denied_roles, allowed_roles, created_by, created_at, reason, expires_at).
func scanClearanceRule(rows *sql.Rows) (types.ClearanceRule, error) {
	var rule types.ClearanceRule
	var deniedRolesStr string
	var allowedRolesStr sql.NullString
	var expiresAt sql.NullString
	var reason sql.NullString

	if err := rows.Scan(&rule.ID, &rule.BeadID, &deniedRolesStr, &allowedRolesStr, &rule.CreatedBy, &rule.CreatedAt, &reason, &expiresAt); err != nil {
		return rule, err
	}
	if err := json.Unmarshal([]byte(deniedRolesStr), &rule.DeniedRoles); err != nil {
		return rule, err
	}
	if allowedRolesStr.Valid && allowedRolesStr.String != "" {
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

	query := fmt.Sprintf(`SELECT id, bead_id, denied_roles, allowed_roles, created_by, created_at, reason, expires_at
	          FROM clearance_rules WHERE bead_id IN (%s)`, strings.Join(placeholders, ","))
	rows, err := DB.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query clearance rules: %w", err)
	}
	defer rows.Close()

	result := make(map[string][]types.ClearanceRule)
	for rows.Next() {
		rule, err := scanClearanceRule(rows)
		if err != nil {
			continue
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

// IsRuleActive reports whether a clearance rule is currently in effect, i.e.
// it has no expiry or its expiry is in the future. An unparseable expiry is
// treated as active (fail-closed for security).
func IsRuleActive(rule types.ClearanceRule, now time.Time) bool {
	if rule.ExpiresAt == nil || *rule.ExpiresAt == "" {
		return true
	}
	expiresAt, err := parseTime(*rule.ExpiresAt)
	if err != nil {
		return true
	}
	return !now.After(expiresAt)
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

	return HasAccessWithRules(rules, viewerRoles), nil
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

	now := currentTime()

	// A viewer with no identified role may not view any bead that has an
	// active clearance rule. This closes the bypass where omitting the
	// X-Viewer-Roles header would otherwise expose restricted data.
	if len(viewerRoles) == 0 {
		for _, rule := range rules {
			if IsRuleActive(rule, now) {
				return false
			}
		}
		return true
	}

	// Check each active rule. Each rule is an independent constraint; the
	// viewer must satisfy all of them.
	for _, rule := range rules {
		if !IsRuleActive(rule, now) {
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

		// Whitelist: when allowed_roles is set, the viewer must hold at least
		// one of those roles, otherwise this rule blocks access.
		if len(rule.AllowedRoles) > 0 && !rolesIntersect(viewerRoles, rule.AllowedRoles) {
			return false
		}
	}

	return true
}

// rolesIntersect reports whether the two role slices share at least one role.
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

// FilterByAccess masks the content of beads the viewer is not allowed to see.
// A restricted bead keeps its id/type/timestamp/parents (so the DAG structure
// and the UI's "locked node" rendering stay intact) but its Content is replaced
// with {"_restricted": true}. Accessible beads are returned unchanged.
func FilterByAccess(beads []types.Bead, viewerRoles []string) ([]types.Bead, error) {
	if len(beads) == 0 {
		return beads, nil
	}

	beadIDs := make([]string, len(beads))
	for i, b := range beads {
		beadIDs[i] = b.ID
	}

	rulesMap, err := GetAllClearanceRulesForBeads(beadIDs)
	if err != nil {
		return nil, err
	}

	result := make([]types.Bead, len(beads))
	for i, bead := range beads {
		if HasAccessWithRules(rulesMap[bead.ID], viewerRoles) {
			result[i] = bead
			continue
		}
		// Mask the content but preserve graph metadata.
		result[i] = types.Bead{
			ID:        bead.ID,
			Type:      bead.Type,
			Timestamp: bead.Timestamp,
			Parents:   bead.Parents,
			Content:   map[string]interface{}{"_restricted": true},
		}
	}
	return result, nil
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
