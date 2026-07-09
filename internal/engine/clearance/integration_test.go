package clearance_test

import (
	"testing"

	"github.com/medbeads/medbeads/internal/engine/bead"
	"github.com/medbeads/medbeads/internal/engine/clearance"
	"github.com/medbeads/medbeads/internal/engine/graph"
	"github.com/medbeads/medbeads/internal/engine/pod"
)

// TestFilterByAccess_EmbeddedClearance_RoundTrip exercises the layer v2 had
// no equivalent of: a Bead's own embedded bead.Clearance overlay (rather
// than a DB clearance_rules row) restricting access, read back through a
// real engine.Ingest -> engine.GetBead round trip.
//
// # Persistence design (lead ruling)
//
// Clearance/Signature are excluded from the content hash (bead.Canonicalize)
// and therefore never appear in a frame's core_bytes, but they are not
// lost: pod.Writer.Append copies them into the frame's meta_bytes
// (pod.Meta.Clearance/Signature — see pod/record.go's doc comment), which
// is exactly the "minimal derived info, outside the hash" payload
// specs/DESIGN_v3.md §3 designed meta_bytes to hold. Every decode path
// (engine.decodeBeadRecord, graph.decodeBundleRecord, index.decodeRecordBead)
// restores them from rec.Meta. Because Pod is append-only, this makes the
// embedded layer create-time-fixed: changing it after the fact is not
// possible (there is no in-place frame edit), which is why ongoing/mutable
// access changes are the DB clearance_rules layer's job instead (see
// TestFilterByAccess_EmbeddedAndDBRule_BothApply) — see doc.go's "two
// layers" section for the full division of responsibility.
func TestFilterByAccess_EmbeddedClearance_RoundTrip(t *testing.T) {
	e := openT(t)

	patient := seedPatient(t, e, "Patient")

	restricted, err := e.Ingest(bead.Bead{
		Type:      "fhir_condition",
		Timestamp: nextTimestamp(),
		Parents:   []string{patient.ID},
		Content:   map[string]any{"diagnosis": "psychiatric evaluation"},
		Clearance: &bead.Clearance{DeniedRoles: []string{"insurance"}},
	})
	if err != nil {
		t.Fatalf("Ingest restricted bead: %v", err)
	}

	// Read back through the engine (Pod encode/decode round trip) rather
	// than reusing the in-memory `restricted` value, so this exercises the
	// embedded Clearance actually surviving storage, not just being present
	// on the value this test happened to construct.
	reloaded, err := e.GetBead(restricted.ID)
	if err != nil {
		t.Fatalf("GetBead: %v", err)
	}
	if reloaded.Clearance == nil || len(reloaded.Clearance.DeniedRoles) != 1 || reloaded.Clearance.DeniedRoles[0] != "insurance" {
		t.Fatalf("reloaded bead lost its embedded Clearance: %+v", reloaded.Clearance)
	}

	filtered, err := clearance.FilterByAccess(e.Index(), []bead.Bead{reloaded}, []string{"insurance"})
	if err != nil {
		t.Fatalf("FilterByAccess: %v", err)
	}
	if filtered[0].Content["_restricted"] != true {
		t.Errorf("insurance role should be masked by the embedded Clearance, got content = %v", filtered[0].Content)
	}

	allowed, err := clearance.HasAccess(e.Index(), reloaded, []string{"primary_care"})
	if err != nil {
		t.Fatalf("HasAccess: %v", err)
	}
	if !allowed {
		t.Error("primary_care should have access; only insurance is denied by the embedded Clearance")
	}
}

// TestGetBead_PersistsEmbeddedClearance is a named regression pin (the
// positive counterpart of what used to be
// TestGetBead_DoesNotPersistEmbeddedClearance, before the lead ruling that
// meta_bytes is Clearance/Signature's designed storage location — see
// pod/record.go's Meta doc comment): if a future change to pod/engine ever
// stops persisting Clearance, this test fails loudly rather than the
// regression being silently reintroduced.
func TestGetBead_PersistsEmbeddedClearance(t *testing.T) {
	e := openT(t)
	patient := seedPatient(t, e, "Patient")

	restricted, err := e.Ingest(bead.Bead{
		Type:      "fhir_condition",
		Timestamp: nextTimestamp(),
		Parents:   []string{patient.ID},
		Content:   map[string]any{"diagnosis": "x"},
		Clearance: &bead.Clearance{DeniedRoles: []string{"insurance"}, Reason: "psych eval"},
	})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if restricted.Clearance == nil {
		t.Fatalf("Ingest's own return value should still carry Clearance in memory")
	}

	reloaded, err := e.GetBead(restricted.ID)
	if err != nil {
		t.Fatalf("GetBead: %v", err)
	}
	if reloaded.Clearance == nil {
		t.Fatal("GetBead lost the embedded Clearance across the Pod round trip")
	}
	if len(reloaded.Clearance.DeniedRoles) != 1 || reloaded.Clearance.DeniedRoles[0] != "insurance" {
		t.Errorf("DeniedRoles round-trip = %v, want [insurance]", reloaded.Clearance.DeniedRoles)
	}
	if reloaded.Clearance.Reason != "psych eval" {
		t.Errorf("Reason round-trip = %q, want %q", reloaded.Clearance.Reason, "psych eval")
	}

	// ID must be unaffected: Clearance is hash-excluded (bead package's own
	// TestComputeID_ExcludesClearanceAndSignature / TestVerify_
	// ClearanceSignatureDoNotAffectVerify already cover this invariant at
	// the bead.Bead level; this just confirms it still holds end to end
	// through the real engine.Ingest/GetBead path, not only in isolation).
	if reloaded.ID != restricted.ID {
		t.Errorf("reloaded bead has a different ID: got %s, want %s", reloaded.ID, restricted.ID)
	}
	if err := bead.Verify(reloaded); err != nil {
		t.Errorf("Verify(reloaded): %v", err)
	}
}

// TestPod_RoundTrip_PersistsClearanceAndSignature is the pod-layer half of
// the same guarantee (engine-layer coverage is
// TestGetBead_PersistsEmbeddedClearance above): a Bead with no Clearance at
// all must decode back with a nil Clearance (the "old frame, Meta.Clearance
// absent" case the lead ruling calls out) without panicking, and a Bead
// with both Clearance and Signature set must round-trip both.
func TestPod_RoundTrip_PersistsClearanceAndSignature(t *testing.T) {
	e := openT(t)
	patient := seedPatient(t, e, "Patient")

	// No Clearance/Signature at all (the common case, and the "nil Meta
	// field" case that must not panic on decode).
	plain := seedChildBead(t, e, patient, "fhir_observation", nil, map[string]any{"value": "normal"})
	reloadedPlain, err := e.GetBead(plain.ID)
	if err != nil {
		t.Fatalf("GetBead(plain): %v", err)
	}
	if reloadedPlain.Clearance != nil {
		t.Errorf("plain bead should round-trip with a nil Clearance, got %+v", reloadedPlain.Clearance)
	}
	if reloadedPlain.Signature != "" {
		t.Errorf("plain bead should round-trip with an empty Signature, got %q", reloadedPlain.Signature)
	}

	// Both Clearance and Signature set.
	signed, err := e.Ingest(bead.Bead{
		Type:      "fhir_condition",
		Timestamp: nextTimestamp(),
		Parents:   []string{patient.ID},
		Content:   map[string]any{"diagnosis": "y"},
		Clearance: &bead.Clearance{AllowedRoles: []string{"dept:genetics"}},
		Signature: "base64:deadbeef==",
	})
	if err != nil {
		t.Fatalf("Ingest(signed): %v", err)
	}
	reloadedSigned, err := e.GetBead(signed.ID)
	if err != nil {
		t.Fatalf("GetBead(signed): %v", err)
	}
	if reloadedSigned.Clearance == nil || len(reloadedSigned.Clearance.AllowedRoles) != 1 || reloadedSigned.Clearance.AllowedRoles[0] != "dept:genetics" {
		t.Errorf("AllowedRoles round-trip = %+v, want [dept:genetics]", reloadedSigned.Clearance)
	}
	if reloadedSigned.Signature != "base64:deadbeef==" {
		t.Errorf("Signature round-trip = %q, want %q", reloadedSigned.Signature, "base64:deadbeef==")
	}
}

// TestFilterByAccess_EmbeddedAndDBRule_BothApply confirms the two-layer
// model (doc.go): an embedded bead.Clearance and a DB clearance_rules row on
// the same Bead are combined as independent constraints — a viewer must
// satisfy both, not just one. Uses a real GetBead round trip for the
// embedded layer now that it persists (see
// TestFilterByAccess_EmbeddedClearance_RoundTrip).
func TestFilterByAccess_EmbeddedAndDBRule_BothApply(t *testing.T) {
	e := openT(t)

	patient := seedPatient(t, e, "Patient")

	// Embedded layer denies "insurance"; DB layer separately denies
	// "family". A viewer must clear both layers.
	restricted, err := e.Ingest(bead.Bead{
		Type:      "fhir_condition",
		Timestamp: nextTimestamp(),
		Parents:   []string{patient.ID},
		Content:   map[string]any{"diagnosis": "x"},
		Clearance: &bead.Clearance{DeniedRoles: []string{"insurance"}},
	})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	seedClearanceRule(t, e, restricted.ID, []string{"family"}, nil)

	reloaded, err := e.GetBead(restricted.ID)
	if err != nil {
		t.Fatalf("GetBead: %v", err)
	}

	cases := []struct {
		role string
		want bool
	}{
		{"insurance", false}, // blocked by embedded layer
		{"family", false},    // blocked by DB layer
		{"primary_care", true},
		{"system", true}, // system always bypasses both layers
	}
	for _, tc := range cases {
		got, err := clearance.HasAccess(e.Index(), reloaded, []string{tc.role})
		if err != nil {
			t.Fatalf("HasAccess(%s): %v", tc.role, err)
		}
		if got != tc.want {
			t.Errorf("HasAccess(role=%s) = %v, want %v", tc.role, got, tc.want)
		}
	}
}

// TestFilterByAccess_MultiViewer_SameBeadSet checks the "Filter(beads,
// viewer) as a per-viewer view" round trip docs/requirements.md's testing
// task calls for directly: the same Bead set, filtered for two different
// viewers, yields two different visible sets from one shared DB rule.
func TestFilterByAccess_MultiViewer_SameBeadSet(t *testing.T) {
	e := openT(t)

	patient := seedPatient(t, e, "Patient")
	open := seedChildBead(t, e, patient, "fhir_observation", nil, map[string]any{"value": "normal"})
	restricted := seedChildBead(t, e, patient, "fhir_condition", nil, map[string]any{"diagnosis": "restricted"})
	seedClearanceRule(t, e, restricted.ID, []string{"insurance", "researcher"}, nil)

	beads := []bead.Bead{open, restricted}

	forInsurance, err := clearance.FilterByAccess(e.Index(), beads, []string{"insurance"})
	if err != nil {
		t.Fatalf("FilterByAccess(insurance): %v", err)
	}
	if forInsurance[0].Content["value"] != "normal" {
		t.Errorf("insurance viewer should see the open bead unmasked: %v", forInsurance[0].Content)
	}
	if forInsurance[1].Content["_restricted"] != true {
		t.Errorf("insurance viewer should have the restricted bead masked: %v", forInsurance[1].Content)
	}

	forPrimaryCare, err := clearance.FilterByAccess(e.Index(), beads, []string{"primary_care"})
	if err != nil {
		t.Fatalf("FilterByAccess(primary_care): %v", err)
	}
	if forPrimaryCare[1].Content["diagnosis"] != "restricted" {
		t.Errorf("primary_care viewer should see the restricted bead's content unmasked: %v", forPrimaryCare[1].Content)
	}
}

// TestFilterByAccess_ViaLoadBundle confirms FilterByAccess works the same
// way whether its input Beads came from engine.GetBead (per-Bead random
// read) or graph.LoadBundle (one sequential Pod scan — specs/DESIGN_v3.md
// §3's patient-bundle read path): both decode paths restore Clearance from
// rec.Meta (see graph.decodeBundleRecord), so a Bundle-sourced Bead's
// embedded Clearance is enforced identically.
func TestFilterByAccess_ViaLoadBundle(t *testing.T) {
	e := openT(t)

	patient := seedPatient(t, e, "Patient")
	_, err := e.Ingest(bead.Bead{
		Type:      "fhir_condition",
		Timestamp: nextTimestamp(),
		Parents:   []string{patient.ID},
		Content:   map[string]any{"diagnosis": "psychiatric evaluation"},
		Clearance: &bead.Clearance{DeniedRoles: []string{"insurance"}},
	})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	store := pod.NewStore(e.DataDir())
	bundle, err := graph.LoadBundle(store, patient.ID)
	if err != nil {
		t.Fatalf("LoadBundle: %v", err)
	}

	// Descendants(patient.ID, bundle.Beads()) returns patient.ID itself plus
	// every Bead reachable from it within bundle.Beads() child-hops — an
	// upper bound on depth that is guaranteed to reach every Bead in this
	// small, single-branch test bundle, giving a full Bundle-sourced Bead
	// list via Bundle's existing public API (Bundle has no direct "list
	// every Bead" method, and adding one is out of this unit's scope).
	beads := bundle.Descendants(patient.ID, bundle.Beads())

	filtered, err := clearance.FilterByAccess(e.Index(), beads, []string{"insurance"})
	if err != nil {
		t.Fatalf("FilterByAccess: %v", err)
	}

	maskedCount := 0
	for _, b := range filtered {
		if b.Content["_restricted"] == true {
			maskedCount++
		}
	}
	if maskedCount != 1 {
		t.Errorf("expected exactly 1 masked bead from a Bundle-sourced Filter, got %d (of %d beads)", maskedCount, len(filtered))
	}
}
