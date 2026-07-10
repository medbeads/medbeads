package apc_test

import (
	"sort"
	"testing"

	"github.com/medbeads/medbeads/internal/engine"
	"github.com/medbeads/medbeads/internal/engine/apc"
	"github.com/medbeads/medbeads/internal/engine/bead"
)

// TestScan_SiblingLinkIDSet_StableAcrossRepeatedBuilds is the fixed-point
// regression guard the lead's performance-fix task requires ("暴走防止5点・
// 冪等性・生成される sibling_link 集合の不変が絶対条件... 最適化前後で小規模
// データの最終 link ID 集合一致をテストで固定"): the query-plan/index
// (migrations/0005_bead_antigens_patient_idx.sql) and per-batch
// frequentAntigens caching (Scanner.frequentCache) changes in this same
// commit are pure performance optimizations — same rows read, same
// candidates considered, same scores computed, same runaway-prevention
// decisions — and must not change *which* sibling_link Beads a full Scan to
// fixed point produces, nor their IDs (content-derived, so any behavioral
// drift in matching order/generation/timestamp handling would show up here
// as a different ID set even before any visible test failure elsewhere).
//
// This builds two independent Engines from byte-identical Bead histories
// (fixed literal timestamps throughout — see TestBuildSiblingLink_
// DeterministicID's identical reasoning for why seedPatient's counter-based
// timestamps are avoided here) across multiple patients and multiple
// distinct match opportunities (not just one pair), settles each to a fixed
// point, and asserts the two runs' final sibling_link ID sets are exactly
// equal — set membership AND count, so neither a spurious extra link nor a
// missing one would go unnoticed.
func TestScan_SiblingLinkIDSet_StableAcrossRepeatedBuilds(t *testing.T) {
	build := func(t *testing.T) []string {
		e, err := engine.Open(t.TempDir())
		if err != nil {
			t.Fatalf("engine.Open: %v", err)
		}
		t.Cleanup(func() { _ = e.Close() })

		buildPatient := func(name string, baseHour int) {
			root := ingestFixedT(t, e, "patient_registration", nil, nil,
				map[string]any{"name": name}, fixedTimestamp(baseHour, 0))

			// Padding so every shared antigen below stays under the 30% IDF
			// threshold (see padWithNoiseBeads' doc comment for why this
			// matters at small scale).
			for i := 0; i < 10; i++ {
				ingestFixedT(t, e, "fhir_observation", []string{root.ID},
					[]string{fixedAntigen(name, i)}, map[string]any{"noise": i},
					fixedTimestamp(baseHour, i+1))
			}

			// Two independent match opportunities per patient (distinct
			// antigen pairs), so this test exercises more than one
			// candidate/scoring path per patient, not just a single pair.
			rx1 := ingestFixedT(t, e, "fhir_medicationrequest", []string{root.ID},
				[]string{"risk:nephrotoxic", "organ:renal"}, map[string]any{"drug": "meropenem"},
				fixedTimestamp(baseHour, 20))
			lab1 := ingestFixedT(t, e, "fhir_observation", []string{root.ID},
				[]string{"risk:nephrotoxic", "organ:renal"}, map[string]any{"test": "eGFR"},
				fixedTimestamp(baseHour, 21))
			_ = rx1
			_ = lab1

			rx2 := ingestFixedT(t, e, "fhir_medicationrequest", []string{root.ID},
				[]string{"risk:bleeding", "organ:hematologic"}, map[string]any{"drug": "warfarin"},
				fixedTimestamp(baseHour, 30))
			labOrObs2 := ingestFixedT(t, e, "fhir_diagnosticreport", []string{root.ID},
				[]string{"risk:bleeding", "organ:hematologic"}, map[string]any{"test": "INR"},
				fixedTimestamp(baseHour, 31))
			_ = rx2
			_ = labOrObs2
		}

		buildPatient("patient A", 0)
		buildPatient("patient B", 1)
		buildPatient("patient C", 2)

		scanner := apc.New(e, e.Index(), apc.Default())
		settleT(t, scanner)

		rows, err := e.Index().SQLDB().Query(`SELECT id FROM beads WHERE type = 'sibling_link' ORDER BY id`)
		if err != nil {
			t.Fatalf("query sibling_link ids: %v", err)
		}
		defer rows.Close()
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				t.Fatalf("scan: %v", err)
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("rows: %v", err)
		}
		sort.Strings(ids)
		return ids
	}

	idsRun1 := build(t)
	idsRun2 := build(t)

	if len(idsRun1) == 0 {
		t.Fatal("run1 produced zero sibling_link Beads — test setup did not actually create any matches")
	}
	if len(idsRun1) != len(idsRun2) {
		t.Fatalf("sibling_link count differs across repeated builds: run1=%d run2=%d\nrun1=%v\nrun2=%v",
			len(idsRun1), len(idsRun2), idsRun1, idsRun2)
	}
	for i := range idsRun1 {
		if idsRun1[i] != idsRun2[i] {
			t.Errorf("sibling_link ID set differs at index %d: run1=%s run2=%s", i, idsRun1[i], idsRun2[i])
		}
	}

	// Six shared-antigen opportunities were set up (2 per patient x 3
	// patients); each should clear threshold and produce at least a
	// generation-1 link (further generation-2 links from settleT's
	// convergence are also expected and fine — this only asserts a lower
	// bound so the test fails loudly if the whole fixture stopped matching
	// anything at all, e.g. from an accidental IDF-threshold or scoring
	// regression).
	if len(idsRun1) < 6 {
		t.Errorf("sibling_link count = %d, want >= 6 (2 opportunities x 3 patients, generation-1 links alone)", len(idsRun1))
	}
}

// fixedTimestamp returns a deterministic RFC3339 timestamp derived purely
// from its integer inputs (no wall clock, no package-level counter shared
// across other tests in this package) — hour and minute offsets from a
// fixed base date, entirely independent of test execution order or count,
// so two separate calls to build() in the same test produce byte-identical
// Bead histories.
func fixedTimestamp(hour, minute int) string {
	const digits = "0123456789"
	pad := func(n int) string {
		return string([]byte{digits[n/10%10], digits[n%10]})
	}
	return "2026-03-01T" + pad(hour) + ":" + pad(minute) + ":00Z"
}

// fixedAntigen returns a deterministic per-patient, per-index antigen tag
// for padWithNoiseBeads-style padding beads, so padding never accidentally
// collides with the same tag across two different patients in this test's
// fixture (which would change one patient's antigen-frequency denominator
// depending on unrelated iteration order).
func fixedAntigen(patientName string, i int) string {
	return "loinc:noise-" + patientName + "-" + string(rune('a'+i))
}

// ingestFixedT ingests a Bead with an explicit, fixed timestamp (not
// unsavedBead's nextTimestamp() counter) and fails the test on error. If
// antigens is non-empty, its tags are injected as bead_antigens rows
// directly via seedAntigens (see unsavedBead's doc comment in apc_test.go
// for why this package controls tags this way rather than through a Bead
// field — v3.1 removed Bead.Antigens entirely).
func ingestFixedT(t *testing.T, e *engine.Engine, typ string, parents, antigens []string, content map[string]any, timestamp string) bead.Bead {
	t.Helper()
	if content == nil {
		content = map[string]any{}
	}
	out, err := e.Ingest(bead.Bead{
		Type:      typ,
		Timestamp: timestamp,
		Author:    "did:medbeads:doctor:12345",
		Parents:   parents,
		Content:   content,
	})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if len(antigens) > 0 {
		seedAntigens(t, e, out, antigens...)
	}
	return out
}
