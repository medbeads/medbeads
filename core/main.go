package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

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

	// Graceful shutdown: on SIGINT/SIGTERM, compact the metadata DB (FTS
	// optimize + WAL checkpoint + VACUUM) and close it cleanly before exit.
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		fmt.Println("\n🛑 Shutting down: compacting storage...")
		if err := store.Compact(); err != nil {
			fmt.Printf("⚠️ Compact failed: %v\n", err)
		}
		if store.DB != nil {
			store.DB.Close()
		}
		os.Exit(0)
	}()

	// Start API Server
	if err := api.StartServer(Port); err != nil {
		panic(err)
	}
}
