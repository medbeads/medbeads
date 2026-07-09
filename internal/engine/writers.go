package engine

import (
	"fmt"
	"sync"

	"github.com/medbeads/medbeads/internal/engine/pod"
)

// writerRegistry is a process-in-Engine-wide map[path]*pod.Writer plus a
// mutex, guaranteeing exactly one *pod.Writer instance exists per Pod path
// for the lifetime of an Engine. package pod's Writer only guarantees that a
// single Writer instance is itself concurrency-safe (see pod.Writer's doc
// comment: "a process-wide registry of per-path Writers is the caller's
// job") — this is that registry. Without it, two goroutines calling
// Engine.Ingest for the same patient concurrently could each open their own
// *os.File against the same Pod path; both would independently Stat() the
// file to compute their frame's offset (pod.Writer.Append's documented
// approach), and neither Writer's internal mutex would serialize the other's
// Stat-then-Write, reintroducing exactly the offset race Writer's own
// single-instance mutex is meant to prevent.
type writerRegistry struct {
	mu      sync.Mutex
	writers map[string]*pod.Writer
}

func newWriterRegistry() *writerRegistry {
	return &writerRegistry{writers: make(map[string]*pod.Writer)}
}

// get returns the *pod.Writer for path, opening (and caching) one if this is
// the first request for that path. Concurrent calls for the same path
// (or different paths) are safe; only one goroutine ever creates the Writer
// for a given path.
func (r *writerRegistry) get(path string) (*pod.Writer, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if w, ok := r.writers[path]; ok {
		return w, nil
	}
	w, err := pod.OpenWriter(path)
	if err != nil {
		return nil, fmt.Errorf("engine: writer registry: open %s: %w", path, err)
	}
	r.writers[path] = w
	return w, nil
}

// closeAll closes every Writer this registry has opened, collecting (rather
// than stopping at the first) any Close errors so Engine.Close reports every
// Pod that failed to close cleanly.
func (r *writerRegistry) closeAll() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	var errs []error
	for path, w := range r.writers {
		if err := w.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close writer %s: %w", path, err))
		}
	}
	r.writers = make(map[string]*pod.Writer)
	if len(errs) > 0 {
		return fmt.Errorf("engine: writer registry: close all: %v", errs)
	}
	return nil
}
