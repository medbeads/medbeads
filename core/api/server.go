package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sojin25/medbeads/core/store"
	"github.com/sojin25/medbeads/core/types"
)

// StartServer starts the MedBeads Core HTTP server.
func StartServer(port string) error {
	http.HandleFunc("/beads", handleBeads)
	http.HandleFunc("/beads/context", handleContext)
	http.HandleFunc("/patients", handlePatients)
	http.HandleFunc("/search", handleSearch)
	http.HandleFunc("/resource-counts", handleResourceCounts)
	fmt.Printf("🚀 MedBeads Core Server running on port %s\n", port)
	return http.ListenAndServe(port, nil)
}

func handleSearch(w http.ResponseWriter, r *http.Request) {
	// CORS Headers
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	query := r.URL.Query().Get("q")
	// Allow empty query if resource types are specified
	// if query == "" {
	// 	http.Error(w, "Missing 'q' parameter", http.StatusBadRequest)
	// 	return
	// }

	// Get resource types filter
	resourceTypesStr := r.URL.Query().Get("resourceTypes")
	var resourceTypes []string
	if resourceTypesStr != "" {
		// Parse comma-separated resource types
		for _, rt := range strings.Split(resourceTypesStr, ",") {
			rt = strings.TrimSpace(rt)
			if rt != "" {
				resourceTypes = append(resourceTypes, rt)
			}
		}
	}

	var patients []types.Bead
	var err error

	// Use new function with resource type filtering
	if len(resourceTypes) > 0 || query == "" {
		patients, err = store.SearchPatientsByContentWithResourceTypes(query, resourceTypes)
	} else {
		// Fallback to old function for backward compatibility
		patients, err = store.SearchPatientsByContent(query)
	}

	if err != nil {
		http.Error(w, fmt.Sprintf("Search failed: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(patients)
}

func handleContext(w http.ResponseWriter, r *http.Request) {
	// CORS Headers
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	id := r.URL.Query().Get("id")
	depthStr := r.URL.Query().Get("depth")

	if id == "" {
		http.Error(w, "Missing 'id' parameter", http.StatusBadRequest)
		return
	}

	depth := 5 // Default depth
	if depthStr != "" {
		fmt.Sscanf(depthStr, "%d", &depth)
	}

	var beads []types.Bead
	var err error

	lookupType := r.URL.Query().Get("lookup")
	if lookupType == "reverse" {
		beads, err = store.GetBeadsByParent(id, depth)
	} else {
		beads, err = store.GetContext(id, depth)
	}

	if err != nil {
		http.Error(w, "Failed to retrieve context", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(beads)
}

func handleBeads(w http.ResponseWriter, r *http.Request) {
	// CORS Middleware-like headers
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	if r.Method == "POST" {
		saveHandler(w, r)
		return
	}

	if r.Method == "GET" {
		getHandler(w, r)
		return
	}

	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
}

func saveHandler(w http.ResponseWriter, r *http.Request) {
	var bead types.Bead

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read body", http.StatusBadRequest)
		return
	}

	if err := json.Unmarshal(body, &bead); err != nil {
		http.Error(w, "Invalid JSON format", http.StatusBadRequest)
		return
	}

	if bead.Timestamp == "" {
		bead.Timestamp = time.Now().Format(time.RFC3339)
	}

	hashID, err := store.SaveToCAS(bead)
	if err != nil {
		http.Error(w, "Failed to save data", http.StatusInternalServerError)
		return
	}

	response := map[string]string{
		"status": "success",
		"id":     hashID,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)

	fmt.Printf("📥 Received & Saved: %s (Type: %s)\n", hashID, bead.Type)
}

func getHandler(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "Missing 'id' parameter", http.StatusBadRequest)
		return
	}

	data, err := store.GetFromCAS(id)
	if err != nil {
		http.Error(w, "Bead not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

func handlePatients(w http.ResponseWriter, r *http.Request) {
	// CORS Headers
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	patients, err := store.GetPatients()
	if err != nil {
		http.Error(w, "Failed to retrieve patients", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(patients)
}

func handleResourceCounts(w http.ResponseWriter, r *http.Request) {
	// CORS Headers
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	counts, err := store.GetResourceTypeCounts()
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to get counts: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(counts)
}
