package engine

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/medbeads/medbeads/internal/engine/bead"
)

// TestIngest_ConcurrentMultiPatient ingests several patients concurrently,
// each from its own goroutine, each appending several children to its own
// root — exercising the per-path Writer registry (writers.go) under
// concurrent Ingest calls across *different* Pod paths, and confirming no
// cross-patient interference. Run with -race.
func TestIngest_ConcurrentMultiPatient(t *testing.T) {
	e := openT(t)

	const patients = 8
	const childrenPerPatient = 20

	var wg sync.WaitGroup
	roots := make([]string, patients)
	errs := make([]error, patients)

	for p := 0; p < patients; p++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			root, err := e.Ingest(bead.Bead{
				Type:      "patient_registration",
				Timestamp: fmt.Sprintf("2026-02-01T00:%02d:00Z", p),
				Content:   map[string]any{"name": fmt.Sprintf("patient-%d", p)},
			})
			if err != nil {
				errs[p] = fmt.Errorf("ingest root %d: %w", p, err)
				return
			}
			roots[p] = root.ID

			for c := 0; c < childrenPerPatient; c++ {
				_, err := e.Ingest(bead.Bead{
					Type:      "fhir_observation",
					Timestamp: fmt.Sprintf("2026-02-01T01:%02d:%02dZ", p, c),
					Parents:   []string{root.ID},
					Content:   map[string]any{"note": fmt.Sprintf("obs-%d-%d", p, c)},
				})
				if err != nil {
					errs[p] = fmt.Errorf("ingest child %d/%d: %w", p, c, err)
					return
				}
			}
		}(p)
	}
	wg.Wait()

	for p, err := range errs {
		if err != nil {
			t.Fatalf("patient %d: %v", p, err)
		}
	}

	for p, root := range roots {
		all, err := e.ListPatientBeads(root)
		if err != nil {
			t.Fatalf("ListPatientBeads(patient %d): %v", p, err)
		}
		if len(all) != 1+childrenPerPatient {
			t.Errorf("patient %d: ListPatientBeads returned %d beads, want %d", p, len(all), 1+childrenPerPatient)
		}
	}
}

// TestIngest_ConcurrentSamePatient ingests many children of the *same*
// patient root concurrently from multiple goroutines — the case the
// per-path Writer registry exists specifically to serialize safely (all
// goroutines contend for the same *pod.Writer instance). Run with -race.
func TestIngest_ConcurrentSamePatient(t *testing.T) {
	e := openT(t)

	root, err := e.Ingest(unsavedBead("patient_registration", nil, map[string]any{"name": "shared root"}))
	if err != nil {
		t.Fatalf("Ingest (root): %v", err)
	}

	const n = 60
	var wg sync.WaitGroup
	errs := make([]error, n)
	ids := make([]string, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			b, err := e.Ingest(bead.Bead{
				Type:      "fhir_observation",
				Timestamp: fmt.Sprintf("2026-03-01T00:00:%02dZ", i%60),
				Parents:   []string{root.ID},
				Content:   map[string]any{"n": i},
			})
			errs[i] = err
			if err == nil {
				ids[i] = b.ID
			}
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("Ingest %d: %v", i, err)
		}
	}

	all, err := e.ListPatientBeads(root.ID)
	if err != nil {
		t.Fatalf("ListPatientBeads: %v", err)
	}
	if len(all) != n+1 {
		t.Fatalf("ListPatientBeads returned %d beads, want %d (root + %d children)", len(all), n+1, n)
	}

	seen := make(map[string]bool, n)
	for _, b := range all {
		seen[b.ID] = true
	}
	for i, id := range ids {
		if !seen[id] {
			t.Errorf("child %d (id=%s) missing from ListPatientBeads", i, id)
		}
	}
}

// TestIngest_PerformanceSmoke ingests ~900 Beads for a single patient (the
// DESIGN §3 target patient sub-graph size) and checks ListPatientBeads
// completes in a small, generous bound — not a strict benchmark, just a
// guard against an obvious N+1 (e.g. one index query per Bead) creeping into
// ListPatientBeads or the patient_root resolution path.
func TestIngest_PerformanceSmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping performance smoke test in -short mode")
	}
	e := openT(t)

	root, err := e.Ingest(unsavedBead("patient_registration", nil, map[string]any{"name": "perf patient"}))
	if err != nil {
		t.Fatalf("Ingest (root): %v", err)
	}

	const n = 900
	start := time.Now()
	prev := root.ID
	for i := 0; i < n; i++ {
		b, err := e.Ingest(unsavedBead("fhir_observation", []string{prev}, map[string]any{"i": i}))
		if err != nil {
			t.Fatalf("Ingest %d: %v", i, err)
		}
		prev = b.ID
	}
	ingestElapsed := time.Since(start)

	listStart := time.Now()
	all, err := e.ListPatientBeads(root.ID)
	if err != nil {
		t.Fatalf("ListPatientBeads: %v", err)
	}
	listElapsed := time.Since(listStart)

	if len(all) != n+1 {
		t.Fatalf("ListPatientBeads returned %d beads, want %d", len(all), n+1)
	}

	t.Logf("ingested %d beads in %v (%.2f ms/bead); ListPatientBeads(%d beads) took %v",
		n, ingestElapsed, float64(ingestElapsed.Microseconds())/1000/float64(n), len(all), listElapsed)

	// Generous bound: a genuine N+1 in patient_root resolution or listing
	// would make this take seconds to tens of seconds for 900 beads; a
	// correct single-query-per-step implementation finishes in well under a
	// second even on a slow CI machine.
	if listElapsed > 5*time.Second {
		t.Errorf("ListPatientBeads(%d beads) took %v, want well under 5s (possible N+1)", len(all), listElapsed)
	}
	if ingestElapsed > 30*time.Second {
		t.Errorf("ingesting %d beads took %v, want well under 30s (possible N+1)", n, ingestElapsed)
	}
}
