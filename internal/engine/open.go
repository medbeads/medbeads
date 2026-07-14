package engine

import (
	"fmt"
	"path/filepath"
	"runtime/debug"
	"sync"

	"github.com/medbeads/medbeads/internal/engine/index"
	"github.com/medbeads/medbeads/internal/engine/pod"
	"github.com/medbeads/medbeads/internal/engine/trust"
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

	// ingestMu covers the append -> index/projection commit protocol. The
	// SQLite pool already serializes transactions, but this wider lock also
	// prevents two goroutines from both passing the pre-append idempotency
	// check and guarantees patient-local projection observes appends in order.
	ingestMu       sync.Mutex
	autoProjection *autoProjection
	trustPolicy    *trust.Policy
}

// projectionAlgorithmVersion is bumped for a deliberate projector-contract
// revision. DefaultProjectionCodeVersion additionally incorporates Go's VCS
// build metadata so a newly committed binary cannot silently write rows using
// new code under an older projection generation.
const projectionAlgorithmVersion = "medbeads-auto-v1"

// record_state has an independent contract version. A routine binary/git
// revision may change link logic and start a rolling link rollout, but it must
// not force an unrelated synchronous status rebuild across a million patients.
// Bump this value only when correction-chain semantics actually change.
const recordStateProjectionVersion = "record-state-v1"

func DefaultRecordStateProjectionCodeVersion() string {
	return recordStateProjectionVersion
}

// DefaultProjectionCodeVersion returns an auditable default code generation.
// Release builds normally carry vcs.revision; ad-hoc builds fall back to the Go
// module version or an explicit devel marker. Operators may still supply their
// own release/git identifier through OpenOptions.
func DefaultProjectionCodeVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return projectionAlgorithmVersion + "+devel"
	}

	revision := ""
	modified := false
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}
	if revision != "" {
		version := projectionAlgorithmVersion + "+git." + revision
		if modified {
			version += ".dirty"
		}
		return version
	}
	if info.Main.Version != "" && info.Main.Version != "(devel)" {
		return projectionAlgorithmVersion + "+" + info.Main.Version
	}
	return projectionAlgorithmVersion + "+devel"
}

// OpenOptions controls optional derived-state maintenance. Open intentionally
// keeps its historical low-level behavior for projector tests and maintenance
// commands; production serving enables AutoProject through OpenWithOptions.
type OpenOptions struct {
	AutoProject                  bool
	ProjectionCodeVersion        string
	RecordStateProjectionVersion string
	// TrustPolicy enables cryptographic knowledge-release enforcement. The
	// policy contains public keys only; private keys never enter Engine.
	TrustPolicy *trust.Policy
	// InitialKnowledgeBeadIDs is used by the reproject CLI to activate a
	// pre-published, signed release when no trusted active generation exists.
	InitialKnowledgeBeadIDs []string
}

// Open acquires the data directory's advisory lock, opens (creating if
// necessary) its Pod store and index.db, and runs crash recovery
// (index.CatchUp for every Pod file found) before returning. The returned
// Engine must be Closed to release the lock and close the index database.
// It preserves the low-level/manual projection behavior; production servers
// use OpenWithOptions with AutoProject=true.
//
// A second Open against the same dataDir — from this process or another —
// fails fast (see lock.go): only one Engine may be open per data directory
// at a time.
func Open(dataDir string) (*Engine, error) {
	return OpenWithOptions(dataDir, OpenOptions{})
}

// OpenWithOptions is Open plus optional automatic patient-local projection.
func OpenWithOptions(dataDir string, opts OpenOptions) (*Engine, error) {
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
	if opts.TrustPolicy != nil {
		if err := opts.TrustPolicy.Validate(); err != nil {
			e.writers.closeAll() //nolint:errcheck
			idx.Close()          //nolint:errcheck
			lock.release()       //nolint:errcheck
			return nil, fmt.Errorf("engine: open %s: trust policy: %w", dataDir, err)
		}
		policyCopy := opts.TrustPolicy.Clone()
		e.trustPolicy = &policyCopy
	}

	if err := e.catchUpAll(); err != nil {
		idx.Close()    //nolint:errcheck
		lock.release() //nolint:errcheck
		return nil, fmt.Errorf("engine: open %s: crash recovery: %w", dataDir, err)
	}

	if opts.AutoProject {
		codeVersion := opts.ProjectionCodeVersion
		if codeVersion == "" {
			codeVersion = DefaultProjectionCodeVersion()
		}
		statusVersion := opts.RecordStateProjectionVersion
		if statusVersion == "" {
			statusVersion = DefaultRecordStateProjectionCodeVersion()
		}
		if err := e.initializeAutoProjection(codeVersion, statusVersion, opts.InitialKnowledgeBeadIDs); err != nil {
			e.writers.closeAll() //nolint:errcheck
			idx.Close()          //nolint:errcheck
			lock.release()       //nolint:errcheck
			return nil, fmt.Errorf("engine: open %s: initialize automatic projection: %w", dataDir, err)
		}
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
		if err := index.CatchUp(e.idx, e.podStore, path); err != nil {
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

// Index exposes this Engine's *index.DB for callers (e.g. package apc's
// Scanner) that need direct SQL access to tables Ingest's own thin read API
// does not expose (bead_apc_scan, sibling_pairs, bead_tags lookups by
// (tag, patient_root)) — the same "narrow escape hatch" as index.DB's own
// SQLDB(). Routing through this accessor rather than having apc open its own
// second *sql.DB against index.db keeps every SQLite connection to a given
// data directory going through Engine's single capped connection pool (see
// index.Open's SetMaxOpenConns(1) doc comment) instead of adding a second,
// separately-pooled connection that would contend with it.
func (e *Engine) Index() *index.DB {
	return e.idx
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
