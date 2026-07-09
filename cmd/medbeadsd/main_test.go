package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/medbeads/medbeads/internal/engine/bead"
	"github.com/medbeads/medbeads/internal/engine/index"
	"github.com/medbeads/medbeads/internal/engine/pod"
)

func TestRun(t *testing.T) {
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer devNull.Close()

	tests := []struct {
		name string
		args []string
		want int
	}{
		{name: "no args prints usage", args: nil, want: 0},
		{name: "help flag", args: []string{"-h"}, want: 0},
		{name: "serve not implemented", args: []string{"serve"}, want: 1},
		{name: "verify without -data is a usage error", args: []string{"verify"}, want: 2},
		{name: "reindex without -data is a usage error", args: []string{"reindex"}, want: 2},
		{name: "unknown command", args: []string{"bogus"}, want: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := run(tt.args, devNull, devNull); got != tt.want {
				t.Errorf("run(%v) = %d, want %d", tt.args, got, tt.want)
			}
		})
	}
}

// TestRun_VerifyEmptyDataDir exercises `medbeadsd verify -data <dir>`
// against a data directory with no pods/ at all: this must succeed (exit 0)
// and report zero Pod files, not fail.
func TestRun_VerifyEmptyDataDir(t *testing.T) {
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer devNull.Close()

	dir := t.TempDir()
	if got := run([]string{"verify", "-data", dir}, devNull, devNull); got != 0 {
		t.Errorf("run(verify -data %s) = %d, want 0", dir, got)
	}
}

// TestRun_VerifyRealPods exercises `medbeadsd verify -data <dir>` against a
// real, on-disk Pod file written via the pod package's own Writer, ensuring
// the CLI is actually wired to pod.VerifyAll end-to-end.
func TestRun_VerifyRealPods(t *testing.T) {
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer devNull.Close()

	dir := t.TempDir()
	store := pod.NewStore(dir)

	b, err := bead.WithID(bead.Bead{
		Type:      "patient_registration",
		Timestamp: "2026-01-01T00:00:00Z",
		Content:   map[string]any{"name": "Synthea Test Patient"},
	})
	if err != nil {
		t.Fatalf("bead.WithID: %v", err)
	}

	podPath, err := store.EnsurePatientPodDir(b.ID)
	if err != nil {
		t.Fatalf("EnsurePatientPodDir: %v", err)
	}
	w, err := pod.OpenWriter(podPath)
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	if _, err := w.Append(b, pod.CodecZstd, pod.NewMeta(b.ID)); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if got := run([]string{"verify", "-data", dir}, devNull, devNull); got != 0 {
		t.Errorf("run(verify -data %s) = %d, want 0 (clean pod)", dir, got)
	}

	// Corrupt the pod file on disk, then confirm verify reports failure.
	f, err := os.OpenFile(podPath, os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("open for corruption: %v", err)
	}
	if _, err := f.WriteAt([]byte{0xFF}, 40); err != nil { // inside bead_id/core_bytes region
		t.Fatalf("corrupt pod file: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close after corruption: %v", err)
	}

	if got := run([]string{"verify", "-data", dir}, devNull, devNull); got != 1 {
		t.Errorf("run(verify -data %s) after corruption = %d, want 1 (verification failure)", dir, got)
	}
}

// TestRun_ReindexRealPods exercises `medbeadsd reindex -data <dir>` against
// a real, on-disk Pod file, ensuring the CLI is actually wired to
// index.Reindex end-to-end: index.db is created at <dir>/index.db and the
// written Bead is resolvable afterward.
func TestRun_ReindexRealPods(t *testing.T) {
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer devNull.Close()

	dir := t.TempDir()
	store := pod.NewStore(dir)

	b, err := bead.WithID(bead.Bead{
		Type:      "patient_registration",
		Timestamp: "2026-01-01T00:00:00Z",
		Content:   map[string]any{"name": "Synthea Test Patient"},
	})
	if err != nil {
		t.Fatalf("bead.WithID: %v", err)
	}

	podPath, err := store.EnsurePatientPodDir(b.ID)
	if err != nil {
		t.Fatalf("EnsurePatientPodDir: %v", err)
	}
	w, err := pod.OpenWriter(podPath)
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	if _, err := w.Append(b, pod.CodecZstd, pod.NewMeta(b.ID)); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if got := run([]string{"reindex", "-data", dir}, devNull, devNull); got != 0 {
		t.Fatalf("run(reindex -data %s) = %d, want 0", dir, got)
	}

	dbPath := filepath.Join(dir, "index.db")
	db, err := index.Open(dbPath)
	if err != nil {
		t.Fatalf("index.Open(%s) after reindex: %v", dbPath, err)
	}
	defer db.Close()

	ref, err := db.GetBead(b.ID)
	if err != nil {
		t.Fatalf("GetBead(%s) after reindex: %v", b.ID, err)
	}
	if ref.PatientRoot != b.ID {
		t.Errorf("ref.PatientRoot = %q, want %q", ref.PatientRoot, b.ID)
	}
}

// TestRun_ReindexEmptyDataDir exercises `medbeadsd reindex -data <dir>`
// against a data directory with no pods/ at all: this must succeed (exit 0)
// and produce an index.db with zero Beads, not fail.
func TestRun_ReindexEmptyDataDir(t *testing.T) {
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer devNull.Close()

	dir := t.TempDir()
	if got := run([]string{"reindex", "-data", dir}, devNull, devNull); got != 0 {
		t.Errorf("run(reindex -data %s) = %d, want 0", dir, got)
	}
}
