package projector_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/medbeads/medbeads/internal/engine"
	"github.com/medbeads/medbeads/internal/engine/bead"
	"github.com/medbeads/medbeads/internal/engine/index"
	"github.com/medbeads/medbeads/internal/engine/projector"
)

// engineReader adapts *engine.Engine to projector.beadReader (an unexported
// interface — Go structural typing lets this test package satisfy it without
// importing anything unexported).
type engineReader struct{ e *engine.Engine }

func (r engineReader) GetBead(id string) (projector.BeadContent, error) {
	b, err := r.e.GetBead(id)
	if err != nil {
		return projector.BeadContent{}, err
	}
	return projector.BeadContent{Content: b.Content}, nil
}

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
	return fmt.Sprintf("2026-01-01T%02d:%02d:%02dZ", h, m, s)
}

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

func seedPatient(t *testing.T, e *engine.Engine, name string) bead.Bead {
	t.Helper()
	return ingestT(t, e, unsavedBead("patient_registration", nil, map[string]any{"name": name}))
}

// seedTags inserts bead_tags rows for an already-ingested Bead directly
// (bypassing antigen.Extract), mirroring apc_test.go's seedAntigens helper —
// this package's tests exercise projector behavior given certain bead_tags
// tags exist, not tag derivation itself.
func seedTags(t *testing.T, e *engine.Engine, b bead.Bead, tags ...string) {
	t.Helper()
	ref, err := e.Index().GetBead(b.ID)
	if err != nil {
		t.Fatalf("seedTags(%s): resolve patient_root: %v", b.ID, err)
	}
	var root any
	if ref.PatientRoot != "" {
		root = ref.PatientRoot
	}
	for _, tag := range tags {
		if _, err := e.Index().SQLDB().Exec(
			`INSERT OR IGNORE INTO bead_tags (tag, bead_id, patient_root) VALUES (?, ?, ?)`,
			tag, b.ID, root,
		); err != nil {
			t.Fatalf("seedTags(%s, %v): %v", b.ID, tags, err)
		}
	}
}

func seedChildBead(t *testing.T, e *engine.Engine, parent bead.Bead, typ string, tags []string, content map[string]any) bead.Bead {
	t.Helper()
	b := ingestT(t, e, unsavedBead(typ, []string{parent.ID}, content))
	if len(tags) > 0 {
		seedTags(t, e, b, tags...)
	}
	return b
}

// padWithNoiseBeads keeps any genuinely-shared tag comfortably under the
// projector's own 30% patient-local frequency threshold, mirroring
// apc_test.go's identical helper for the identical reason.
func padWithNoiseBeads(t *testing.T, e *engine.Engine, parent bead.Bead, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		seedChildBead(t, e, parent, "fhir_observation",
			[]string{fmt.Sprintf("loinc:noise-%d", i)},
			map[string]any{"noise": i})
	}
}

func seedCooccurrenceRule(t *testing.T, e *engine.Engine) bead.Bead {
	t.Helper()
	ruleBead := projector.BuildCooccurrenceRuleBead("2026-01-01T00:00:00Z")
	return ingestT(t, e, ruleBead)
}

func countRows(t *testing.T, db *index.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.SQLDB().QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("countRows(%q): %v", query, err)
	}
	return n
}

type clinicalLinkRow struct {
	LinkID          string
	BeadA           string
	BeadB           string
	PatientRoot     string
	Relation        string
	MatchedTag      string
	Severity        string
	EvidenceBasis   string
	EvidenceBeadIDs string
	ScoreBreakdown  string
	RuleID          string
	RuleVersion     string
	ProjectionRunID string
	CreatedAt       string
}

func queryClinicalLinks(t *testing.T, db *index.DB, patientRoot string) []clinicalLinkRow {
	t.Helper()
	rows, err := db.SQLDB().Query(`
		SELECT link_id, bead_a, bead_b, patient_root, relation, matched_tag,
		       severity, evidence_basis, evidence_bead_ids, score_breakdown,
		       rule_id, COALESCE(rule_version, ''), COALESCE(projection_run_id, ''), created_at
		FROM clinical_links WHERE patient_root = ? ORDER BY bead_a, bead_b, matched_tag`, patientRoot)
	if err != nil {
		t.Fatalf("query clinical_links: %v", err)
	}
	defer rows.Close()

	var out []clinicalLinkRow
	for rows.Next() {
		var r clinicalLinkRow
		if err := rows.Scan(&r.LinkID, &r.BeadA, &r.BeadB, &r.PatientRoot, &r.Relation, &r.MatchedTag,
			&r.Severity, &r.EvidenceBasis, &r.EvidenceBeadIDs, &r.ScoreBreakdown,
			&r.RuleID, &r.RuleVersion, &r.ProjectionRunID, &r.CreatedAt); err != nil {
			t.Fatalf("query clinical_links: scan: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("query clinical_links: %v", err)
	}
	return out
}

// --- (c): a risk:/atc: cooccurrence produces exactly one info/cooccurrence
// clinical_links row with rule_version == the link_rule Bead ID -----------

func TestReproject_CooccurrencePair_CreatesInfoLink(t *testing.T) {
	e := openT(t)
	rule := seedCooccurrenceRule(t, e)

	root := seedPatient(t, e, "patient A")
	padWithNoiseBeads(t, e, root, 10)
	rx := seedChildBead(t, e, root, "fhir_medicationrequest",
		[]string{"risk:nephrotoxic", "atc:c09aa03"}, map[string]any{"drug": "meropenem"})
	lab := seedChildBead(t, e, root, "fhir_observation",
		[]string{"risk:nephrotoxic"}, map[string]any{"test": "eGFR"})

	res, err := projector.Reproject(e.Index(), engineReader{e}, []string{rule.ID}, "test-code-v1", "2026-07-11T00:00:00Z")
	if err != nil {
		t.Fatalf("Reproject: %v", err)
	}
	if res.PatientsProjected != 1 {
		t.Errorf("PatientsProjected = %d, want 1", res.PatientsProjected)
	}

	links := queryClinicalLinks(t, e.Index(), root.ID)
	if len(links) != 1 {
		t.Fatalf("clinical_links rows = %d, want 1: %+v", len(links), links)
	}
	link := links[0]

	wantA, wantB := rx.ID, lab.ID
	if wantB < wantA {
		wantA, wantB = wantB, wantA
	}
	if link.BeadA != wantA || link.BeadB != wantB {
		t.Errorf("bead_a/bead_b = %s/%s, want %s/%s", link.BeadA, link.BeadB, wantA, wantB)
	}
	if link.Severity != "info" {
		t.Errorf("severity = %q, want info", link.Severity)
	}
	if link.EvidenceBasis != "cooccurrence" {
		t.Errorf("evidence_basis = %q, want cooccurrence", link.EvidenceBasis)
	}
	if link.MatchedTag != "risk:nephrotoxic" {
		t.Errorf("matched_tag = %q, want risk:nephrotoxic", link.MatchedTag)
	}
	if link.RuleVersion != rule.ID {
		t.Errorf("rule_version = %q, want link_rule Bead ID %q", link.RuleVersion, rule.ID)
	}
	if link.RuleID != projector.CooccurrenceRuleID {
		t.Errorf("rule_id = %q, want %q", link.RuleID, projector.CooccurrenceRuleID)
	}
	if link.ProjectionRunID != res.RunID {
		t.Errorf("projection_run_id = %q, want %q", link.ProjectionRunID, res.RunID)
	}
	if link.EvidenceBeadIDs != "[]" {
		t.Errorf("evidence_bead_ids = %q, want []", link.EvidenceBeadIDs)
	}
	var scoreBreakdown map[string]any
	if err := json.Unmarshal([]byte(link.ScoreBreakdown), &scoreBreakdown); err != nil {
		t.Errorf("score_breakdown not valid JSON: %v", err)
	}
}

// --- (b): LOINC-only pair -> no link; temporal-only -> no link -------------

func TestReproject_LoincOnlyPair_NoLink(t *testing.T) {
	e := openT(t)
	rule := seedCooccurrenceRule(t, e)

	root := seedPatient(t, e, "patient A")
	padWithNoiseBeads(t, e, root, 10)
	// Two Beads sharing only a loinc: tag (same lab code measured twice) —
	// must not trigger a link at all (U3b's noise-exclusion point).
	labA := seedChildBead(t, e, root, "fhir_observation",
		[]string{"loinc:2160-0"}, map[string]any{"test": "creatinine", "n": 1})
	labB := seedChildBead(t, e, root, "fhir_observation",
		[]string{"loinc:2160-0"}, map[string]any{"test": "creatinine", "n": 2})
	_, _ = labA, labB

	if _, err := projector.Reproject(e.Index(), engineReader{e}, []string{rule.ID}, "test-code-v1", "2026-07-11T00:00:00Z"); err != nil {
		t.Fatalf("Reproject: %v", err)
	}

	links := queryClinicalLinks(t, e.Index(), root.ID)
	if len(links) != 0 {
		t.Fatalf("clinical_links rows = %d, want 0 (loinc-only cooccurrence must not link): %+v", len(links), links)
	}
}

func TestReproject_TemporalOnlyPair_NoLink(t *testing.T) {
	e := openT(t)
	rule := seedCooccurrenceRule(t, e)

	root := seedPatient(t, e, "patient A")
	padWithNoiseBeads(t, e, root, 10)
	// fhir_encounter Beads both carry temporal:encounter (beadTypeTemporal in
	// package antigen) — sharing only that tag must not trigger a link.
	a := seedChildBead(t, e, root, "fhir_encounter", []string{"temporal:encounter"}, map[string]any{"n": 1})
	b := seedChildBead(t, e, root, "fhir_encounter", []string{"temporal:encounter"}, map[string]any{"n": 2})
	_, _ = a, b

	if _, err := projector.Reproject(e.Index(), engineReader{e}, []string{rule.ID}, "test-code-v1", "2026-07-11T00:00:00Z"); err != nil {
		t.Fatalf("Reproject: %v", err)
	}

	links := queryClinicalLinks(t, e.Index(), root.ID)
	if len(links) != 0 {
		t.Fatalf("clinical_links rows = %d, want 0 (temporal-only cooccurrence must not link): %+v", len(links), links)
	}
}

// --- (a): determinism — running the projector twice on the same bead_tags
// + same rule Bead yields byte-identical clinical_links (excluding
// projection_run_id, which varies per run) ----------------------------------

func TestReproject_Deterministic_SameInputsSameOutput(t *testing.T) {
	build := func(t *testing.T) []clinicalLinkRow {
		e := openT(t)
		rule := ingestT(t, e, bead.Bead{
			Type: "link_rule", Timestamp: "2026-01-01T00:00:00Z", Author: "projector_seed",
			Content: projector.BuildCooccurrenceRuleBead("2026-01-01T00:00:00Z").Content,
		})

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
			seedTags(t, e, noise, fmt.Sprintf("loinc:noise-%d", i))
		}
		rx := ingestT(t, e, bead.Bead{
			Type: "fhir_medicationrequest", Timestamp: "2026-02-01T09:00:00Z",
			Author: "did:medbeads:doctor:12345", Parents: []string{root.ID},
			Content: map[string]any{"drug": "meropenem"},
		})
		seedTags(t, e, rx, "risk:nephrotoxic", "atc:c09aa03")
		lab := ingestT(t, e, bead.Bead{
			Type: "fhir_observation", Timestamp: "2026-02-01T10:00:00Z",
			Author: "did:medbeads:doctor:12345", Parents: []string{root.ID},
			Content: map[string]any{"test": "eGFR"},
		})
		seedTags(t, e, lab, "risk:nephrotoxic")

		if _, err := projector.Reproject(e.Index(), engineReader{e}, []string{rule.ID}, "test-code-v1", "2026-07-11T00:00:00Z"); err != nil {
			t.Fatalf("Reproject: %v", err)
		}
		return queryClinicalLinks(t, e.Index(), root.ID)
	}

	run1 := build(t)
	run2 := build(t)

	if len(run1) != len(run2) {
		t.Fatalf("run1 has %d links, run2 has %d", len(run1), len(run2))
	}
	for i := range run1 {
		a, b := run1[i], run2[i]
		// Every column except ProjectionRunID must match byte-for-byte —
		// run_id legitimately varies (it is derived in part from the
		// knowledge_bead_ids/config, but the two independent build() calls
		// here use the same literal inputs and builtAt, so even run_id
		// happens to match; the comparison below explicitly excludes it
		// anyway per the task's stated exclusion).
		a.ProjectionRunID, b.ProjectionRunID = "", ""
		if a != b {
			t.Errorf("row %d differs:\n run1=%+v\n run2=%+v", i, run1[i], run2[i])
		}
	}
}

// --- (d): manifest flip — after Reproject, exactly one 'active' manifest
// row for the projection_name, prior 'active' became 'superseded' ----------

func TestReproject_ManifestFlip_OneActivePerProjectionName(t *testing.T) {
	e := openT(t)
	rule := seedCooccurrenceRule(t, e)
	root := seedPatient(t, e, "patient A")
	padWithNoiseBeads(t, e, root, 5)

	res1, err := projector.Reproject(e.Index(), engineReader{e}, []string{rule.ID}, "test-code-v1", "2026-07-11T00:00:00Z")
	if err != nil {
		t.Fatalf("first Reproject: %v", err)
	}

	if n := countRows(t, e.Index(),
		`SELECT COUNT(*) FROM projection_manifest WHERE status = 'active' AND projection_name = ?`,
		projector.ProjectionName); n != 1 {
		t.Fatalf("active manifests after first Reproject = %d, want 1", n)
	}

	res2, err := projector.Reproject(e.Index(), engineReader{e}, []string{rule.ID}, "test-code-v2", "2026-07-11T00:00:01Z")
	if err != nil {
		t.Fatalf("second Reproject: %v", err)
	}
	if res2.RunID == res1.RunID {
		t.Fatalf("second Reproject reused run_id %s from first (must be distinct: different builtAt/code_version)", res1.RunID)
	}

	if n := countRows(t, e.Index(),
		`SELECT COUNT(*) FROM projection_manifest WHERE status = 'active' AND projection_name = ?`,
		projector.ProjectionName); n != 1 {
		t.Fatalf("active manifests after second Reproject = %d, want 1", n)
	}
	var supersededStatus string
	if err := e.Index().SQLDB().QueryRow(
		`SELECT status FROM projection_manifest WHERE run_id = ?`, res1.RunID,
	).Scan(&supersededStatus); err != nil {
		t.Fatalf("query first run's manifest status: %v", err)
	}
	if supersededStatus != "superseded" {
		t.Errorf("first run's manifest status = %q, want superseded", supersededStatus)
	}
	var activeStatus string
	if err := e.Index().SQLDB().QueryRow(
		`SELECT status FROM projection_manifest WHERE run_id = ?`, res2.RunID,
	).Scan(&activeStatus); err != nil {
		t.Fatalf("query second run's manifest status: %v", err)
	}
	if activeStatus != "active" {
		t.Errorf("second run's manifest status = %q, want active", activeStatus)
	}
}

// --- Reproject does not mint any sibling_link Bead --------------------------

func TestReproject_DoesNotCreateSiblingLinkBeads(t *testing.T) {
	e := openT(t)
	rule := seedCooccurrenceRule(t, e)
	root := seedPatient(t, e, "patient A")
	padWithNoiseBeads(t, e, root, 10)
	seedChildBead(t, e, root, "fhir_medicationrequest",
		[]string{"risk:nephrotoxic", "atc:c09aa03"}, map[string]any{"drug": "meropenem"})
	seedChildBead(t, e, root, "fhir_observation",
		[]string{"risk:nephrotoxic"}, map[string]any{"test": "eGFR"})

	if _, err := projector.Reproject(e.Index(), engineReader{e}, []string{rule.ID}, "test-code-v1", "2026-07-11T00:00:00Z"); err != nil {
		t.Fatalf("Reproject: %v", err)
	}

	if n := countRows(t, e.Index(), `SELECT COUNT(*) FROM beads WHERE type = 'sibling_link'`); n != 0 {
		t.Errorf("sibling_link Bead count = %d, want 0 (Reproject must not mint sibling_link Beads)", n)
	}
}
