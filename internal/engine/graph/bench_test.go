package graph_test

import (
	"testing"

	"github.com/medbeads/medbeads/internal/engine"
	"github.com/medbeads/medbeads/internal/engine/bead"
	"github.com/medbeads/medbeads/internal/engine/graph"
)

// bigPatientBeadCount mirrors specs/DESIGN_v3.md §3's "~900 Bead" patient
// sub-graph size estimate (the number docs/requirements.md §7's <10ms
// patient-bundle-load performance target is measured against).
const bigPatientBeadCount = 900

// seedBigPatient ingests a patient_registration root plus (n-1) children
// (a flat fan-out from the root, which is enough to exercise LoadBundle's
// full-Pod-scan + adjacency-list-build path at realistic size without this
// helper itself becoming the bottleneck under test).
func seedBigPatient(t testing.TB, e *engine.Engine, n int) bead.Bead {
	t.Helper()
	root := ingestT2(t, e, unsavedBead("patient_registration", nil, map[string]any{"name": "bench patient"}))
	for i := 1; i < n; i++ {
		ingestT2(t, e, unsavedBead("fhir_observation", []string{root.ID},
			map[string]any{"note": "observation content for performance smoke testing, long enough to be realistic"}))
	}
	return root
}

// ingestT2 is ingestT generalized to testing.TB (BenchmarkLoadBundle uses a
// *testing.B, which testing.T-only ingestT/seedPatient/seedChildBead cannot
// accept).
func ingestT2(t testing.TB, e *engine.Engine, b bead.Bead) bead.Bead {
	t.Helper()
	out, err := e.Ingest(b)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	return out
}

// TestLoadBundle_LargePatient_LogicalCorrectness is the "性能スモーク" test's
// non-flaky half: it seeds a ~900-Bead patient and checks LoadBundle's
// output is logically complete (every Bead present, BFS still reaches the
// full depth), without asserting on wall-clock time — timing itself belongs
// in BenchmarkLoadBundle (testing.B), run explicitly via `go test -bench`,
// so CI's normal test run never flakes on a shared/loaded machine's timing
// noise (per this task's requirement: "CI で flaky にならないようベンチは
// testing.B で分離し、通常テストでは論理検証のみ").
func TestLoadBundle_LargePatient_LogicalCorrectness(t *testing.T) {
	e := openT(t)
	root := seedBigPatient(t, e, bigPatientBeadCount)

	bd, err := graph.LoadBundle(storeFor(e), root.ID)
	if err != nil {
		t.Fatalf("LoadBundle: %v", err)
	}
	if bd.Beads() != bigPatientBeadCount {
		t.Fatalf("Bundle.Beads() = %d, want %d", bd.Beads(), bigPatientBeadCount)
	}

	// Every one of the (n-1) children is a direct child of root: Descendants
	// from root at depth 1 must reach all of them plus root itself.
	desc := bd.Descendants(root.ID, 1)
	if len(desc) != bigPatientBeadCount {
		t.Errorf("Descendants(root, 1) returned %d beads, want %d", len(desc), bigPatientBeadCount)
	}
}

// BenchmarkLoadBundle measures LoadBundle's wall-clock cost against a
// ~900-Bead patient — the docs/requirements.md §7 "患者バンドル取得 <10ms"
// target — as a reference number only (see this task's report for the
// actual measured value on the machine it was run on); this is not asserted
// against in-band (a hard-coded ns/op ceiling in a benchmark would itself be
// a flaky-CI trap on slower/shared hardware), per the same flakiness
// concern TestLoadBundle_LargePatient_LogicalCorrectness's doc comment
// explains. Run explicitly:
//
//	CGO_ENABLED=1 go test -tags sqlite_fts5 -bench BenchmarkLoadBundle -benchtime=20x ./internal/engine/graph/...
func BenchmarkLoadBundle(b *testing.B) {
	e := openT(b)
	root := seedBigPatient(b, e, bigPatientBeadCount)
	store := storeFor(e)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := graph.LoadBundle(store, root.ID); err != nil {
			b.Fatalf("LoadBundle: %v", err)
		}
	}
}
