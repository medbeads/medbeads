package main

import (
	"fmt"

	"github.com/sojin25/medbeads/core/api"
	"github.com/sojin25/medbeads/core/store"
)

const (
	Port = ":8080"
)

func main() {
	// Initialize Storage
	if err := store.EnsureStorageDir(); err != nil {
		panic(err)
	}
	// Initialize Metadata DB
	if err := store.InitDB(); err != nil {
		panic(err)
	}
	// Re-index Storage to match FS (Async)
	// Disabled auto-reindexing to prevent startup delays
	// Uncomment below if you need to reindex the storage
	// go func() {
	// 	if err := store.ReindexStorage(); err != nil {
	// 		fmt.Printf("⚠️ Warning: Re-indexing failed: %v\n", err)
	// 	}
	// }()
	fmt.Printf("📂 Storage initialized at: %s\n", store.StorageDir)

	// Start API Server
	if err := api.StartServer(Port); err != nil {
		panic(err)
	}
}
