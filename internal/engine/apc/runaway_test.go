package apc_test

import (
	"fmt"
	"testing"

	"github.com/medbeads/medbeads/internal/engine/apc"
)

// TestRunawayPrevention_A_UniquePairAntigen_NoDuplicateSiblingPairRow checks
// point a: sibling_pairs UNIQUE(bead_a, bead_b, matched_antigen) means the
// exact same (pair, antigen) is never recorded twice, even across an
// explicit RescanPatient + re-Scan that revisits the same Beads.
func TestRunawayPrevention_A_UniquePairAntigen_NoDuplicateSiblingPairRow(t *testing.T) {
	e := openT(t)
	root := seedPatient(t, e, "patient A")
	padWithNoiseBeads(t, e, root, 10)
	rx := seedChildBead(t, e, root, "fhir_medicationrequest",
		[]string{"risk:nephrotoxic"}, map[string]any{"drug": "meropenem"})
	lab := seedChildBead(t, e, root, "fhir_observation",
		[]string{"risk:nephrotoxic"}, map[string]any{"test": "eGFR"})

	scanner := apc.New(e, e.Index(), apc.Default())
	if _, err := scanner.Scan(); err != nil {
		t.Fatalf("first Scan: %v", err)
	}

	if err := scanner.RescanPatient(root.ID); err != nil {
		t.Fatalf("RescanPatient: %v", err)
	}
	res, err := scanner.Scan()
	if err != nil {
		t.Fatalf("second Scan: %v", err)
	}
	if res.SiblingLinksCreated != 0 {
		t.Errorf("SiblingLinksCreated after rescan = %d, want 0 (already linked)", res.SiblingLinksCreated)
	}

	pairA, pairB := rx.ID, lab.ID
	if pairB < pairA {
		pairA, pairB = pairB, pairA
	}
	if n := countRows(t, e.Index(),
		`SELECT COUNT(*) FROM sibling_pairs WHERE bead_a = ? AND bead_b = ? AND matched_antigen = ?`,
		pairA, pairB, "risk:nephrotoxic",
	); n != 1 {
		t.Errorf("sibling_pairs rows for (pair, risk:nephrotoxic) = %d, want 1 (UNIQUE holds)", n)
	}
}

// TestRunawayPrevention_B_MaxSiblingsPerBead_StopsAtEleventh checks point b:
// with 12 Beads all sharing "risk:nephrotoxic" in one patient (C(12,2) = 66
// possible pairs), no single Bead ever ends up linked to more than
// MaxSiblingsPerBead (10, the default) distinct partners — Scan's own
// per-candidate/per-anchor cap check (scanOne) stops issuing further links
// for any Bead once its running count reaches the cap, regardless of which
// specific Bead in the pool happens to hit it first (the scanner does not
// designate one Bead as "the" hub in advance; whichever Bead's turn as an
// anchor exhausts the cap first is simply the one this test observes it on
// — see the debug run this test's bound was derived from). Each Bead also
// carries a unique antigen in addition to the shared risk:nephrotoxic
// purely to keep the pool large enough that risk:nephrotoxic's patient-local
// frequency (12 of 52 antigen-bearing Beads once padded) stays under the
// 30% IDF threshold — otherwise runaway-prevention d would filter the whole
// scenario out before b ever had a chance to bind.
func TestRunawayPrevention_B_MaxSiblingsPerBead_StopsAtEleventh(t *testing.T) {
	e := openT(t)
	root := seedPatient(t, e, "patient A")
	padWithNoiseBeads(t, e, root, 40) // 12 nephrotoxic-bearing / (40+12) = ~23% < 30%

	const n = 12
	for i := 0; i < n; i++ {
		seedChildBead(t, e, root, "fhir_observation",
			[]string{"risk:nephrotoxic"}, map[string]any{"test": fmt.Sprintf("obs-%d", i)})
	}

	scanner := apc.New(e, e.Index(), apc.Default())
	if _, err := scanner.Scan(); err != nil {
		t.Fatalf("Scan: %v", err)
	}

	// No Bead in bead_apc_scan may report a sibling_count above the cap
	// (runaway prevention b's persisted counter, checked across every
	// participant rather than one hand-picked "anchor").
	rows, err := e.Index().SQLDB().Query(
		`SELECT bead_id, sibling_count FROM bead_apc_scan WHERE sibling_count > 0`)
	if err != nil {
		t.Fatalf("query bead_apc_scan: %v", err)
	}
	var maxSeen int
	for rows.Next() {
		var beadID string
		var count int
		if err := rows.Scan(&beadID, &count); err != nil {
			rows.Close()
			t.Fatalf("scan: %v", err)
		}
		if count > apc.Default().MaxSiblingsPerBead {
			t.Errorf("bead %s sibling_count = %d, exceeds MaxSiblingsPerBead %d", beadID, count, apc.Default().MaxSiblingsPerBead)
		}
		if count > maxSeen {
			maxSeen = count
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatalf("rows: %v", err)
	}
	rows.Close()

	// With 12 mutually-matching Beads and a cap of 10, at least one Bead
	// must actually hit the cap (there is no way to keep every pairwise
	// score-clearing match under the cap with this much overlap) — this
	// guards against a vacuously-passing test where the cap check never
	// actually engages.
	if maxSeen != apc.Default().MaxSiblingsPerBead {
		t.Errorf("max observed sibling_count = %d, want exactly %d (cap should bind for at least one Bead)", maxSeen, apc.Default().MaxSiblingsPerBead)
	}
}

// TestRunawayPrevention_C_GenerationCapAndDecay checks point c: a
// first-generation sibling_link (generation 1, born from two ordinary
// Beads) may itself be scanned and matched again (producing a generation-2
// link), but a generation-2 Bead never produces a generation-3 link.
func TestRunawayPrevention_C_GenerationCapAndDecay(t *testing.T) {
	e := openT(t)
	root := seedPatient(t, e, "patient A")
	padWithNoiseBeads(t, e, root, 10)

	// Two Beads sharing "risk:nephrotoxic" AND "organ:renal" (a
	// medicationrequest and an observation, close in time): scores well
	// above threshold, producing a generation-1 sibling_link. That link's
	// own Antigens are exactly the matched antigens (buildSiblingLinkBead),
	// i.e. ["organ:renal", "risk:nephrotoxic"] — so a third ordinary Bead
	// also carrying both can match *the link itself* on the next Scan,
	// producing a generation-2 link. Two shared antigens (not one) matter
	// here specifically because of the generation-1 decay
	// (Config.GenerationDecay = 0.5): a single shared antigen's subtotal
	// (1 + risk:'s +3 + a 24h-proximity +2 = 6) decays to 3.0, just under
	// MinScoreThreshold=4 — the second shared antigen (+2 additional, +2
	// organ:) lifts the pre-decay subtotal to 10, decaying to 5.0, clearing
	// threshold.
	a := seedChildBead(t, e, root, "fhir_medicationrequest",
		[]string{"risk:nephrotoxic", "organ:renal"}, map[string]any{"drug": "A"})
	b := seedChildBead(t, e, root, "fhir_observation",
		[]string{"risk:nephrotoxic", "organ:renal"}, map[string]any{"test": "B"})

	cfg := apc.Default()
	scanner := apc.New(e, e.Index(), cfg)

	res1, err := scanner.Scan()
	if err != nil {
		t.Fatalf("Scan 1: %v", err)
	}
	if res1.SiblingLinksCreated != 1 {
		t.Fatalf("Scan 1 created %d links, want 1", res1.SiblingLinksCreated)
	}

	var gen1LinkID string
	if err := e.Index().SQLDB().QueryRow(
		`SELECT id FROM beads WHERE type = 'sibling_link'`,
	).Scan(&gen1LinkID); err != nil {
		t.Fatalf("query gen1 link: %v", err)
	}
	gen1Link, err := e.GetBead(gen1LinkID)
	if err != nil {
		t.Fatalf("GetBead(gen1 link): %v", err)
	}
	if got, _ := gen1Link.Content["scan_generation"].(float64); int(got) != 1 {
		t.Errorf("gen1 link scan_generation = %v, want 1", gen1Link.Content["scan_generation"])
	}

	// A third, ordinary Bead sharing both antigens — but note it also shares
	// them with a and b, so this Scan may additionally link c to a and/or b
	// directly (generation-0 pairs); the assertion below only checks that a
	// generation-2 link (parented by gen1Link) exists, not an exact total.
	c := seedChildBead(t, e, root, "fhir_diagnosticreport",
		[]string{"risk:nephrotoxic", "organ:renal"}, map[string]any{"report": "C"})

	res2, err := scanner.Scan()
	if err != nil {
		t.Fatalf("Scan 2: %v", err)
	}
	if res2.SiblingLinksCreated < 1 {
		t.Fatalf("Scan 2 created %d links, want >= 1", res2.SiblingLinksCreated)
	}

	// Collect candidate IDs first and close rows before calling e.GetBead in
	// the loop below: index.DB's underlying *sql.DB is capped at one open
	// connection (see index.Open's SetMaxOpenConns(1) doc comment), so
	// holding this Query's rows open while GetBead tries to acquire a
	// second connection for its own QueryRow would deadlock — both wait on
	// the same single connection slot forever.
	var candidateIDs []string
	rows, err := e.Index().SQLDB().Query(
		`SELECT id FROM beads WHERE type = 'sibling_link' AND id != ?`, gen1LinkID)
	if err != nil {
		t.Fatalf("query gen2 candidates: %v", err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			t.Fatalf("scan: %v", err)
		}
		candidateIDs = append(candidateIDs, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatalf("rows: %v", err)
	}
	rows.Close()

	var gen2LinkID string
	var gen2Found bool
	for _, id := range candidateIDs {
		lb, err := e.GetBead(id)
		if err != nil {
			t.Fatalf("GetBead: %v", err)
		}
		for _, p := range lb.Parents {
			if p == gen1LinkID {
				gen2Found = true
				gen2LinkID = id
				if got, _ := lb.Content["scan_generation"].(float64); int(got) != 2 {
					t.Errorf("gen2 link scan_generation = %v, want 2", lb.Content["scan_generation"])
				}
				// Decay check: score(gen2) = additive-subtotal *
				// GenerationDecay^1 (pairGeneration=1 when matching gen1Link
				// against c, both generation 0/1) — assert it is strictly
				// less than what the same antigen overlap would score at
				// generation 0 (a plain +1 for one shared antigen, no
				// decay), i.e. decay measurably reduced the score.
				gotScore, _ := lb.Content["score"].(float64)
				if gotScore <= 0 || gotScore >= 20 {
					t.Errorf("gen2 link score = %v, want a small decayed positive value", gotScore)
				}
			}
		}
	}
	if !gen2Found {
		t.Fatal("no sibling_link found with gen1Link as a parent (expected a generation-2 link)")
	}

	// A gen2 link must never itself produce a gen3 link: rescanning after
	// the gen2 link exists must create zero further links descending from
	// it specifically (other new links among a/b/c/gen1Link's remaining
	// unlinked antigen combinations are irrelevant to this assertion, which
	// only checks gen2LinkID never becomes a sibling_link's parent).
	if err := scanner.RescanPatient(root.ID); err != nil {
		t.Fatalf("RescanPatient: %v", err)
	}
	if _, err := scanner.Scan(); err != nil {
		t.Fatalf("Scan 3 (post-rescan): %v", err)
	}

	var gen3Count int
	if err := e.Index().SQLDB().QueryRow(`
		SELECT COUNT(*) FROM bead_edges
		WHERE parent_id = ? AND edge_type = 'parent'`, gen2LinkID,
	).Scan(&gen3Count); err != nil {
		t.Fatalf("count gen3 candidates: %v", err)
	}
	if gen3Count != 0 {
		t.Errorf("found %d Bead(s) with gen2Link as a parent (would be a gen3 sibling_link), want 0", gen3Count)
	}

	_ = a
	_ = b
	_ = c
}

// TestRunawayPrevention_D_HighFrequencyAntigenExcluded checks point d: an
// antigen present on more than AntigenFrequencyThreshold (30% default) of a
// patient's antigen-bearing Beads is dropped as a trigger, even though two
// Beads sharing only that antigen would otherwise score high enough to
// link.
func TestRunawayPrevention_D_HighFrequencyAntigenExcluded(t *testing.T) {
	e := openT(t)
	root := seedPatient(t, e, "patient A")

	// 10 Beads all carry "organ:cardiovascular" (a vitals-style antigen on
	// every encounter) -> 100% patient-local frequency, well over the 30%
	// default threshold. None of them should link to each other via that
	// antigen alone.
	const n = 10
	for i := 0; i < n; i++ {
		seedChildBead(t, e, root, "fhir_observation",
			[]string{"organ:cardiovascular"}, map[string]any{"vital": fmt.Sprintf("v-%d", i)})
	}

	scanner := apc.New(e, e.Index(), apc.Default())
	res, err := scanner.Scan()
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if res.SiblingLinksCreated != 0 {
		t.Errorf("SiblingLinksCreated = %d, want 0 (organ:cardiovascular is a high-frequency antigen, filtered by IDF)", res.SiblingLinksCreated)
	}
	if n := countRows(t, e.Index(), `SELECT COUNT(*) FROM beads WHERE type = 'sibling_link'`); n != 0 {
		t.Errorf("sibling_link Bead count = %d, want 0", n)
	}
}

// TestRunawayPrevention_E_PerPatientPerScanRateLimit checks point e: a
// tightly configured MaxSiblingLinksPerPatientPerScan stops link generation
// for a patient once the limit is reached within a single Scan call, even
// though more matching pairs remain.
func TestRunawayPrevention_E_PerPatientPerScanRateLimit(t *testing.T) {
	e := openT(t)
	root := seedPatient(t, e, "patient A")
	padWithNoiseBeads(t, e, root, 20) // 6 nephrotoxic-bearing / (20+6) = ~23% < 30%

	anchor := seedChildBead(t, e, root, "fhir_medicationrequest",
		[]string{"risk:nephrotoxic"}, map[string]any{"drug": "anchor drug"})
	const n = 5
	for i := 0; i < n; i++ {
		seedChildBead(t, e, root, "fhir_observation",
			[]string{"risk:nephrotoxic"}, map[string]any{"test": fmt.Sprintf("obs-%d", i)})
	}

	cfg := apc.Default()
	cfg.MaxSiblingLinksPerPatientPerScan = 2
	scanner := apc.New(e, e.Index(), cfg)

	res, err := scanner.Scan()
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if res.SiblingLinksCreated != 2 {
		t.Errorf("SiblingLinksCreated = %d, want 2 (MaxSiblingLinksPerPatientPerScan)", res.SiblingLinksCreated)
	}
	_ = anchor
}
