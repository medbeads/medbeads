package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// lockFileName is the name of the advisory-lock file created directly under
// a data directory, guarding the "one Engine per data directory per
// process(es)" invariant documented in engine.go. It is deliberately not a
// Pod or index file so pod.Store.ListPodFiles / index.Reindex never need to
// know about it.
const lockFileName = ".medbeads.lock"

// dataDirLock holds the open file descriptor an flock(2) exclusive lock was
// taken on. Closing the underlying file (via Close) releases the lock.
type dataDirLock struct {
	f *os.File
}

// acquireDataDirLock creates (if needed) and flock-locks dataDir/.medbeads.lock
// in non-blocking exclusive mode. If another process (or another Open call
// in this process) already holds the lock, it returns an error immediately
// rather than blocking — Open is meant to fail fast on a second instance,
// not queue behind one.
func acquireDataDirLock(dataDir string) (*dataDirLock, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("engine: acquire lock: create data dir: %w", err)
	}
	path := filepath.Join(dataDir, lockFileName)

	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("engine: acquire lock: open %s: %w", path, err)
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("engine: acquire lock: %s is already locked by another process (only one Engine may be open per data directory): %w", path, err)
	}

	return &dataDirLock{f: f}, nil
}

// release unlocks and closes the lock file. It is safe to call at most once
// (Engine.Close's job to ensure that).
func (l *dataDirLock) release() error {
	// Unlocking is implied by Close on most platforms, but doing it
	// explicitly first documents intent and surfaces a distinct error if
	// unlocking itself somehow fails, rather than folding it silently into
	// the Close error.
	if err := syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN); err != nil {
		l.f.Close()
		return fmt.Errorf("engine: release lock: unlock: %w", err)
	}
	if err := l.f.Close(); err != nil {
		return fmt.Errorf("engine: release lock: close: %w", err)
	}
	return nil
}
