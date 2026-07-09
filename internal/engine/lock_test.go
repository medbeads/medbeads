package engine

import (
	"strings"
	"testing"
)

// TestOpen_SecondOpenOnSameDataDirFails checks the "one Engine per data
// directory" invariant (engine.go): a second Open against a directory an
// Engine already holds open must fail fast, not block or silently corrupt
// state.
func TestOpen_SecondOpenOnSameDataDirFails(t *testing.T) {
	dir := t.TempDir()

	e1, err := Open(dir)
	if err != nil {
		t.Fatalf("Open (first): %v", err)
	}
	defer e1.Close()

	_, err = Open(dir)
	if err == nil {
		t.Fatal("Open (second, same dir, first still open) succeeded, want error")
	}
	if !strings.Contains(err.Error(), "already locked") {
		t.Errorf("Open (second) error = %v, want it to mention the lock", err)
	}
}

// TestOpen_ReopenAfterCloseSucceeds checks that closing an Engine releases
// the lock so a subsequent Open against the same directory works cleanly
// (not a permanent lockout — only a concurrent-open guard).
func TestOpen_ReopenAfterCloseSucceeds(t *testing.T) {
	dir := t.TempDir()

	e1, err := Open(dir)
	if err != nil {
		t.Fatalf("Open (first): %v", err)
	}
	if err := e1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	e2, err := Open(dir)
	if err != nil {
		t.Fatalf("Open (after close): %v", err)
	}
	if err := e2.Close(); err != nil {
		t.Fatalf("Close (second): %v", err)
	}
}
