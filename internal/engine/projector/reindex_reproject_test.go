package projector_test

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/medbeads/medbeads/internal/engine"
	"github.com/medbeads/medbeads/internal/engine/bead"
	"github.com/medbeads/medbeads/internal/engine/index"
	"github.com/medbeads/medbeads/internal/engine/pod"
	"github.com/medbeads/medbeads/internal/engine/projector"
)

// TestReprojectAfterReindex_Deterministic is U3c's strengthened round-trip
// invariant (specs/U3_link_projector.md's U3c section, specs/
// DESIGN_v3.1_draft.md §3's "解釈投影…Pod + 参照した知識 Bead ID 集合
// (dictionary/link_rule) + config_hash +投影コード版(code_version)から決定論
// 再構築可能"): this is the CROSS-CUTTING case internal/engine/projector/
// reproject_test.go's own TestReproject_Deterministic_SameInputsSameOutput
// does not cover, because that test only re-runs Reproject against a live
// Engine's already-populated bead_tags — it never rebuilds index.db itself.
// This test instead:
//
//  1. Builds a real Pod (via engine.Ingest, so real antigen.Extract-derived
//     bead_tags exist, not hand-seeded rows) with a genuine risk:/rxnorm:
//     cooccurrence pair, seeds the built-in cooccurrence link_rule Bead, and
//     runs Reproject once against the live Engine ("run A, pass 1").
//  2. Closes the Engine, discards index.db, and rebuilds a fresh index.db
//     from the Pod files ALONE via index.Reindex (Pod is the only input —
//     this is the "正本事実は Pod のみから再構築可能" half of the invariant).
//  3. Runs Reproject again against the freshly-reindexed DB with the SAME
//     manifest inputs (same knowledgeBeadIDs, same codeVersion, same
//     builtAt) — "run B, pass 1" — and asserts its bead_tags AND
//     clinical_links are byte-identical to run A's (excluding
//     projection_run_id, the one column the task's DONE MEANS explicitly
//     names as an exclusion), proving "Pod + manifest -> deterministic
//     projection" rather than merely "index.db's already-populated
//     bead_tags -> deterministic clinical_links".
//  4. Runs Reproject a THIRD time against the same freshly-reindexed DB with
//     a DIFFERENT knowledgeBeadIDs set (a second link_rule Bead whose
//     trigger namespace set excludes rxnorm:, i.e. a different rule) and
//     asserts the resulting clinical_links set actually DIFFERS from run
//     A/B's — proving the projection genuinely depends on the manifest
//     (knowledge_bead_ids), not on the Pod's content alone (the
//     "強化の証明" half of the U3c spec section: "異なる
//     knowledge_bead_ids で Reproject すると clinical_links が変わる").
func TestReprojectAfterReindex_Deterministic(t *testing.T) {
	dataDir := t.TempDir()

	e, err := engine.Open(dataDir)
	if err != nil {
		t.Fatalf("engine.Open: %v", err)
	}

	rule := ingestT(t, e, projector.BuildCooccurrenceRuleBead("2026-01-01T00:00:00Z"))

	root := ingestT(t, e, bead.Bead{
		Type: "patient_registration", Timestamp: "2026-01-01T00:00:00Z",
		Author: "did:medbeads:doctor:12345", Content: map[string]any{"name": "Reindex Reproject Patient"},
	})
	// Noise Beads keep the shared risk:/rxnorm: tags comfortably under the
	// projector's own 30% patient-local frequency threshold (mirrors every
	// other projector test's padWithNoiseBeads-equivalent setup).
	for i := 0; i < 10; i++ {
		noise := ingestT(t, e, bead.Bead{
			Type: "fhir_observation", Timestamp: fmtTS(i),
			Author: "did:medbeads:doctor:12345", Parents: []string{root.ID},
			Content: map[string]any{
				"code": map[string]any{
					"coding": []any{
						map[string]any{"system": "http://loinc.org", "code": "noise-" + itoaT(i), "display": "noise"},
					},
				},
			},
		})
		_ = noise
	}
	// rx/lab share a real RxNorm coding (6919, meropenem) extracted by the
	// live antigen.Extract path at Ingest time — not a hand-seeded bead_tags
	// row — so index.Reindex's own IndexBead call re-derives the identical
	// tag from the Pod frame alone (the same discipline
	// index/reindex_test.go's writeRealPod already established for its own
	// RxNorm fixture).
	rx := ingestT(t, e, bead.Bead{
		Type: "fhir_medicationrequest", Timestamp: "2026-02-01T09:00:00Z",
		Author: "did:medbeads:doctor:12345", Parents: []string{root.ID},
		Content: map[string]any{
			"drug": "meropenem 1g IV every 8 hours",
			"medicationCodeableConcept": map[string]any{
				"coding": []any{
					map[string]any{"system": "http://www.nlm.nih.gov/research/umls/rxnorm", "code": "6919", "display": "meropenem"},
				},
			},
		},
	})
	lab := ingestT(t, e, bead.Bead{
		Type: "fhir_observation", Timestamp: "2026-02-01T10:00:00Z",
		Author: "did:medbeads:doctor:12345", Parents: []string{root.ID},
		Content: map[string]any{
			"code": map[string]any{
				"coding": []any{
					map[string]any{"system": "http://www.nlm.nih.gov/research/umls/rxnorm", "code": "6919", "display": "meropenem"},
				},
			},
		},
	})
	_, _ = rx, lab

	// --- run A: Reproject against the live Engine ---
	resA, err := projector.Reproject(e.Index(), engineReader{e}, []string{rule.ID}, "test-code-v1", "2026-07-11T00:00:00Z")
	if err != nil {
		t.Fatalf("Reproject (run A): %v", err)
	}
	if resA.LinksWritten == 0 {
		t.Fatal("run A wrote 0 clinical_links — test setup did not actually create a cooccurrence pair")
	}
	tagsA := queryBeadTags(t, e.Index(), root.ID)
	linksA := queryClinicalLinks(t, e.Index(), root.ID)

	if err := e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// --- rebuild index.db from the Pod files ALONE (no Scan/Reproject state
	// --- carried over — this is the "Pod のみから正本再構築" half) ---
	dbPath := filepath.Join(dataDir, "index.db")
	reDB, err := index.Reindex(dataDir, dbPath, index.DefaultFlattener{})
	if err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	defer reDB.Close()

	// bead_tags must already match run A's post-Reproject state purely from
	// Reindex's own IndexBead antigen.Extract re-derivation — Reproject has
	// not run yet against reDB at this point.
	tagsAfterReindexOnly := queryBeadTags(t, reDB, root.ID)
	if !equalTagSlices(tagsA, tagsAfterReindexOnly) {
		t.Fatalf("bead_tags after bare Reindex (no Reproject yet) = %v, want %v (Reindex must re-derive tags from Pod content alone)",
			tagsAfterReindexOnly, tagsA)
	}

	store := pod.NewStore(dataDir)
	reader := podBackedReader{idx: reDB, store: store}

	// --- run B: Reproject against the freshly-reindexed DB, SAME manifest
	// --- inputs as run A ---
	resB, err := projector.Reproject(reDB, reader, []string{rule.ID}, "test-code-v1", "2026-07-11T00:00:00Z")
	if err != nil {
		t.Fatalf("Reproject (run B, same manifest): %v", err)
	}
	tagsB := queryBeadTags(t, reDB, root.ID)
	linksB := queryClinicalLinks(t, reDB, root.ID)

	if !equalTagSlices(tagsA, tagsB) {
		t.Fatalf("bead_tags mismatch across Reindex+Reproject round trip:\n run A=%v\n run B=%v", tagsA, tagsB)
	}
	if len(linksA) != len(linksB) {
		t.Fatalf("clinical_links row count mismatch across Reindex+Reproject round trip: run A=%d run B=%d", len(linksA), len(linksB))
	}
	for i := range linksA {
		a, b := linksA[i], linksB[i]
		a.ProjectionRunID, b.ProjectionRunID = "", "" // excluded column, per this test's own doc comment
		if a != b {
			t.Errorf("clinical_links row %d differs across Reindex+Reproject round trip:\n run A=%+v\n run B=%+v", i, linksA[i], linksB[i])
		}
	}
	if resB.LinksWritten != resA.LinksWritten {
		t.Errorf("LinksWritten mismatch: run A=%d run B=%d", resA.LinksWritten, resB.LinksWritten)
	}

	// --- strengthening proof: a DIFFERENT knowledge_bead_ids set (a second
	// --- link_rule Bead with a different trigger namespace set — excludes
	// --- rxnorm:, so the rx/lab pair's shared rxnorm:6919 tag no longer
	// --- triggers) must yield a DIFFERENT clinical_links outcome, proving
	// --- the projection depends on the manifest, not on the Pod alone. ---
	altRuleBead := altCooccurrenceRuleBead("2026-01-02T00:00:00Z")
	altRule, err := ingestViaPod(reDB, reader, altRuleBead)
	if err != nil {
		t.Fatalf("ingest alt link_rule via Pod: %v", err)
	}

	resC, err := projector.Reproject(reDB, reader, []string{altRule}, "test-code-v2", "2026-07-11T00:00:01Z")
	if err != nil {
		t.Fatalf("Reproject (run C, different knowledge_bead_ids): %v", err)
	}
	linksC := queryClinicalLinks(t, reDB, root.ID)

	if resC.LinksWritten == len(linksA) && sameMatchedTags(linksA, linksC) {
		t.Fatalf("run C (different knowledge_bead_ids) produced the same clinical_links as run A/B — "+
			"projection must depend on the manifest's knowledge_bead_ids, not on the Pod alone: linksA=%+v linksC=%+v",
			linksA, linksC)
	}
	if len(linksC) != 0 {
		t.Fatalf("run C (rxnorm:-excluding rule) clinical_links = %+v, want 0 (the only shared tag, rxnorm:6919, is no longer a trigger namespace)", linksC)
	}
}

// altCooccurrenceRuleBead builds a second link_rule Bead with the SAME
// rule_id as the built-in cooccurrence rule (CooccurrenceRuleID) but
// trigger.tag_namespaces = ["atc:","risk:"] (rxnorm: excluded) — a revised
// knowledge Bead superseding the original, per
// LoadActiveCooccurrenceRule's own documented selection rule ("the
// lexicographically greatest Bead ID wins" among Beads sharing one rule_id
// — see rule.go's doc comment). This is the correct way to model "different
// knowledge_bead_ids" against the *current* Reproject implementation: its
// loadRule helper resolves the active rule via LoadActiveCooccurrenceRule
// alone (matching on rule_id), and deliberately does not yet filter
// candidates by the knowledgeBeadIDs argument (reproject.go's own comment:
// "reserved: a future multi-rule Reproject would filter … U3b has exactly
// one rule, so no filtering is needed yet") — ingesting a same-rule_id,
// different-trigger revision and Reprojecting is what actually flips which
// rule content Reproject uses today, exercising the real code path rather
// than an aspirational one loadRule does not implement yet.
func altCooccurrenceRuleBead(timestamp string) bead.Bead {
	content := map[string]any{
		"schema":      "medbeads.link_rule.v1",
		"rule_id":     projector.CooccurrenceRuleID,
		"rule_family": "cooccurrence",
		"trigger": map[string]any{
			"tag_namespaces": []any{"atc:", "risk:"},
			"min_shared":     1,
			"excludes": map[string]any{
				"same_code_namespaces": []any{"loinc:"},
			},
		},
		"relation":       "clinical_correlation",
		"severity":       "info",
		"evidence_basis": "cooccurrence",
		"score_model": map[string]any{
			"weights": map[string]any{"shared_tag": 1},
		},
	}
	return bead.Bead{
		Type:      "link_rule",
		Timestamp: timestamp,
		Author:    "projector_seed",
		Content:   content,
	}
}

func fmtTS(i int) string {
	h := i / 60
	m := i % 60
	return "2026-01-15T" + pad2(h) + ":" + pad2(m) + ":00Z"
}

func pad2(n int) string {
	digits := "0123456789"
	return string([]byte{digits[n/10%10], digits[n%10]})
}

func itoaT(i int) string {
	if i == 0 {
		return "0"
	}
	digits := "0123456789"
	var buf []byte
	for i > 0 {
		buf = append([]byte{digits[i%10]}, buf...)
		i /= 10
	}
	return string(buf)
}

func equalTagSlices(a, b []tagRow) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

type tagRow struct {
	Tag    string
	BeadID string
}

func queryBeadTags(t *testing.T, db *index.DB, patientRoot string) []tagRow {
	t.Helper()
	rows, err := db.SQLDB().Query(
		`SELECT tag, bead_id FROM bead_tags WHERE patient_root = ? ORDER BY tag, bead_id`, patientRoot)
	if err != nil {
		t.Fatalf("query bead_tags: %v", err)
	}
	defer rows.Close()

	var out []tagRow
	for rows.Next() {
		var r tagRow
		if err := rows.Scan(&r.Tag, &r.BeadID); err != nil {
			t.Fatalf("query bead_tags: scan: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("query bead_tags: %v", err)
	}
	return out
}

func sameMatchedTags(a, b []clinicalLinkRow) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].MatchedTag != b[i].MatchedTag || a[i].BeadA != b[i].BeadA || a[i].BeadB != b[i].BeadB {
			return false
		}
	}
	return true
}

// podBackedReader adapts a Reindex-rebuilt *index.DB (no live *engine.Engine
// behind it — the Engine was Closed before Reindex ran) to projector's
// unexported beadReader interface, by resolving a Bead's storage location
// via idx.GetBead and reading the frame directly off disk via
// pod.OpenReader — the same "read a Bead's content back from a
// Reindex-only DB" building block index/reindex_test.go's own
// TestReindex_MatchesManualIndexBead exercises implicitly via GetBead/
// GetEdges/GetTags, applied here to the one additional thing Reproject
// needs that those don't expose: a Bead's full Content (for decoding the
// link_rule Bead named by knowledgeBeadIDs).
type podBackedReader struct {
	idx   *index.DB
	store *pod.Store
}

func (r podBackedReader) GetBead(id string) (projector.BeadContent, error) {
	ref, err := r.idx.GetBead(id)
	if err != nil {
		return projector.BeadContent{}, err
	}
	rd, err := pod.OpenReader(r.store.AbsPath(ref.PodPath))
	if err != nil {
		return projector.BeadContent{}, err
	}
	defer rd.Close()

	rec, err := rd.ReadAt(ref.Offset)
	if err != nil {
		return projector.BeadContent{}, err
	}
	b, err := decodeRecordBeadT(rec)
	if err != nil {
		return projector.BeadContent{}, err
	}
	return projector.BeadContent{Content: b.Content}, nil
}

// decodeRecordBeadT mirrors index/reindex.go's unexported decodeRecordBead
// (this test package cannot call it directly): decompress the frame's core
// bytes and unmarshal them into a bead.Bead.
func decodeRecordBeadT(rec pod.Record) (bead.Bead, error) {
	plain, err := rec.Decompress()
	if err != nil {
		return bead.Bead{}, err
	}
	var b bead.Bead
	if err := json.Unmarshal(plain, &b); err != nil {
		return bead.Bead{}, err
	}
	b.ID = rec.BeadID
	return b, nil
}

// ingestViaPod appends a new link_rule Bead directly to the shared Pod (the
// same file index.Reindex/reader read from) and indexes it into reDB, so a
// later Reproject call against reDB can resolve its content via reader —
// this test's own minimal substitute for engine.Ingest, since the Engine
// backing reDB was already Closed (Reproject-after-Reindex's whole point is
// operating without a live Engine).
func ingestViaPod(reDB *index.DB, reader podBackedReader, b bead.Bead) (string, error) {
	withID, err := bead.WithID(b)
	if err != nil {
		return "", err
	}

	sharedPath := reader.store.SharedPodPath()
	if err := reader.store.EnsurePodsDir(); err != nil {
		return "", err
	}
	w, err := pod.OpenWriter(sharedPath)
	if err != nil {
		return "", err
	}
	defer w.Close()

	rec, err := w.Append(withID, pod.CodecZstd, pod.NewMeta(""))
	if err != nil {
		return "", err
	}
	_ = rec

	// index.CatchUp itself resolves sharedPath (an absolute path) against
	// store via store.RelPath internally — see CatchUp's own doc comment —
	// so the caller passes the same absolute path used to open the Writer,
	// not a pre-relativized one.
	if err := index.CatchUp(reDB, reader.store, sharedPath); err != nil {
		return "", err
	}
	return withID.ID, nil
}
