package engine

import (
	"testing"

	"github.com/medbeads/medbeads/internal/engine/bead"
)

// openT opens a fresh Engine under a t.TempDir() and registers a Close
// cleanup, for tests that only need a working, empty Engine.
func openT(t *testing.T) *Engine {
	t.Helper()
	dir := t.TempDir()
	e, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	return e
}

// unsavedBead returns an ID-less Bead with the given type/parents/content,
// for tests that want to exercise Ingest's own ID-assignment path
// (verifyOrAssignID). Timestamps increase per call (via a package-level
// counter) so ListPatientBeads ordering is deterministic and distinct Beads
// with otherwise-identical content still get distinct content hashes.
//
// No antigens parameter: v3.1 removed Bead.Antigens entirely (tag
// derivation now happens only at index-projection time, from Type+Content —
// see antigen.Extract / index.IndexBead). No test in this package asserts on
// bead_tags content, so there is nothing for a caller here to need to
// control.
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

var timestampCounter int

// nextTimestamp returns a strictly increasing RFC3339 timestamp string across
// the whole test binary run, so Beads built by different test functions
// never collide and ListPatientBeads' timestamp-ordering is predictable
// within a single test.
func nextTimestamp() string {
	timestampCounter++
	// 2026-01-01T00:00:00Z plus timestampCounter seconds, formatted by hand
	// to avoid importing "time" just for this; well within a plausible
	// range for all tests in this package (well under 86400 calls/test run).
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
