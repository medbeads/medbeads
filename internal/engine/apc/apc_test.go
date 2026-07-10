package apc_test

import (
	"fmt"
	"testing"

	"github.com/medbeads/medbeads/internal/engine"
	"github.com/medbeads/medbeads/internal/engine/apc"
	"github.com/medbeads/medbeads/internal/engine/bead"
	"github.com/medbeads/medbeads/internal/engine/graph"
	"github.com/medbeads/medbeads/internal/engine/index"
	"github.com/medbeads/medbeads/internal/engine/pod"
)

// --- shared test scaffolding (mirrors internal/engine/graph/graph_test.go's
// conventions: openT/seedPatient/seedChildBead/ingestT/unsavedBead, each
// duplicated here per this project's established "apc/graph do not import
// engine internals, tests in _test packages re-derive small helpers"
// pattern) ---------------------------------------------------------------

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

var timestampCounter int

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

// unsavedBead returns an ID-less Bead for the given type/parents/content. No
// antigens parameter: v3.1 removed Bead.Antigens entirely (see bead.Bead's
// doc comment) — tag derivation now happens only at index-projection time,
// from antigen.Extract(b.Type, b.Content), which is a fixed deterministic
// function (real FHIR coding + a small static dictionary, no override hook,
// no LLM). This package's own tests exercise Scanner behavior *given*
// certain bead_antigens tags exist, not tag derivation itself (that is
// package antigen's job, covered by its own fixture-based tests) — many of
// the tag strings these tests need (loinc:noise-N uniqueness markers,
// risk:nephrotoxic alone without organ:renal, etc.) do not correspond to any
// real FHIR coding or dictionary.json entry antigen.Extract could ever
// produce. See seedAntigens below for how a caller gets specific
// bead_antigens rows onto a seeded Bead instead.
func unsavedBead(typ string, parents []string, content map[string]any) bead.Bead {
	if content == nil {
		content = map[string]any{}
	}
	return bead.Bead{
		Type:      typ,
		Timestamp: nextTimestamp(),
		Author:    "did:medbeads:doctor:12345",
		Parents:   parents,
		Content:   content,
	}
}

func ingestT(t *testing.T, e *engine.Engine, b bead.Bead) bead.Bead {
	t.Helper()
	out, err := e.Ingest(b)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	return out
}

// seedAntigens inserts bead_antigens rows for the already-ingested Bead b
// directly (bypassing antigen.Extract entirely), for tests whose subject is
// Scanner behavior given a Bead carries certain tags, not tag derivation
// itself — see unsavedBead's doc comment. This mirrors exactly the row
// shape/table IndexBead itself would have written had Extract produced
// these tags (INSERT OR IGNORE INTO bead_antigens(antigen, bead_id,
// patient_root)), so every downstream Scanner code path (GetAntigens,
// frequentAntigens, candidateRows) sees the identical shape of data it would
// from a real projection run.
//
// patient_root is resolved from the index (e.Index().GetBead(b.ID)) rather
// than trusted from a caller-supplied parameter: candidatesFor/
// frequentAntigens scope every query by patient_root, so a wrong value here
// would silently scope a seeded tag to the wrong patient (or the shared
// pod) and break IDF-threshold/matching assertions in a way that has
// nothing to do with what the test is actually checking. b's own parent
// bead is not necessarily the patient root (e.g. a Bead seeded under an
// intermediate encounter), so re-deriving it from what Ingest itself
// already resolved is the only reliable source.
func seedAntigens(t *testing.T, e *engine.Engine, b bead.Bead, tags ...string) {
	t.Helper()
	ref, err := e.Index().GetBead(b.ID)
	if err != nil {
		t.Fatalf("seedAntigens(%s): resolve patient_root: %v", b.ID, err)
	}
	var root any
	if ref.PatientRoot != "" {
		root = ref.PatientRoot
	}
	for _, tag := range tags {
		if _, err := e.Index().SQLDB().Exec(
			`INSERT OR IGNORE INTO bead_antigens (antigen, bead_id, patient_root) VALUES (?, ?, ?)`,
			tag, b.ID, root,
		); err != nil {
			t.Fatalf("seedAntigens(%s, %v): %v", b.ID, tags, err)
		}
	}
}

func seedPatient(t *testing.T, e *engine.Engine, name string) bead.Bead {
	t.Helper()
	return ingestT(t, e, unsavedBead("patient_registration", nil, map[string]any{"name": name}))
}

// seedChildBead ingests a Bead of the given type/content as a child of
// parent, then (if antigens is non-empty) injects bead_antigens rows for it
// directly via seedAntigens — see unsavedBead's doc comment for why this
// package's tests control tags this way rather than through a Bead field.
func seedChildBead(t *testing.T, e *engine.Engine, parent bead.Bead, typ string, antigens []string, content map[string]any) bead.Bead {
	t.Helper()
	b := ingestT(t, e, unsavedBead(typ, []string{parent.ID}, content))
	if len(antigens) > 0 {
		seedAntigens(t, e, b, antigens...)
	}
	return b
}

func storeFor(e *engine.Engine) *pod.Store {
	return pod.NewStore(e.DataDir())
}

// settleT repeatedly calls scanner.Scan() until a call examines zero new
// Beads, failing the test if it does not converge within a small bounded
// number of calls. This is needed because a matched pair's own
// generation-1 sibling_link Bead is itself a brand-new, not-yet-scanned
// Bead the moment it is ingested (SIBLING_SPEC §4.5's "二次応答" chain): the
// call that creates it does not also scan it (scanOne only marks its own
// batch's anchors scanned, and the sibling_link Bead was ingested mid-batch,
// not enumerated by that batch's own unscannedBeads() call), so it — and
// any generation-2 link it then produces — needs its own subsequent Scan()
// call to settle. MaxGeneration bounds how many such calls can ever be
// needed (each call can only deepen the chain by one generation, and
// Config.MaxGeneration caps how deep the chain goes before scanOne refuses
// to produce anything further), so this loop is guaranteed to terminate for
// any Config a real caller would use.
func settleT(t *testing.T, scanner *apc.Scanner) apc.Result {
	t.Helper()
	var last apc.Result
	for i := 0; i < 10; i++ {
		res, err := scanner.Scan()
		if err != nil {
			t.Fatalf("settleT: Scan: %v", err)
		}
		last = res
		if res.BeadsScanned == 0 {
			return last
		}
	}
	t.Fatalf("settleT: did not converge (BeadsScanned stayed > 0) after 10 Scan calls")
	return last
}

// padWithNoiseBeads ingests n Beads under parent, each carrying a distinct,
// unique antigen no other Bead in the patient shares, so that any
// genuinely-shared antigen elsewhere in the same patient stays comfortably
// under Config.AntigenFrequencyThreshold's default 30% patient-local
// frequency (runaway prevention d — see candidatesFor/frequentAntigens).
// Every test in this package that wants a shared-antigen match to actually
// clear the IDF filter and reach scoring needs this: a bare 2-Bead patient
// makes any antigen those two Beads share automatically 100% frequent,
// which is realistic IDF behavior, not a bug (see this package's own git
// history / task notes) — tests must set up patient-scale data accordingly
// rather than relying on toy 2-Bead patients.
func padWithNoiseBeads(t *testing.T, e *engine.Engine, parent bead.Bead, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		seedChildBead(t, e, parent, "fhir_observation",
			[]string{fmt.Sprintf("loinc:noise-%d", i)},
			map[string]any{"noise": i})
	}
}

func countRows(t *testing.T, db *index.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.SQLDB().QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("countRows(%q): %v", query, err)
	}
	return n
}

// --- integration: shared antigen -> sibling_link Bead + sibling edge -----

// TestScan_MatchedPair_CreatesSiblingLinkAndEdge checks the headline
// behavior end to end: two Beads sharing enough weight of antigen overlap
// to clear the score threshold produce a sibling_link Bead (ingested via
// engine.Ingest — itself hash-verifiable), a bead_edges 'sibling' row in
// both directions, and become visible to graph.Bundle.Siblings once that
// edge is injected into a loaded Bundle.
func TestScan_MatchedPair_CreatesSiblingLinkAndEdge(t *testing.T) {
	e := openT(t)
	root := seedPatient(t, e, "patient A")
	padWithNoiseBeads(t, e, root, 10) // keep risk:nephrotoxic/organ:renal under the 30% IDF threshold

	// Two shared antigens (one risk:, one organ:) within 24h of each other,
	// one a medicationrequest and the other an observation: this should
	// clear MinScoreThreshold=4 by a wide margin (1 + 3(risk) + 2(organ) +
	// 2(24h) + 3(rx/lab) = 11), matching SIBLING_SPEC §6.4's own worked
	// example shape (nephrotoxic prescription <-> renal lab result).
	rx := seedChildBead(t, e, root, "fhir_medicationrequest",
		[]string{"risk:nephrotoxic", "organ:renal"},
		map[string]any{"drug": "meropenem"})
	lab := seedChildBead(t, e, root, "fhir_observation",
		[]string{"risk:nephrotoxic", "organ:renal"},
		map[string]any{"test": "eGFR"})

	scanner := apc.New(e, e.Index(), apc.Default())
	res, err := scanner.Scan()
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if res.BeadsScanned != 13 { // root + 10 noise + rx + lab
		t.Errorf("BeadsScanned = %d, want 13", res.BeadsScanned)
	}
	if res.SiblingLinksCreated != 1 {
		t.Fatalf("SiblingLinksCreated = %d, want 1", res.SiblingLinksCreated)
	}

	// The sibling_link Bead itself must be a real, hash-verifiable Bead
	// reachable via engine.GetBead.
	var linkID string
	if err := e.Index().SQLDB().QueryRow(
		`SELECT id FROM beads WHERE type = 'sibling_link'`,
	).Scan(&linkID); err != nil {
		t.Fatalf("query sibling_link id: %v", err)
	}
	link, err := e.GetBead(linkID)
	if err != nil {
		t.Fatalf("GetBead(sibling_link): %v", err)
	}
	if err := bead.Verify(link); err != nil {
		t.Errorf("sibling_link Bead fails Verify: %v", err)
	}
	if got := len(link.Parents); got != 2 {
		t.Fatalf("sibling_link Parents = %d, want 2", got)
	}
	wantParents := map[string]bool{rx.ID: true, lab.ID: true}
	for _, p := range link.Parents {
		if !wantParents[p] {
			t.Errorf("sibling_link has unexpected parent %s", p)
		}
	}

	// bead_edges must carry the bidirectional 'sibling' edge (SPEC §5.3).
	if n := countRows(t, e.Index(),
		`SELECT COUNT(*) FROM bead_edges WHERE child_id = ? AND parent_id = ? AND edge_type = 'sibling'`,
		rx.ID, lab.ID); n != 1 {
		t.Errorf("sibling edge rx->lab count = %d, want 1", n)
	}
	if n := countRows(t, e.Index(),
		`SELECT COUNT(*) FROM bead_edges WHERE child_id = ? AND parent_id = ? AND edge_type = 'sibling'`,
		lab.ID, rx.ID); n != 1 {
		t.Errorf("sibling edge lab->rx count = %d, want 1", n)
	}

	// graph.LoadBundle + the sibling edge must make the pair mutually
	// visible via Siblings, once the loaded Bundle is told about the edge
	// (LoadBundle itself only reads the Pod — see Bundle.AddSiblingEdge's
	// doc comment; this is the "index-aware wiring" apc's own doc comment
	// defers to a later unit, so the test wires it by hand here).
	bd, err := graph.LoadBundle(storeFor(e), root.ID)
	if err != nil {
		t.Fatalf("LoadBundle: %v", err)
	}
	bd.AddSiblingEdge(rx.ID, lab.ID)
	siblingIDs := map[string]bool{}
	for _, s := range bd.Siblings(rx.ID) {
		siblingIDs[s.ID] = true
	}
	if !siblingIDs[lab.ID] {
		t.Errorf("Siblings(rx) = %v missing lab %s after AddSiblingEdge", siblingIDs, lab.ID)
	}
}

// --- idempotency + incremental scope --------------------------------------

// TestScan_SecondCallWithNoNewBeads_CreatesNothing checks idempotency: once
// a Scan call has settled every reachable anchor — including the
// generation-1 sibling_link Bead it itself created, which becomes a new
// anchor on the *next* call and legitimately produces generation-2 links
// against its own parents (SIBLING_SPEC §4.5 "二次応答" — this is expected
// behavior, not a bug) — a further Scan call with nothing new to discover
// creates no further sibling_link Beads at all.
func TestScan_SecondCallWithNoNewBeads_CreatesNothing(t *testing.T) {
	e := openT(t)
	root := seedPatient(t, e, "patient A")
	padWithNoiseBeads(t, e, root, 10)
	seedChildBead(t, e, root, "fhir_medicationrequest",
		[]string{"risk:nephrotoxic", "organ:renal"}, map[string]any{"drug": "meropenem"})
	seedChildBead(t, e, root, "fhir_observation",
		[]string{"risk:nephrotoxic", "organ:renal"}, map[string]any{"test": "eGFR"})

	scanner := apc.New(e, e.Index(), apc.Default())
	first, err := scanner.Scan()
	if err != nil {
		t.Fatalf("first Scan: %v", err)
	}
	if first.SiblingLinksCreated != 1 {
		t.Fatalf("first Scan created %d links, want 1", first.SiblingLinksCreated)
	}

	// Settle every chained generation the first Scan's own sibling_link
	// (and any further links it in turn produces, up to MaxGeneration) still
	// triggers — see settleT's doc comment.
	settleT(t, scanner)

	// Now nothing new remains to discover: one further Scan call must be a
	// true no-op.
	final, err := scanner.Scan()
	if err != nil {
		t.Fatalf("final Scan: %v", err)
	}
	if final.BeadsScanned != 0 {
		t.Errorf("final Scan BeadsScanned = %d, want 0", final.BeadsScanned)
	}
	if final.SiblingLinksCreated != 0 {
		t.Errorf("final Scan SiblingLinksCreated = %d, want 0", final.SiblingLinksCreated)
	}
}

// TestScan_Incremental_DoesNotRematchAlreadyScannedPair checks the
// watermark's incremental scope: after Scan has already matched (rx, lab)
// and settled the resulting generation-1 sibling_link Bead (which itself
// briefly becomes a new anchor on the very next call — see
// TestScan_SecondCallWithNoNewBeads_CreatesNothing), ingesting one further
// unrelated Bead and scanning again must not re-visit the (rx, lab) pair
// (it should not appear a second time in sibling_pairs, and no second
// sibling_link between exactly rx and lab should be created).
func TestScan_Incremental_DoesNotRematchAlreadyScannedPair(t *testing.T) {
	e := openT(t)
	root := seedPatient(t, e, "patient A")
	padWithNoiseBeads(t, e, root, 10)
	rx := seedChildBead(t, e, root, "fhir_medicationrequest",
		[]string{"risk:nephrotoxic", "organ:renal"}, map[string]any{"drug": "meropenem"})
	lab := seedChildBead(t, e, root, "fhir_observation",
		[]string{"risk:nephrotoxic", "organ:renal"}, map[string]any{"test": "eGFR"})

	scanner := apc.New(e, e.Index(), apc.Default())
	if _, err := scanner.Scan(); err != nil {
		t.Fatalf("first Scan: %v", err)
	}
	// Settle every chained generation the first Scan's own sibling_link
	// still triggers (see settleT's doc comment / TestScan_SecondCallWithNoNewBeads_CreatesNothing).
	settleT(t, scanner)

	// A new, unrelated Bead with no shared antigen: it should be scanned
	// (BeadsScanned counts it) but should not disturb the already-settled
	// rx/lab pair.
	seedChildBead(t, e, root, "fhir_encounter", nil, map[string]any{"n": "unrelated"})

	res, err := scanner.Scan()
	if err != nil {
		t.Fatalf("final Scan: %v", err)
	}
	if res.BeadsScanned != 1 {
		t.Errorf("final Scan BeadsScanned = %d, want 1 (only the new encounter)", res.BeadsScanned)
	}
	if res.SiblingLinksCreated != 0 {
		t.Errorf("final Scan SiblingLinksCreated = %d, want 0", res.SiblingLinksCreated)
	}

	pairA, pairB := rx.ID, lab.ID
	if pairB < pairA {
		pairA, pairB = pairB, pairA
	}
	if n := countRows(t, e.Index(),
		`SELECT COUNT(*) FROM sibling_pairs WHERE bead_a = ? AND bead_b = ?`, pairA, pairB,
	); n != 2 { // two matched_antigen rows (risk:nephrotoxic, organ:renal), not duplicated
		t.Errorf("sibling_pairs rows for rx/lab = %d, want 2 (unchanged by second Scan)", n)
	}
}

// --- sibling_link Bead ID determinism -------------------------------------

// TestBuildSiblingLink_DeterministicID checks that scanning the same
// pre-existing state from scratch twice (two separate Engines seeded with
// byte-identical Bead histories) produces the byte-identical sibling_link
// Bead ID — the "same pair, same score -> same ID" property the task calls
// for, which requires Timestamp to be derived from the input Beads rather
// than wall-clock time (see link.go's buildSiblingLinkBead doc comment).
func TestBuildSiblingLink_DeterministicID(t *testing.T) {
	build := func(t *testing.T) string {
		e := openT(t)
		// A fixed-timestamp patient_registration (not seedPatient's
		// nextTimestamp-based one): nextTimestamp's counter is shared
		// process-wide across every test in this package, so seedPatient
		// would give the two build() calls' root Bead two different
		// Timestamps, two different root.IDs, and therefore two different
		// rx/lab.IDs (their Parents=[root.ID]) — which would make the
		// resulting sibling_link's own Parents=[rx.ID, lab.ID] differ between
		// runs for a reason unrelated to what this test checks. Every Bead
		// below uses a fixed literal Timestamp for the same reason.
		root := ingestT(t, e, bead.Bead{
			Type: "patient_registration", Timestamp: "2026-01-01T00:00:00Z",
			Author: "did:medbeads:doctor:12345", Content: map[string]any{"name": "patient A"},
		})
		for i := 0; i < 10; i++ {
			noise := ingestT(t, e, bead.Bead{
				Type: "fhir_observation", Timestamp: fmt.Sprintf("2026-01-15T%02d:00:00Z", i),
				Author: "did:medbeads:doctor:12345", Parents: []string{root.ID},
				Content: map[string]any{"noise": i},
			})
			seedAntigens(t, e, noise, fmt.Sprintf("loinc:noise-%d", i))
		}
		rx := ingestT(t, e, bead.Bead{
			Type: "fhir_medicationrequest", Timestamp: "2026-02-01T09:00:00Z",
			Author: "did:medbeads:doctor:12345", Parents: []string{root.ID},
			Content: map[string]any{"drug": "meropenem"},
		})
		seedAntigens(t, e, rx, "risk:nephrotoxic", "organ:renal")
		lab := ingestT(t, e, bead.Bead{
			Type: "fhir_observation", Timestamp: "2026-02-01T10:00:00Z",
			Author: "did:medbeads:doctor:12345", Parents: []string{root.ID},
			Content: map[string]any{"test": "eGFR"},
		})
		seedAntigens(t, e, lab, "risk:nephrotoxic", "organ:renal")

		scanner := apc.New(e, e.Index(), apc.Default())
		if _, err := scanner.Scan(); err != nil {
			t.Fatalf("Scan: %v", err)
		}
		var linkID string
		if err := e.Index().SQLDB().QueryRow(
			`SELECT id FROM beads WHERE type = 'sibling_link'`,
		).Scan(&linkID); err != nil {
			t.Fatalf("query sibling_link id: %v", err)
		}
		return linkID
	}

	id1 := build(t)
	id2 := build(t)
	if id1 != id2 {
		t.Errorf("sibling_link ID not deterministic: run1=%s run2=%s", id1, id2)
	}
}
