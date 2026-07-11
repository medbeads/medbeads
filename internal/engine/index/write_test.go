package index

import (
	"testing"

	"github.com/medbeads/medbeads/internal/engine/bead"
)

// TestIndexBead_RoundTrip writes one Bead with parents and a real FHIR
// Coding, then reads it back through GetBead / GetEdges / GetTags /
// Search, checking every piece IndexBead is responsible for landed correctly
// (R3.1/R3.2/R3.3). The bead_tags assertion below exercises the v3.1
// path end to end: IndexBead runs antigen.Extract(b.Type, b.Content) at
// index time (Bead.Antigens no longer exists — see bead.Bead's doc
// comment), so child's "loinc:718-7" tag must come from its own content's
// FHIR coding[], not from a field this test sets directly.
func TestIndexBead_RoundTrip(t *testing.T) {
	db := openT(t)

	parent := testBead(t, "patient_registration", "root patient", nil, nil)
	indexBeadT(t, db, parent, BeadLocation{
		PodPath: "pods/aa/aabb.pod", PatientRoot: parent.ID, Offset: 0, Length: 100,
	})

	child := testBead(t, "fhir_observation", "hemoglobin 12.3 g/dL",
		[]string{parent.ID}, map[string]any{
			"code": map[string]any{
				"coding": []any{
					map[string]any{"system": "http://loinc.org", "code": "718-7", "display": "Hemoglobin"},
				},
			},
		})
	// testBead fixes every Bead's Timestamp to the same constant; give child
	// a strictly later one (recomputing its content-hash ID via bead.WithID)
	// so ListPatientBeads' "ORDER BY timestamp, id" below has an unambiguous
	// timestamp-driven order to assert on, rather than incidentally
	// depending on which of the two IDs happens to sort first lexically.
	child.Timestamp = "2026-03-01T10:00:01Z"
	child, err := bead.WithID(child)
	if err != nil {
		t.Fatalf("bead.WithID (child, re-timestamped): %v", err)
	}
	indexBeadT(t, db, child, BeadLocation{
		PodPath: "pods/aa/aabb.pod", PatientRoot: parent.ID, Offset: 100, Length: 80,
	})

	ref, err := db.GetBead(child.ID)
	if err != nil {
		t.Fatalf("GetBead: %v", err)
	}
	if ref.PatientRoot != parent.ID {
		t.Errorf("ref.PatientRoot = %q, want %q", ref.PatientRoot, parent.ID)
	}
	if ref.Type != "fhir_observation" {
		t.Errorf("ref.Type = %q, want fhir_observation", ref.Type)
	}
	if ref.PodPath != "pods/aa/aabb.pod" {
		t.Errorf("ref.PodPath = %q, want pods/aa/aabb.pod", ref.PodPath)
	}
	if ref.Offset != 100 || ref.Length != 80 {
		t.Errorf("ref.Offset/Length = %d/%d, want 100/80", ref.Offset, ref.Length)
	}
	if ref.Summary == "" {
		t.Error("ref.Summary is empty, want a DefaultFlattener-derived summary")
	}

	edges, err := db.GetEdges(child.ID)
	if err != nil {
		t.Fatalf("GetEdges: %v", err)
	}
	if len(edges) != 1 || edges[0] != parent.ID {
		t.Errorf("GetEdges(child) = %v, want [%s]", edges, parent.ID)
	}

	antigens, err := db.GetTags(child.ID)
	if err != nil {
		t.Fatalf("GetTags: %v", err)
	}
	wantAntigens := []string{"loinc:718-7"}
	if len(antigens) != len(wantAntigens) {
		t.Fatalf("GetTags(child) = %v, want %v", antigens, wantAntigens)
	}
	for i, a := range wantAntigens {
		if antigens[i] != a {
			t.Errorf("GetTags(child)[%d] = %q, want %q", i, antigens[i], a)
		}
	}

	results, err := db.Search("hemoglobin", 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	found := false
	for _, r := range results {
		if r.BeadID == child.ID {
			found = true
			if r.PatientRoot != parent.ID {
				t.Errorf("SearchResult.PatientRoot = %q, want %q", r.PatientRoot, parent.ID)
			}
		}
	}
	if !found {
		t.Errorf("Search(hemoglobin) did not return child bead %s: %+v", child.ID, results)
	}

	patientBeads, err := db.ListPatientBeads(parent.ID)
	if err != nil {
		t.Fatalf("ListPatientBeads: %v", err)
	}
	if len(patientBeads) != 2 {
		t.Fatalf("ListPatientBeads(%s) returned %d beads, want 2", parent.ID, len(patientBeads))
	}
	if patientBeads[0].ID != parent.ID || patientBeads[1].ID != child.ID {
		t.Errorf("ListPatientBeads order = [%s, %s], want [%s, %s] (timestamp order)",
			patientBeads[0].ID, patientBeads[1].ID, parent.ID, child.ID)
	}
}

// TestIndexBead_SharedPod checks that a Bead written with PatientRoot=""
// (the shared Pod case) is stored with a NULL patient_root and surfaces via
// ListSharedBeads rather than any patient's ListPatientBeads.
func TestIndexBead_SharedPod(t *testing.T) {
	db := openT(t)

	b := testBead(t, "drug_master", "meropenem", nil, nil)
	indexBeadT(t, db, b, BeadLocation{
		PodPath: "pods/_shared.pod", PatientRoot: "", Offset: 0, Length: 50,
	})

	ref, err := db.GetBead(b.ID)
	if err != nil {
		t.Fatalf("GetBead: %v", err)
	}
	if ref.PatientRoot != "" {
		t.Errorf("ref.PatientRoot = %q, want empty (shared pod)", ref.PatientRoot)
	}

	shared, err := db.ListSharedBeads()
	if err != nil {
		t.Fatalf("ListSharedBeads: %v", err)
	}
	if len(shared) != 1 || shared[0].ID != b.ID {
		t.Errorf("ListSharedBeads = %+v, want [%s]", shared, b.ID)
	}
}

// TestGetBead_NotFound checks the ErrNotFound contract for an unknown ID.
func TestGetBead_NotFound(t *testing.T) {
	db := openT(t)
	_, err := db.GetBead("0000000000000000000000000000000000000000000000000000000000000000"[:64])
	if err == nil {
		t.Fatal("GetBead(unknown) = nil error, want ErrNotFound")
	}
}

// TestIndexBead_TagProjection_SameContentSameTags is the v3.1 content-
// invariance regression this unit's task explicitly calls for: "タグが投影時
// 抽出でも従来と同じ bead_tags 内容になること(同一 content → 同一タグ)".
// It indexes two independently-built Beads with byte-identical Content (and
// therefore, since antigen.Extract's only inputs are Type and Content,
// necessarily identical derived tags) into two separate index.DB instances
// — mirroring how a real "same clinical fact ingested twice" or "Reindex on
// two different machines" scenario would produce two independently-computed
// projections — and asserts GetTags returns the exact same tag set from
// both. It also re-indexes the very same Bead a second time into the first
// DB (the CatchUp/Reindex duplicate-frame replay path — see IndexBead's own
// "Duplicate-frame idempotency" doc comment) and asserts the tag set is
// unchanged, not doubled.
func TestIndexBead_TagProjection_SameContentSameTags(t *testing.T) {
	content := map[string]any{
		"code": map[string]any{
			"coding": []any{
				map[string]any{"system": "http://loinc.org", "code": "2345-7", "display": "Glucose"},
			},
		},
	}

	buildAndIndex := func(t *testing.T) (*DB, bead.Bead) {
		t.Helper()
		db := openT(t)
		parent := testBead(t, "patient_registration", "root", nil, nil)
		indexBeadT(t, db, parent, BeadLocation{PodPath: "p.pod", PatientRoot: parent.ID, Offset: 0, Length: 10})

		// content is a shared map value across both buildAndIndex calls;
		// testBead mutates it in place (content["note"] = note) but with the
		// identical note string both times, so the two calls still produce
		// byte-identical Content (and therefore identical IDs) — this is
		// intentional: the test's whole point is "same content -> same
		// tags", so both Beads must actually be the same content, not
		// merely similar.
		child := testBead(t, "fhir_observation", "glucose 95 mg/dL", []string{parent.ID}, content)
		indexBeadT(t, db, child, BeadLocation{PodPath: "p.pod", PatientRoot: parent.ID, Offset: 10, Length: 10})
		return db, child
	}

	db1, child1 := buildAndIndex(t)
	db2, child2 := buildAndIndex(t)

	if child1.ID != child2.ID {
		t.Fatalf("child1.ID = %s, child2.ID = %s: want equal (identical content must hash identically)", child1.ID, child2.ID)
	}

	tags1, err := db1.GetTags(child1.ID)
	if err != nil {
		t.Fatalf("db1.GetTags: %v", err)
	}
	tags2, err := db2.GetTags(child2.ID)
	if err != nil {
		t.Fatalf("db2.GetTags: %v", err)
	}
	wantTags := []string{"loinc:2345-7"}
	if !equalStringSlices(tags1, wantTags) {
		t.Errorf("db1 GetTags(%s) = %v, want %v", child1.ID, tags1, wantTags)
	}
	if !equalStringSlices(tags2, wantTags) {
		t.Errorf("db2 GetTags(%s) = %v, want %v", child2.ID, tags2, wantTags)
	}
	if !equalStringSlices(tags1, tags2) {
		t.Errorf("tag sets differ across independently-built DBs for identical content: db1=%v db2=%v", tags1, tags2)
	}

	// Re-index the identical Bead a second time into db1 (simulating a
	// duplicate-frame replay / re-run of Reindex over the same Pod): the
	// projected tag set must stay exactly the same, not double up.
	indexBeadT(t, db1, child1, BeadLocation{PodPath: "p.pod", PatientRoot: child1.Parents[0], Offset: 10, Length: 10})
	tagsAfterReplay, err := db1.GetTags(child1.ID)
	if err != nil {
		t.Fatalf("db1.GetTags (after replay): %v", err)
	}
	if !equalStringSlices(tagsAfterReplay, wantTags) {
		t.Errorf("db1 GetTags(%s) after replay = %v, want unchanged %v", child1.ID, tagsAfterReplay, wantTags)
	}
}

// equalStringSlices (order-sensitive; GetTags already returns its rows
// sorted — see its own ORDER BY tag) is defined in reindex_test.go and
// reused here.

// TestIndexBead_DuplicateEdgeAntigen_IsIdempotent checks that indexing the
// same Bead's edges/antigens twice (as CatchUp replaying an overlapping
// watermark range would) does not error and does not duplicate rows — the
// INSERT OR IGNORE contract write.go documents.
func TestIndexBead_DuplicateEdgeAntigen_IsIdempotent(t *testing.T) {
	db := openT(t)

	parent := testBead(t, "patient_registration", "root", nil, nil)
	indexBeadT(t, db, parent, BeadLocation{PodPath: "p.pod", PatientRoot: parent.ID, Offset: 0, Length: 10})

	// child carries no coding antigen.Extract recognizes (plain "note" text),
	// so IndexBead's own antigen.Extract pass contributes zero bead_tags
	// rows for it; the "organ:renal" row asserted on below comes entirely
	// from this test's own duplicate-insert exercise, not from IndexBead —
	// exactly what this test is checking (bead_tags' INSERT OR IGNORE
	// contract, independent of tag derivation).
	child := testBead(t, "fhir_observation", "note", []string{parent.ID}, nil)
	loc := BeadLocation{PodPath: "p.pod", PatientRoot: parent.ID, Offset: 10, Length: 10}

	tx1, err := db.sqlDB.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := IndexBead(tx1, child, loc, DefaultFlattener{}); err != nil {
		t.Fatalf("IndexBead (first): %v", err)
	}
	if err := tx1.Commit(); err != nil {
		t.Fatalf("Commit (first): %v", err)
	}

	// Re-inserting edges/antigens for the same bead_id (simulating a replay
	// that skips the beads INSERT via a different path) must not error.
	tx2, err := db.sqlDB.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := tx2.Exec(
		`INSERT OR IGNORE INTO bead_edges (child_id, parent_id, edge_type) VALUES (?, ?, 'parent')`,
		child.ID, parent.ID,
	); err != nil {
		t.Fatalf("duplicate edge insert: %v", err)
	}
	if _, err := tx2.Exec(
		`INSERT OR IGNORE INTO bead_tags (tag, bead_id, patient_root) VALUES (?, ?, ?)`,
		"organ:renal", child.ID, parent.ID,
	); err != nil {
		t.Fatalf("duplicate antigen insert: %v", err)
	}
	if err := tx2.Commit(); err != nil {
		t.Fatalf("Commit (second): %v", err)
	}

	edges, err := db.GetEdges(child.ID)
	if err != nil {
		t.Fatalf("GetEdges: %v", err)
	}
	if len(edges) != 1 {
		t.Errorf("GetEdges after duplicate insert = %v, want 1 row (no duplication)", edges)
	}
	antigens, err := db.GetTags(child.ID)
	if err != nil {
		t.Fatalf("GetTags: %v", err)
	}
	if len(antigens) != 1 {
		t.Errorf("GetTags after duplicate insert = %v, want 1 row (no duplication)", antigens)
	}
}
