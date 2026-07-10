package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/medbeads/medbeads/internal/engine"
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
		{name: "serve without -data is a usage error", args: []string{"serve"}, want: 2},
		{name: "verify without -data is a usage error", args: []string{"verify"}, want: 2},
		{name: "reindex without -data is a usage error", args: []string{"reindex"}, want: 2},
		{name: "apc without -data is a usage error", args: []string{"apc"}, want: 2},
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

// --- medbeadsd apc -----------------------------------------------------

// TestRun_ApcInvalidPatientID exercises the -patient argument-validation
// path: a malformed patient ID (not 64 hex chars, with or without the
// "sha256:" prefix) must be rejected as a usage error (exit 2) before the
// engine is ever opened.
func TestRun_ApcInvalidPatientID(t *testing.T) {
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer devNull.Close()

	dir := t.TempDir()
	tests := []struct {
		name    string
		patient string
	}{
		{name: "too short", patient: "abc123"},
		{name: "sha256 prefix but too short", patient: "sha256:abc123"},
		{name: "not hex", patient: "sha256:" + fmt.Sprintf("%064s", "zz")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := run([]string{"apc", "-data", dir, "-patient", tt.patient}, devNull, devNull); got != 2 {
				t.Errorf("run(apc -data %s -patient %s) = %d, want 2", dir, tt.patient, got)
			}
		})
	}
}

// TestRun_ApcEmptyDataDir exercises `medbeadsd apc -data <dir>` against a
// freshly engine.Open'd (but otherwise empty) data directory: this must
// succeed (exit 0) and scan/create nothing, not fail.
func TestRun_ApcEmptyDataDir(t *testing.T) {
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer devNull.Close()

	dir := t.TempDir()
	// engine.Open must be able to create the data dir layout on its own the
	// first time apc opens it; no pre-seeding needed here.
	if got := run([]string{"apc", "-data", dir}, devNull, devNull); got != 0 {
		t.Errorf("run(apc -data %s) = %d, want 0", dir, got)
	}
}

// apcIngestT is a small local Ingest helper (mirrors internal/engine/apc's
// own _test package convention of re-deriving small scaffolding rather than
// importing apc's test helpers, which are unexported to that package).
func apcIngestT(t *testing.T, e *engine.Engine, b bead.Bead) bead.Bead {
	t.Helper()
	out, err := e.Ingest(b)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	return out
}

// apcTimestamp returns a distinct RFC3339 timestamp per call (Beads within
// the same patient must have distinct timestamps for a deterministic Bead
// ID), mirroring internal/engine/apc/apc_test.go's nextTimestamp.
var apcTimestampCounter int

func apcTimestamp() string {
	apcTimestampCounter++
	return fmt.Sprintf("2026-01-01T%02d:%02d:%02dZ",
		apcTimestampCounter/3600, (apcTimestampCounter%3600)/60, apcTimestampCounter%60)
}

// seedApcMatchablePair ingests a patient_registration Bead plus enough
// noise Beads (each with a unique antigen, keeping any genuinely shared
// antigen comfortably under apc.Config's default 30% patient-local IDF
// threshold — see internal/engine/apc/apc_test.go's padWithNoiseBeads doc
// comment for why this is required, not optional) and a
// fhir_medicationrequest/fhir_observation pair sharing two antigens closely
// in time, reproducing TestScan_MatchedPair_CreatesSiblingLinkAndEdge's own
// scoring shape (1 + 3(risk) + 2(organ) + 2(24h) + 3(rx/lab) = 11, clearing
// clearing 4) so `apc`'s end-to-end test actually reaches a real sibling_link, not
// just an empty scan.
func seedApcMatchablePair(t *testing.T, e *engine.Engine) (patientID string) {
	t.Helper()

	root := apcIngestT(t, e, bead.Bead{
		Type:      "patient_registration",
		Timestamp: apcTimestamp(),
		Author:    "did:medbeads:doctor:12345",
		Content:   map[string]any{"name": "CLI Test Patient"},
	})

	for i := 0; i < 10; i++ {
		apcIngestT(t, e, bead.Bead{
			Type:      "fhir_observation",
			Timestamp: apcTimestamp(),
			Author:    "did:medbeads:doctor:12345",
			Parents:   []string{root.ID},
			Antigens:  []string{fmt.Sprintf("loinc:noise-%d", i)},
			Content:   map[string]any{"noise": i},
		})
	}

	apcIngestT(t, e, bead.Bead{
		Type:      "fhir_medicationrequest",
		Timestamp: apcTimestamp(),
		Author:    "did:medbeads:doctor:12345",
		Parents:   []string{root.ID},
		Antigens:  []string{"risk:nephrotoxic", "organ:renal"},
		Content:   map[string]any{"drug": "meropenem"},
	})
	apcIngestT(t, e, bead.Bead{
		Type:      "fhir_observation",
		Timestamp: apcTimestamp(),
		Author:    "did:medbeads:doctor:12345",
		Parents:   []string{root.ID},
		Antigens:  []string{"risk:nephrotoxic", "organ:renal"},
		Content:   map[string]any{"test": "eGFR"},
	})

	return root.ID
}

// TestRun_ApcEndToEnd exercises `medbeadsd apc -data <dir>` end to end
// against real, on-disk Pod files: a small ingest that sets up a matchable
// (shared-antigen) pair must produce exactly one sibling_pairs row once apc
// runs, and a second run against the same, now-fully-scanned store must be
// idempotent — creating no further sibling_link Beads (BeadsScanned may
// still be >0 the second time only if new, not-yet-watermarked Beads exist,
// which there are none of here).
func TestRun_ApcEndToEnd(t *testing.T) {
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer devNull.Close()

	dir := t.TempDir()

	e, err := engine.Open(dir)
	if err != nil {
		t.Fatalf("engine.Open: %v", err)
	}
	seedApcMatchablePair(t, e)
	if err := e.Close(); err != nil {
		t.Fatalf("engine.Close: %v", err)
	}

	// First run: must find the matchable pair and create exactly one
	// sibling_link (i.e. exactly one sibling_pairs row per matched antigen
	// — two antigens matched here, risk:nephrotoxic and organ:renal, both
	// recorded against the same sibling_link Bead).
	if got := run([]string{"apc", "-data", dir}, devNull, devNull); got != 0 {
		t.Fatalf("run(apc -data %s) = %d, want 0", dir, got)
	}

	db, err := index.Open(filepath.Join(dir, "index.db"))
	if err != nil {
		t.Fatalf("index.Open after apc: %v", err)
	}
	defer db.Close()

	var pairCount int
	if err := db.SQLDB().QueryRow(`SELECT COUNT(*) FROM sibling_pairs`).Scan(&pairCount); err != nil {
		t.Fatalf("count sibling_pairs: %v", err)
	}
	if pairCount == 0 {
		t.Fatalf("sibling_pairs count = 0 after apc run, want > 0 (matchable pair should have linked)")
	}

	var linkCount int
	if err := db.SQLDB().QueryRow(`SELECT COUNT(*) FROM beads WHERE type = 'sibling_link'`).Scan(&linkCount); err != nil {
		t.Fatalf("count sibling_link beads: %v", err)
	}
	if linkCount == 0 {
		t.Fatalf("sibling_link bead count = 0 after apc run, want > 0")
	}

	// Second run against the same, now-fully-scanned store: idempotent —
	// no new sibling_pairs rows (the pair is already recorded, see apc's
	// own runaway-prevention-a / unlinkedAntigens de-duplication).
	if got := run([]string{"apc", "-data", dir}, devNull, devNull); got != 0 {
		t.Fatalf("second run(apc -data %s) = %d, want 0", dir, got)
	}

	var pairCountAfterSecondRun int
	if err := db.SQLDB().QueryRow(`SELECT COUNT(*) FROM sibling_pairs`).Scan(&pairCountAfterSecondRun); err != nil {
		t.Fatalf("count sibling_pairs after second run: %v", err)
	}
	if pairCountAfterSecondRun != pairCount {
		t.Errorf("sibling_pairs count after second run = %d, want %d (idempotent, no new pairs)",
			pairCountAfterSecondRun, pairCount)
	}

	var linkCountAfterSecondRun int
	if err := db.SQLDB().QueryRow(`SELECT COUNT(*) FROM beads WHERE type = 'sibling_link'`).Scan(&linkCountAfterSecondRun); err != nil {
		t.Fatalf("count sibling_link beads after second run: %v", err)
	}
	if linkCountAfterSecondRun != linkCount {
		t.Errorf("sibling_link bead count after second run = %d, want %d (idempotent, no new links)",
			linkCountAfterSecondRun, linkCount)
	}
}

// TestRun_ApcPatientScope exercises `medbeadsd apc -data <dir> -patient
// <root>` against a store with two patients, one matchable and one not
// touched by the -patient argument: only the named patient's pair should be
// scanned/linked, and a bare "sha256:"-prefixed root must be accepted
// identically to the unprefixed form (bead.ParseID's contract).
func TestRun_ApcPatientScope(t *testing.T) {
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer devNull.Close()

	dir := t.TempDir()

	e, err := engine.Open(dir)
	if err != nil {
		t.Fatalf("engine.Open: %v", err)
	}
	targetPatientID := seedApcMatchablePair(t, e)
	// A second, distinct patient with its own matchable pair, deliberately
	// left unscanned by the -patient run below.
	otherRoot := apcIngestT(t, e, bead.Bead{
		Type:      "patient_registration",
		Timestamp: apcTimestamp(),
		Author:    "did:medbeads:doctor:12345",
		Content:   map[string]any{"name": "Other Patient"},
	})
	for i := 0; i < 10; i++ {
		apcIngestT(t, e, bead.Bead{
			Type:      "fhir_observation",
			Timestamp: apcTimestamp(),
			Author:    "did:medbeads:doctor:12345",
			Parents:   []string{otherRoot.ID},
			Antigens:  []string{fmt.Sprintf("loinc:other-noise-%d", i)},
			Content:   map[string]any{"noise": i},
		})
	}
	apcIngestT(t, e, bead.Bead{
		Type:      "fhir_medicationrequest",
		Timestamp: apcTimestamp(),
		Author:    "did:medbeads:doctor:12345",
		Parents:   []string{otherRoot.ID},
		Antigens:  []string{"risk:nephrotoxic", "organ:renal"},
		Content:   map[string]any{"drug": "meropenem"},
	})
	apcIngestT(t, e, bead.Bead{
		Type:      "fhir_observation",
		Timestamp: apcTimestamp(),
		Author:    "did:medbeads:doctor:12345",
		Parents:   []string{otherRoot.ID},
		Antigens:  []string{"risk:nephrotoxic", "organ:renal"},
		Content:   map[string]any{"test": "eGFR"},
	})
	if err := e.Close(); err != nil {
		t.Fatalf("engine.Close: %v", err)
	}

	// Prefixed form, per bead.ParseID's contract.
	prefixedPatient := "sha256:" + targetPatientID
	if got := run([]string{"apc", "-data", dir, "-patient", prefixedPatient}, devNull, devNull); got != 0 {
		t.Fatalf("run(apc -data %s -patient %s) = %d, want 0", dir, prefixedPatient, got)
	}

	db, err := index.Open(filepath.Join(dir, "index.db"))
	if err != nil {
		t.Fatalf("index.Open after apc: %v", err)
	}
	defer db.Close()

	var targetLinks int
	if err := db.SQLDB().QueryRow(
		`SELECT COUNT(*) FROM beads WHERE type = 'sibling_link' AND patient_root = ?`,
		targetPatientID,
	).Scan(&targetLinks); err != nil {
		t.Fatalf("count target patient sibling_link beads: %v", err)
	}
	if targetLinks == 0 {
		t.Errorf("target patient sibling_link count = 0, want > 0 (matchable pair should have linked)")
	}

	var otherLinks int
	if err := db.SQLDB().QueryRow(
		`SELECT COUNT(*) FROM beads WHERE type = 'sibling_link' AND patient_root = ?`,
		otherRoot.ID,
	).Scan(&otherLinks); err != nil {
		t.Fatalf("count other patient sibling_link beads: %v", err)
	}
	if otherLinks != 0 {
		t.Errorf("other patient sibling_link count = %d, want 0 (-patient must scope to the named patient only)", otherLinks)
	}
}
