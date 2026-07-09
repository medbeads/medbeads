package engine

import (
	"math/rand"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/medbeads/medbeads/internal/engine/graph"
	"github.com/medbeads/medbeads/internal/engine/index"
	"github.com/medbeads/medbeads/internal/engine/pod"
)

// perfDataDirEnv is the opt-in switch for TestPerf_PatientBundleLoad and
// TestPerf_FTSResolve: both Skip unless it is set to an existing MedBeads
// data directory (pods/, index.db) — per docs/requirements.md §7's Synthea
// 1,000-patient targets, this harness is meant to be pointed at a real
// ingested dataset (bench/ingest output), not run against the tiny fixtures
// unit tests build. It is never set in CI, so `go test ./...` stays green
// and these two tests are always skipped there (see this task's "環境変数
// 未設定なら Skip" requirement).
const perfDataDirEnv = "MEDBEADS_PERF_DATA"

// perfSampleSize is how many patients TestPerf_PatientBundleLoad samples out
// of index.ListPatients — "ランダム(シード固定)に ~100患者サンプル" per this
// task. Sampling (not every patient) keeps a single `go test` invocation
// fast even against a full 1,000+/1,135-patient dataset, while still large
// enough for a stable median/p95 read.
const perfSampleSize = 100

// perfRandSeed fixes the sample's random source so repeated runs against the
// same dataset pick the same 100 patients — a prerequisite for comparing
// "before/after" perf numbers across two code changes without the sample
// itself introducing noise.
const perfRandSeed = 20260710

// perfBundleLoadTargetMedian is docs/requirements.md §7's "患者バンドル取得
// <10ms" target, judged against the *median* of the sample (see this test's
// doc comment for why median, not max/p95, is the right statistic here).
const perfBundleLoadTargetMedian = 10 * time.Millisecond

// perfFTSResolveTarget is docs/requirements.md §7's "FTS→患者解決 <50ms"
// target.
const perfFTSResolveTarget = 50 * time.Millisecond

// openPerfEngine opens dir (MEDBEADS_PERF_DATA) read-only-in-practice: it
// still goes through the normal engine.Open (acquiring the data directory's
// flock and running crash recovery), since that is the only supported way to
// get a consistent *index.DB + Pod store pair, and it is the caller's own,
// separately-ingested small dataset (never the live background-ingest
// directory this task's instructions forbid touching).
func openPerfEngine(t *testing.T, dir string) *Engine {
	t.Helper()
	e, err := Open(dir)
	if err != nil {
		t.Fatalf("engine.Open(%s): %v (is another medbeadsd/engine.Open already holding this data directory's lock?)", dir, err)
	}
	t.Cleanup(func() { _ = e.Close() })
	return e
}

// perfSamplePatients returns up to n patient_root IDs sampled without
// replacement from every patient index.ListPatients reports, using a
// fixed-seed math/rand source (perfRandSeed) so the sample is reproducible
// across runs against the same dataset. If the dataset has fewer than n
// patients, every patient is returned (still shuffled, so which one N
// patients "the sample" contains is deterministic either way).
func perfSamplePatients(t *testing.T, idx *index.DB, n int) []index.BeadRef {
	t.Helper()
	all, err := idx.ListPatients()
	if err != nil {
		t.Fatalf("ListPatients: %v", err)
	}
	if len(all) == 0 {
		t.Fatalf("ListPatients returned 0 patients under %s — is this an ingested MedBeads data directory?", idx.Path())
	}

	rng := rand.New(rand.NewSource(perfRandSeed))
	rng.Shuffle(len(all), func(i, j int) { all[i], all[j] = all[j], all[i] })

	if len(all) > n {
		all = all[:n]
	}
	return all
}

// durationStats holds median/p95/max over a sorted []time.Duration sample,
// the three figures this task's report asks each benchmark to print.
type durationStats struct {
	n      int
	median time.Duration
	p95    time.Duration
	max    time.Duration
}

// computeDurationStats sorts a copy of samples and derives median/p95/max.
// Percentile indexing uses the common "nearest-rank" method (ceil(p*n)-1,
// clamped): exact interpolation is not worth the complexity for a ~100-point
// sample used for a go/no-go report, not a published benchmark figure.
func computeDurationStats(samples []time.Duration) durationStats {
	sorted := append([]time.Duration(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	n := len(sorted)
	percentile := func(p float64) time.Duration {
		if n == 0 {
			return 0
		}
		idx := int(p*float64(n) + 0.9999999) // ceil
		if idx < 1 {
			idx = 1
		}
		if idx > n {
			idx = n
		}
		return sorted[idx-1]
	}

	return durationStats{
		n:      n,
		median: percentile(0.5),
		p95:    percentile(0.95),
		max:    sorted[n-1],
	}
}

// TestPerf_PatientBundleLoad measures graph.LoadBundle's wall-clock cost
// against a random (fixed-seed) sample of real patients from a dataset
// pointed to by MEDBEADS_PERF_DATA, checking it against docs/requirements.md
// §7's "患者バンドル取得 <10ms" target.
//
// The target is judged against the sample's *median*, not its max or p95:
// Synthea's own patient-age distribution includes a long tail of very old
// (80s-100s) synthetic patients who accumulate an order of magnitude more
// Beads than a typical patient (specs/DESIGN_v3.md §3's ~900-Bead estimate
// is itself a "typical" figure, not a ceiling) — a handful of such patients
// in a 100-patient sample would make max/p95 dominated by dataset
// demographics rather than by whether LoadBundle itself is fast, which is
// what this target is actually meant to catch. The full distribution
// (median/p95/max) and the Bead-count-vs-latency correlation are still
// printed so a reviewer can see whether large-patient latency is scaling
// reasonably (roughly linear in Bead count, per LoadBundle's single
// sequential Pod scan) rather than blowing up.
func TestPerf_PatientBundleLoad(t *testing.T) {
	dir := os.Getenv(perfDataDirEnv)
	if dir == "" {
		t.Skipf("skipping: set %s=<medbeads data dir> to run (see internal/engine/perf_bench_test.go)", perfDataDirEnv)
	}

	e := openPerfEngine(t, dir)
	store := pod.NewStore(e.DataDir())

	patients := perfSamplePatients(t, e.Index(), perfSampleSize)

	type sample struct {
		patientID string
		beads     int
		elapsed   time.Duration
	}
	results := make([]sample, 0, len(patients))

	for _, p := range patients {
		start := time.Now()
		bd, err := graph.LoadBundle(store, p.ID)
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("LoadBundle(%s): %v", p.ID, err)
		}
		results = append(results, sample{patientID: p.ID, beads: bd.Beads(), elapsed: elapsed})
	}

	durations := make([]time.Duration, len(results))
	for i, r := range results {
		durations[i] = r.elapsed
	}
	stats := computeDurationStats(durations)

	// Bead-count vs latency correlation: report the mean beads/ms across the
	// sample plus the single slowest patient's own (beads, elapsed) pair, so
	// a reviewer can see at a glance whether an outlier is "large patient,
	// proportionally slow" (expected) or "small patient, disproportionately
	// slow" (a real regression).
	var slowest sample
	var totalBeads int
	for _, r := range results {
		totalBeads += r.beads
		if r.elapsed > slowest.elapsed {
			slowest = r
		}
	}

	t.Logf("patient bundle load: n=%d median=%s p95=%s max=%s (target: median < %s)",
		stats.n, stats.median, stats.p95, stats.max, perfBundleLoadTargetMedian)
	t.Logf("patient bundle load: mean beads/patient=%.1f, slowest patient=%d beads in %s",
		float64(totalBeads)/float64(len(results)), slowest.beads, slowest.elapsed)

	// DESIGN §3's headline claim is stated for a "typical" ~900-Bead patient,
	// so additionally report the subset of sampled patients in the 800-1000
	// Bead band — this is the number to quote against docs/requirements.md §7
	// verbatim. Informational only (the pass/fail gate stays on the overall
	// median): the band can legitimately be empty in a small sample.
	var bandDurations []time.Duration
	for _, r := range results {
		if r.beads >= 800 && r.beads <= 1000 {
			bandDurations = append(bandDurations, r.elapsed)
		}
	}
	if len(bandDurations) > 0 {
		bandStats := computeDurationStats(bandDurations)
		t.Logf("patient bundle load (~900-bead band, 800-1000): n=%d median=%s p95=%s max=%s",
			bandStats.n, bandStats.median, bandStats.p95, bandStats.max)
	} else {
		t.Logf("patient bundle load (~900-bead band, 800-1000): no sampled patients in band")
	}

	if stats.median > perfBundleLoadTargetMedian {
		t.Errorf("median patient bundle load %s exceeds docs/requirements.md §7 target of %s",
			stats.median, perfBundleLoadTargetMedian)
	}
}

// perfSearchQueries are representative FTS anchor queries covering the three
// content families docs/requirements.md §7's "FTS→患者解決" target is meant
// to cover (medication / lab-test / symptom-ish text) — deliberately
// English-only substrings (per this task's "日本語不要 — Synthea は英語"),
// chosen to be plausible partial matches against Synthea's own
// medicationCodeableConcept.text / code.coding[].display / Condition.code.text
// content (see index.DefaultFlattener, which indexes every string in a
// Bead's Content verbatim — these are generic enough substrings to expect
// >0 hits against any reasonably-sized Synthea corpus, but this test does
// not assert a nonzero hit count itself: a 0-hit query is still a valid FTS
// round-trip to time, and asserting hits>0 would make this test dataset-
// dependent in a way its actual job — timing — does not need).
var perfSearchQueries = []string{
	"acetaminophen",
	"amlodipine",
	"hydrochlorothiazide",
	"metformin",
	"atorvastatin",
	"hemoglobin",
	"glucose",
	"cholesterol",
	"creatinine",
	"potassium",
	"hypertension",
	"diabetes",
	"asthma",
	"anxiety",
	"fracture",
}

// TestPerf_FTSResolve measures index.DB.Search's wall-clock cost (FTS5
// trigram match + the single patient_root-resolving JOIN, R4.1) against
// docs/requirements.md §7's "FTS→患者解決 <50ms" target, run against
// MEDBEADS_PERF_DATA. Each query in perfSearchQueries is run limit=20 and
// limit=50 (this task's "limit 20〜50" range) to see whether result-set size
// itself moves the needle; all (query, limit) timings are pooled into one
// distribution since the target is a single number regardless of which
// representative query triggered it.
func TestPerf_FTSResolve(t *testing.T) {
	dir := os.Getenv(perfDataDirEnv)
	if dir == "" {
		t.Skipf("skipping: set %s=<medbeads data dir> to run (see internal/engine/perf_bench_test.go)", perfDataDirEnv)
	}

	e := openPerfEngine(t, dir)
	idx := e.Index()

	var durations []time.Duration
	var totalHits int
	for _, q := range perfSearchQueries {
		for _, limit := range []int{20, 50} {
			start := time.Now()
			results, err := idx.Search(q, limit)
			elapsed := time.Since(start)
			if err != nil {
				t.Fatalf("Search(%q, limit=%d): %v", q, limit, err)
			}
			durations = append(durations, elapsed)
			totalHits += len(results)
		}
	}

	stats := computeDurationStats(durations)
	t.Logf("FTS->patient resolve: n=%d (queries=%d x limits={20,50}) median=%s p95=%s max=%s (target: median < %s)",
		stats.n, len(perfSearchQueries), stats.median, stats.p95, stats.max, perfFTSResolveTarget)
	t.Logf("FTS->patient resolve: total hits across all queries=%d", totalHits)

	if stats.median > perfFTSResolveTarget {
		t.Errorf("median FTS->patient resolve %s exceeds docs/requirements.md §7 target of %s",
			stats.median, perfFTSResolveTarget)
	}
}
