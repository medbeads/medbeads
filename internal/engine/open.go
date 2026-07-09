package engine

import (
	"fmt"
	"path/filepath"

	"github.com/medbeads/medbeads/internal/engine/index"
	"github.com/medbeads/medbeads/internal/engine/pod"
)

// Engine is the single entry point onto one MedBeads data directory: it
// bundles the Pod store (pod.Store, plus a per-path Writer registry) and the
// SQLite index (index.DB) behind the write protocol (Ingest) and thin read
// APIs. See the package doc comment (engine.go) for the process-model and
// crash-recovery guarantees Engine provides.
type Engine struct {
	dataDir   string
	lock      *dataDirLock
	podStore  *pod.Store
	writers   *writerRegistry
	idx       *index.DB
	flattener index.Flattener
}

// Open acquires the data directory's advisory lock, opens (creating if
// necessary) its Pod store and index.db, and runs crash recovery
// (index.CatchUp for every Pod file found) before returning. The returned
// Engine must be Closed to release the lock and close the index database.
//
// A second Open against the same dataDir — from this process or another —
// fails fast (see lock.go): only one Engine may be open per data directory
// at a time.
func Open(dataDir string) (*Engine, error) {
	lock, err := acquireDataDirLock(dataDir)
	if err != nil {
		return nil, err
	}

	podStore := pod.NewStore(dataDir)
	if err := podStore.EnsurePodsDir(); err != nil {
		lock.release() //nolint:errcheck // best-effort unwind; Open is already failing
		return nil, fmt.Errorf("engine: open %s: %w", dataDir, err)
	}

	dbPath := filepath.Join(dataDir, "index.db")
	idx, err := index.Open(dbPath)
	if err != nil {
		lock.release() //nolint:errcheck
		return nil, fmt.Errorf("engine: open %s: %w", dataDir, err)
	}

	e := &Engine{
		dataDir:   dataDir,
		lock:      lock,
		podStore:  podStore,
		writers:   newWriterRegistry(),
		idx:       idx,
		flattener: index.DefaultFlattener{},
	}

	if err := e.catchUpAll(); err != nil {
		idx.Close()          //nolint:errcheck
		lock.release()       //nolint:errcheck
		return nil, fmt.Errorf("engine: open %s: crash recovery: %w", dataDir, err)
	}

	return e, nil
}

// catchUpAll runs index.CatchUp for every Pod file under the data
// directory's pods/ (specs/DESIGN_v3.md §3, R1.3): a prior process that
// crashed after a Pod append+fsync but before the matching IndexBead
// transaction committed leaves that Pod ahead of its indexed_upto
// watermark. CatchUp resumes indexing each Pod exactly from its watermark,
// which is a no-op for any Pod that was already fully indexed at shutdown.
func (e *Engine) catchUpAll() error {
	paths, err := e.podStore.ListPodFiles()
	if err != nil {
		return fmt.Errorf("list pod files: %w", err)
	}
	for _, path := range paths {
		if err := index.CatchUp(e.idx, path); err != nil {
			return fmt.Errorf("catch up %s: %w", path, err)
		}
	}
	return nil
}

// SetFlattener replaces the index.Flattener Ingest uses to derive
// search_text/summary for newly-indexed Beads. The default is
// index.DefaultFlattener{} (see Open); a later unit (antigen/FHIR-aware
// flattening) supplies its own Flattener here without any change to Ingest
// itself.
func (e *Engine) SetFlattener(f index.Flattener) {
	e.flattener = f
}

// DataDir returns the data directory this Engine was opened against.
func (e *Engine) DataDir() string {
	return e.dataDir
}

// Close releases the data directory lock, closes every per-Pod Writer this
// Engine opened, and closes the index database. It collects (rather than
// stopping at) the first error so every resource gets a chance to close.
func (e *Engine) Close() error {
	var errs []error
	if err := e.writers.closeAll(); err != nil {
		errs = append(errs, err)
	}
	if err := e.idx.Close(); err != nil {
		errs = append(errs, fmt.Errorf("close index: %w", err))
	}
	if err := e.lock.release(); err != nil {
		errs = append(errs, fmt.Errorf("release lock: %w", err))
	}
	if len(errs) > 0 {
		return fmt.Errorf("engine: close: %v", errs)
	}
	return nil
}
