package graph_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/medbeads/medbeads/internal/engine"
	"github.com/medbeads/medbeads/internal/engine/bead"
	"github.com/medbeads/medbeads/internal/engine/graph"
	"github.com/medbeads/medbeads/internal/engine/index"
	"github.com/medbeads/medbeads/internal/engine/pod"
)

// openT opens a fresh Engine under a t.TempDir() and registers a Close
// cleanup. Mirrors internal/engine/engine_test.go's openT, duplicated here
// since that helper is unexported in package engine and this file lives in
// package graph_test (graph itself does not import engine — see
// internal/engine/graph/doc.go). It takes testing.TB (not *testing.T) so
// BenchmarkLoadBundle (bench_test.go) can share it too.
func openT(t testing.TB) *engine.Engine {
	t.Helper()
	dir := t.TempDir()
	e, err := engine.Open(dir)
	if err != nil {
		t.Fatalf("engine.Open: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	return e
}

// storeFor returns a *pod.Store rooted at e's data directory, for calling
// graph.LoadBundle against Beads e.Ingest wrote.
func storeFor(e *engine.Engine) *pod.Store {
	return pod.NewStore(e.DataDir())
}

var timestampCounter int

// nextTimestamp returns a strictly increasing RFC3339 timestamp string,
// mirroring internal/engine/engine_test.go's nextTimestamp.
func nextTimestamp() string {
	timestampCounter++
	sec := timestampCounter
	h := sec / 3600
	m := (sec % 3600) / 60
	s := sec % 60
	return fmtTimestamp(h, m, s)
}

func fmtTimestamp(h, m, s int) string {
	const digits = "0123456789"
	pad := func(n int) string {
		return string([]byte{digits[n/10%10], digits[n%10]})
	}
	return "2026-01-01T" + pad(h) + ":" + pad(m) + ":" + pad(s) + "Z"
}

// unsavedBead returns an ID-less Bead ready for e.Ingest, mirroring
// internal/engine/engine_test.go's unsavedBead.
func unsavedBead(typ string, parents, antigens []string, content map[string]any) bead.Bead {
	if content == nil {
		content = map[string]any{}
	}
	return bead.Bead{
		Type:      typ,
		Timestamp: nextTimestamp(),
		Author:    "did:medbeads:doctor:12345",
		Parents:   parents,
		Antigens:  antigens,
		Content:   content,
	}
}

// ingestT ingests b via e and fails the test on error, returning the
// resulting (ID-assigned) Bead.
func ingestT(t *testing.T, e *engine.Engine, b bead.Bead) bead.Bead {
	t.Helper()
	out, err := e.Ingest(b)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	return out
}

// seedPatient ingests a patient_registration root Bead with the given name,
// mirroring v2.2.0's core/store/graph_test.go seedPatient.
func seedPatient(t *testing.T, e *engine.Engine, name string) bead.Bead {
	t.Helper()
	return ingestT(t, e, unsavedBead("patient_registration", nil, nil, map[string]any{"name": name}))
}

// seedChildBead ingests a Bead of the given type/content with parent as its
// sole parent, mirroring v2.2.0's core/store/graph_test.go seedChildBead.
func seedChildBead(t *testing.T, e *engine.Engine, parent bead.Bead, typ string, content map[string]any) bead.Bead {
	t.Helper()
	return ingestT(t, e, unsavedBead(typ, []string{parent.ID}, nil, content))
}

// collectIDs returns the set of Bead IDs in beads, for order-independent
// membership checks.
func collectIDs(beads []bead.Bead) map[string]bool {
	m := make(map[string]bool, len(beads))
	for _, b := range beads {
		m[b.ID] = true
	}
	return m
}

// --- Bundle construction -----------------------------------------------

func TestLoadBundle_AdjacencyListsFromRegistrationAndDescendants(t *testing.T) {
	e := openT(t)

	root := seedPatient(t, e, "A")
	b := seedChildBead(t, e, root, "fhir_encounter", map[string]any{"n": "B"})
	c := seedChildBead(t, e, b, "fhir_observation", map[string]any{"n": "C"})
	d := seedChildBead(t, e, b, "fhir_observation", map[string]any{"n": "D"})

	bd, err := graph.LoadBundle(storeFor(e), root.ID)
	if err != nil {
		t.Fatalf("LoadBundle: %v", err)
	}

	if bd.Beads() != 4 {
		t.Fatalf("Bundle.Beads() = %d, want 4", bd.Beads())
	}
	for _, id := range []string{root.ID, b.ID, c.ID, d.ID} {
		if _, ok := bd.Get(id); !ok {
			t.Errorf("Bundle missing bead %s", id)
		}
	}

	// children[root] should contain b; children[b] should contain c and d.
	desc := bd.Descendants(root.ID, 10)
	got := collectIDs(desc)
	for _, want := range []string{root.ID, b.ID, c.ID, d.ID} {
		if !got[want] {
			t.Errorf("Descendants(root, 10) missing %s", want)
		}
	}

	// parents[c] should contain b; parents[b] should contain root.
	anc := bd.Ancestors(c.ID, 10)
	gotAnc := collectIDs(anc)
	for _, want := range []string{c.ID, b.ID, root.ID} {
		if !gotAnc[want] {
			t.Errorf("Ancestors(c, 10) missing %s", want)
		}
	}
}

func TestLoadBundle_UnknownPatientReturnsErrPatientNotFound(t *testing.T) {
	e := openT(t)

	unknown := "0000000000000000000000000000000000000000000000000000000000000000"[:64]
	_, err := graph.LoadBundle(storeFor(e), unknown)
	if !errors.Is(err, graph.ErrPatientNotFound) {
		t.Fatalf("LoadBundle(unknown patient) err = %v, want ErrPatientNotFound", err)
	}
}

// --- Ancestors / Descendants / Siblings (v2 GetContext / GetBeadsByParent) --

// TestAncestors_WalksAncestors mirrors v2.2.0's TestGetContext_WalksAncestors:
// chain A (root) <- B <- C <- D, Ancestors(D, 10) must reach all four.
func TestAncestors_WalksAncestors(t *testing.T) {
	e := openT(t)

	a := seedPatient(t, e, "A")
	b := seedChildBead(t, e, a, "fhir_encounter", map[string]any{"n": "B"})
	c := seedChildBead(t, e, b, "fhir_observation", map[string]any{"n": "C"})
	d := seedChildBead(t, e, c, "fhir_observation", map[string]any{"n": "D"})

	bd, err := graph.LoadBundle(storeFor(e), a.ID)
	if err != nil {
		t.Fatalf("LoadBundle: %v", err)
	}

	got := collectIDs(bd.Ancestors(d.ID, 10))
	for _, want := range []string{a.ID, b.ID, c.ID, d.ID} {
		if !got[want] {
			t.Errorf("Ancestors(D, 10) missing %q", want)
		}
	}
}

// TestAncestors_RespectsDepth mirrors v2.2.0's TestGetContext_RespectsDepth:
// depth=1 from D reaches D and its direct parent C, but not the root A.
func TestAncestors_RespectsDepth(t *testing.T) {
	e := openT(t)

	a := seedPatient(t, e, "A")
	b := seedChildBead(t, e, a, "fhir_encounter", map[string]any{"n": "B"})
	c := seedChildBead(t, e, b, "fhir_observation", map[string]any{"n": "C"})
	d := seedChildBead(t, e, c, "fhir_observation", map[string]any{"n": "D"})

	bd, err := graph.LoadBundle(storeFor(e), a.ID)
	if err != nil {
		t.Fatalf("LoadBundle: %v", err)
	}

	got := collectIDs(bd.Ancestors(d.ID, 1))
	if !got[d.ID] || !got[c.ID] {
		t.Errorf("Ancestors(D, 1) should include D and C, got %v", got)
	}
	if got[a.ID] {
		t.Errorf("Ancestors(D, 1) should not reach the root A, got %v", got)
	}
	if got[b.ID] {
		t.Errorf("Ancestors(D, 1) should not reach B either, got %v", got)
	}
}

func TestAncestors_DepthZeroReturnsOnlySelf(t *testing.T) {
	e := openT(t)

	a := seedPatient(t, e, "A")
	b := seedChildBead(t, e, a, "fhir_encounter", nil)

	bd, err := graph.LoadBundle(storeFor(e), a.ID)
	if err != nil {
		t.Fatalf("LoadBundle: %v", err)
	}

	got := bd.Ancestors(b.ID, 0)
	if len(got) != 1 || got[0].ID != b.ID {
		t.Errorf("Ancestors(B, 0) = %v, want just [B]", collectIDs(got))
	}
}

// TestDescendants_WalksDescendants mirrors v2.2.0's
// TestGetBeadsByParent_WalksDescendants.
func TestDescendants_WalksDescendants(t *testing.T) {
	e := openT(t)

	a := seedPatient(t, e, "A")
	b := seedChildBead(t, e, a, "fhir_encounter", map[string]any{"n": "B"})
	c := seedChildBead(t, e, b, "fhir_observation", map[string]any{"n": "C"})

	bd, err := graph.LoadBundle(storeFor(e), a.ID)
	if err != nil {
		t.Fatalf("LoadBundle: %v", err)
	}

	got := collectIDs(bd.Descendants(a.ID, 10))
	for _, want := range []string{a.ID, b.ID, c.ID} {
		if !got[want] {
			t.Errorf("Descendants(A, 10) missing %q", want)
		}
	}
}

func TestDescendants_RespectsDepth(t *testing.T) {
	e := openT(t)

	a := seedPatient(t, e, "A")
	b := seedChildBead(t, e, a, "fhir_encounter", nil)
	c := seedChildBead(t, e, b, "fhir_observation", nil)

	bd, err := graph.LoadBundle(storeFor(e), a.ID)
	if err != nil {
		t.Fatalf("LoadBundle: %v", err)
	}

	got := collectIDs(bd.Descendants(a.ID, 1))
	if !got[a.ID] || !got[b.ID] {
		t.Errorf("Descendants(A, 1) should include A and B, got %v", got)
	}
	if got[c.ID] {
		t.Errorf("Descendants(A, 1) should not reach C, got %v", got)
	}
}

// TestSiblings_Implicit checks the same-parent-children semantics
// (specs/MEDBEADS_SIBLING_SPEC.md §2.6): siblings sharing a parent with id,
// excluding id itself.
func TestSiblings_Implicit(t *testing.T) {
	e := openT(t)

	a := seedPatient(t, e, "A")
	b1 := seedChildBead(t, e, a, "fhir_encounter", map[string]any{"n": "B1"})
	b2 := seedChildBead(t, e, a, "fhir_encounter", map[string]any{"n": "B2"})
	b3 := seedChildBead(t, e, a, "fhir_encounter", map[string]any{"n": "B3"})

	bd, err := graph.LoadBundle(storeFor(e), a.ID)
	if err != nil {
		t.Fatalf("LoadBundle: %v", err)
	}

	got := collectIDs(bd.Siblings(b1.ID))
	if got[b1.ID] {
		t.Errorf("Siblings(B1) should not include itself, got %v", got)
	}
	if !got[b2.ID] || !got[b3.ID] {
		t.Errorf("Siblings(B1) missing implicit siblings, got %v", got)
	}
}

// TestSiblings_Explicit checks that a manually-injected sibling edge
// (AddSiblingEdge — since the APC daemon that would write edge_type='sibling'
// bead_edges rows is not implemented yet, per docs/requirements.md R5) shows
// up in Siblings even when the two Beads share no parent.
func TestSiblings_Explicit(t *testing.T) {
	e := openT(t)

	a := seedPatient(t, e, "A")
	rx := seedChildBead(t, e, a, "fhir_medicationrequest", map[string]any{"drug": "warfarin"})
	lab := seedChildBead(t, e, a, "fhir_observation", map[string]any{"test": "eGFR"})
	// rx and lab already share parent a, so they'd be implicit siblings too;
	// use a deeper pair to isolate the explicit-edge path.
	rx2 := seedChildBead(t, e, rx, "fhir_medicationrequest", map[string]any{"drug": "nsaid"})

	bd, err := graph.LoadBundle(storeFor(e), a.ID)
	if err != nil {
		t.Fatalf("LoadBundle: %v", err)
	}

	got := collectIDs(bd.Siblings(rx2.ID))
	if got[lab.ID] {
		t.Fatal("test precondition failed: rx2 and lab should not already be siblings")
	}

	bd.AddSiblingEdge(rx2.ID, lab.ID)

	got = collectIDs(bd.Siblings(rx2.ID))
	if !got[lab.ID] {
		t.Errorf("Siblings(rx2) missing explicit sibling %s after AddSiblingEdge, got %v", lab.ID, got)
	}
	// Bidirectional per specs/MEDBEADS_SIBLING_SPEC.md §5.2.
	gotLab := collectIDs(bd.Siblings(lab.ID))
	if !gotLab[rx2.ID] {
		t.Errorf("Siblings(lab) missing explicit sibling %s (bidirectional), got %v", rx2.ID, gotLab)
	}
}

// GetPatients (v2.2.0's core/store/graph_test.go TestGetPatients) is
// migrated as internal/engine/index/read_test.go's
// TestListPatients_ReturnsOnlyRegistrations: ListPatients lives on
// *index.DB (see index/read.go), not on graph.Bundle — a Bundle is scoped to
// one already-known patient_root (LoadBundle's whole point), so patient
// *enumeration* is an index-level concern, not something graph.Bundle
// itself could answer even in principle without scanning every Pod file.

// --- ChainAcrossPatients -----------------------------------------------

// indexFor opens a second, read-only-in-practice *index.DB handle against
// e's already-migrated index.db, for calling graph.ChainAcrossPatients
// (which needs *index.DB, not *engine.Engine — see chain.go). This mirrors
// the well-known dataDir/index.db layout internal/engine/open.go uses; graph
// itself does not import engine (see doc.go), so tests reach into the data
// directory's known file layout directly rather than adding an engine
// accessor purely for test convenience.
func indexFor(t *testing.T, e *engine.Engine) *index.DB {
	t.Helper()
	db, err := index.Open(filepath.Join(e.DataDir(), "index.db"))
	if err != nil {
		t.Fatalf("index.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestChainAcrossPatients_SharedParentReachableFromBothPatients builds one
// _shared Bead (e.g. a drug_master revision, per specs/DESIGN_v3.md §3) that
// two different patients' Beads both name as a parent, and checks that
// ChainAcrossPatients from either patient's Bead reaches the shared Bead —
// something Bundle+BFS cannot do, since a Bundle only ever holds one
// patient's own Pod.
func TestChainAcrossPatients_SharedParentReachableFromBothPatients(t *testing.T) {
	e := openT(t)

	shared := ingestT(t, e, unsavedBead("drug_master", nil, []string{"rxnorm:6919"}, map[string]any{"drug": "meropenem"}))

	patientA := seedPatient(t, e, "A")
	rxA := ingestT(t, e, unsavedBead("fhir_medicationrequest", []string{patientA.ID, shared.ID}, nil, map[string]any{"n": "rxA"}))

	patientB := seedPatient(t, e, "B")
	rxB := ingestT(t, e, unsavedBead("fhir_medicationrequest", []string{patientB.ID, shared.ID}, nil, map[string]any{"n": "rxB"}))

	db := indexFor(t, e)

	chainA, err := graph.ChainAcrossPatients(db, rxA.ID, 5)
	if err != nil {
		t.Fatalf("ChainAcrossPatients(rxA): %v", err)
	}
	gotA := make(map[string]bool, len(chainA))
	for _, ref := range chainA {
		gotA[ref.ID] = true
	}
	if !gotA[shared.ID] {
		t.Errorf("ChainAcrossPatients(rxA, 5) missing shared bead %s, got %v", shared.ID, gotA)
	}
	if !gotA[patientA.ID] {
		t.Errorf("ChainAcrossPatients(rxA, 5) missing patientA %s, got %v", patientA.ID, gotA)
	}

	chainB, err := graph.ChainAcrossPatients(db, rxB.ID, 5)
	if err != nil {
		t.Fatalf("ChainAcrossPatients(rxB): %v", err)
	}
	gotB := make(map[string]bool, len(chainB))
	for _, ref := range chainB {
		gotB[ref.ID] = true
	}
	if !gotB[shared.ID] {
		t.Errorf("ChainAcrossPatients(rxB, 5) missing shared bead %s, got %v", shared.ID, gotB)
	}
}

// TestChainAcrossPatients_RespectsDepth checks that a shallow depth stops
// before reaching a distant ancestor.
func TestChainAcrossPatients_RespectsDepth(t *testing.T) {
	e := openT(t)

	shared := ingestT(t, e, unsavedBead("drug_master", nil, nil, map[string]any{"drug": "meropenem"}))
	patientA := seedPatient(t, e, "A")
	mid := ingestT(t, e, unsavedBead("fhir_medicationrequest", []string{patientA.ID, shared.ID}, nil, nil))
	leaf := ingestT(t, e, unsavedBead("fhir_observation", []string{mid.ID}, nil, nil))

	db := indexFor(t, e)

	// depth=1 from leaf reaches leaf and mid, but not shared/patientA (2 hops away).
	chain, err := graph.ChainAcrossPatients(db, leaf.ID, 1)
	if err != nil {
		t.Fatalf("ChainAcrossPatients: %v", err)
	}
	got := make(map[string]bool, len(chain))
	for _, ref := range chain {
		got[ref.ID] = true
	}
	if !got[leaf.ID] || !got[mid.ID] {
		t.Errorf("ChainAcrossPatients(leaf, 1) should include leaf and mid, got %v", got)
	}
	if got[shared.ID] {
		t.Errorf("ChainAcrossPatients(leaf, 1) should not reach shared (2 hops away), got %v", got)
	}
}
