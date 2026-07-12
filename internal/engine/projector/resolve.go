package projector

import (
	"sort"

	"github.com/medbeads/medbeads/internal/engine/bead"
)

// resolveBead is one patient Bead plus the two pieces of information §2's
// resolution algorithm needs beyond what bead.Bead itself carries: its own
// recorded_at (the *write* instant — beads.recorded_at, Pod meta WrittenAt —
// as distinct from bead.Bead.Timestamp, the *clinical event* instant; see
// specs/U4_state_derivation.md's "穴2" fix) and RecordedAtValid (false for a
// pre-recorded_at-backfill row where the column is still NULL, per
// migrations/0006's comment on why it is nullable).
type resolveBead struct {
	Bead bead.Bead

	// Offset is the Bead's frame position within its patient's Pod — the byte
	// offset returned by pod.Store.Append. It IS the append order: a Pod is
	// append-only and no frame is ever moved, so a frame appended later has a
	// strictly greater offset. This is the ordering key for correction chains
	// (see beadOrderLess), and it is derivable from the Pods alone, so a reindex
	// reproduces it exactly.
	Offset int64

	// RecordedAt is the write instant (beads.recorded_at, Pod meta WrittenAt —
	// distinct from bead.Bead.Timestamp, the clinical event instant).
	//
	// It is kept for audit and display. It is NOT an ordering key: see
	// beadOrderLess for why ordering on it is unsafe.
	RecordedAt      string
	RecordedAtValid bool
}

// beadState is one Bead's §2-resolved outcome, ready to become a
// bead_status row (see buildBeadStatusRow in record_state.go).
//
// JUDGMENT CALL (not fully specified by specs/U4_state_derivation.md's §2
// text): an "unattested" Bead's own CurrentBeadID is set to itself (self-
// referencing), the same as a plain "active" fact with no correction. §2
// only says an unattested Y "does not supersede its target" (the target's
// own current_bead_id is unaffected by Y) — it does not separately define
// what Y's *own* current_bead_id should read. Self-reference is the most
// conservative choice: Y is not known-superseded by anything else, it is
// merely not-yet-validated, and U5's retrieve is what excludes unattested
// Beads from default result sets entirely (specs/DESIGN_v3.1_draft.md's
// "未承認 Bead は retrieve 既定で除外") — so this column is not load-bearing
// for U4/U5 correctness either way, only for a future audit query that might
// want "what would this unattested draft's current version be if it were
// later approved".
type beadState struct {
	Status            string // "active" | "amended" | "retracted" | "unattested"
	CurrentBeadID     string // "" means NULL (no current version — only the retracted case, and the retracted-leaf-of-a-chain case)
	SupersededBy      string // "" means NULL
	AttestationBeadID string // "" means NULL
	RetractionBeadID  string // "" means NULL
}

// resolvePatientState runs specs/U4_state_derivation.md's §2 three-step fixed
// -order pipeline (retracted mark -> attestation gate -> amends replace) over
// one patient's full Bead set, returning one beadState per Bead ID. It is a
// PURE function of beads (deterministic given the same input slice
// regardless of Go map iteration order — every internal iteration that could
// be order-sensitive sorts its keys first) and never reads wall-clock time.
//
// beads need not be pre-sorted by the caller; resolvePatientState sorts its
// own working copy by the §2 ordering key (see beadOrderLess) before running
// the three steps, so callers (record_state.go's per-patient loop) can hand
// it query results in whatever order SQL/ListPatientBeads happened to return
// them.
func resolvePatientState(beads []resolveBead) map[string]beadState {
	byID := make(map[string]resolveBead, len(beads))
	for _, rb := range beads {
		byID[rb.Bead.ID] = rb
	}

	ordered := append([]resolveBead(nil), beads...)
	sort.Slice(ordered, func(i, j int) bool {
		return beadOrderLess(ordered[i], ordered[j])
	})

	out := make(map[string]beadState, len(beads))
	for _, rb := range beads {
		out[rb.Bead.ID] = beadState{Status: "active", CurrentBeadID: rb.Bead.ID}
	}

	// --- step 1: retracted mark (strongest, first) -------------------------
	//
	// For every Bead R (in newest-first §2 order) that retracts some Bead X,
	// mark X retracted, keeping the first (= §2-newest = strongest) retainer R
	// we encounter for X — later (older, since we walk newest-first)
	// retraction Beads naming the same X are simply redundant and do not
	// overwrite the winner already recorded.
	retracted := make(map[string]bool, len(beads))
	for _, r := range ordered {
		for _, targetID := range r.Bead.Retracts {
			if _, known := byID[targetID]; !known {
				continue // target not in this patient's Bead set (should not happen; defensive)
			}
			if retracted[targetID] {
				continue // already retracted by a §2-newer R
			}
			retracted[targetID] = true
			st := out[targetID]
			st.Status = "retracted"
			st.RetractionBeadID = r.Bead.ID
			st.CurrentBeadID = ""
			out[targetID] = st
		}
	}

	// --- step 2: attestation gate ------------------------------------------
	//
	// validAttestation[Y] reports whether Y (an amends-carrying Bead, i.e.
	// len(Y.Amends) > 0, or a Type=="assessment" Bead) has an approved,
	// §2-newest-valid attestation naming it. Attestation Beads are collected
	// per target and resolved to "does the §2-newest one say approved" —
	// exactly the "より新しい verdict=='rejected' は無効化" rule: a later
	// (§2-newer) rejection overrides an earlier approval for the same target,
	// and vice versa, so only the single §2-newest attestation for a given
	// target matters, not an OR across all attestations ever made.
	latestAttestationVerdict := make(map[string]string, len(beads)) // target Bead ID -> "approved"|"rejected" of the §2-newest attestation naming it
	attestationBeadFor := make(map[string]string, len(beads))       // target Bead ID -> that §2-newest attestation Bead's own ID
	seenAttestationForTarget := make(map[string]bool, len(beads))
	for _, a := range ordered {
		if a.Bead.Type != "attestation" {
			continue
		}
		// NOTE ON AUTHORSHIP: an attestation SHOULD name its attester — an approval
		// nobody signed is not an approval — and create_bead now REFUSES to write a
		// correction Bead with an empty Author for exactly that reason (see
		// mcpserver/tools_write.go's requiresAuthor).
		//
		// That check deliberately lives at the WRITE boundary, not here. The fact
		// layer is append-only: a Bead already in a Pod cannot be edited or
		// withdrawn. If this projection began rejecting authorless attestations,
		// every approval written before the rule existed would silently evaporate on
		// the next reproject — a record a clinician really did approve would revert
		// to `unattested`, and the interpretation layer would be rewriting the
		// meaning of history instead of deriving it. Enforcing at the write boundary
		// keeps new corrections accountable while leaving the past faithfully
		// readable, which is the entire point of an immutable fact layer.
		verdict, _ := a.Bead.Content["verdict"].(string)
		for _, targetID := range a.Bead.Parents {
			if _, known := byID[targetID]; !known {
				continue
			}
			if seenAttestationForTarget[targetID] {
				continue // a §2-newer attestation for this target already won
			}
			seenAttestationForTarget[targetID] = true
			latestAttestationVerdict[targetID] = verdict
			attestationBeadFor[targetID] = a.Bead.ID
		}
	}

	requiresAttestation := func(rb resolveBead) bool {
		return len(rb.Bead.Amends) > 0 || rb.Bead.Type == "assessment"
	}

	validForGate := make(map[string]bool, len(beads)) // Bead ID -> passes the attestation gate (or does not need to)
	for _, rb := range beads {
		if !requiresAttestation(rb) {
			validForGate[rb.Bead.ID] = true
			continue
		}
		verdict, ok := latestAttestationVerdict[rb.Bead.ID]
		if ok && verdict == "approved" {
			validForGate[rb.Bead.ID] = true
			continue
		}
		validForGate[rb.Bead.ID] = false
		if !retracted[rb.Bead.ID] {
			st := out[rb.Bead.ID]
			st.Status = "unattested"
			st.CurrentBeadID = rb.Bead.ID
			if ok {
				st.AttestationBeadID = attestationBeadFor[rb.Bead.ID]
			}
			out[rb.Bead.ID] = st
		}
	}
	// Every Bead that DID pass the gate and carries an attestation still
	// records which attestation validated it (audit trail), even though its
	// status is not (yet) "unattested" — set here rather than inside the
	// validForGate loop above so both branches populate AttestationBeadID
	// uniformly.
	for targetID, attID := range attestationBeadFor {
		if latestAttestationVerdict[targetID] != "approved" {
			continue
		}
		st := out[targetID]
		st.AttestationBeadID = attID
		out[targetID] = st
	}

	// --- step 3: amends supersession (valid, non-retracted amenders only) --
	//
	// amendersOf[X] = every Bead Y such that X ∈ Y.Amends, i.e. Y corrects X —
	// a forward edge from the target to its amenders (potentially more than
	// one Y naming the same X, e.g. two independent corrections; §2's
	// ordering key picks the winner deterministically, exactly as step 1's
	// retraction winner is picked).
	amendersOf := make(map[string][]resolveBead, len(beads))
	for _, rb := range beads {
		for _, targetID := range rb.Bead.Amends {
			if _, known := byID[targetID]; !known {
				continue
			}
			amendersOf[targetID] = append(amendersOf[targetID], rb)
		}
	}
	for target := range amendersOf {
		sort.Slice(amendersOf[target], func(i, j int) bool {
			return beadOrderLess(amendersOf[target][i], amendersOf[target][j])
		})
	}

	// winningAmender picks the §2-newest amender of targetID that is VALID
	// (passed the attestation gate, step 2) — an unattested (invalid)
	// candidate amender is skipped as if it were never proposed (the search
	// continues to the next-newest candidate, so an unattested amender never
	// blocks an OLDER, otherwise-valid amender from taking effect). A
	// candidate that is itself RETRACTED is NOT skipped here: retraction
	// happens to the amender's own Bead identity after it already validly
	// superseded its target (or independently of superseding anything) — §2's
	// "leaf が後で retracted なら current_bead_id=NULL" rule is exactly this
	// case, and it must still terminate the chain at that (now-retracted)
	// leaf rather than silently falling back to an older, non-retracted
	// amender or to the original target itself, which would incorrectly
	// revive a correction chain that the retraction was specifically meant to
	// end. The retracted-ness of the winner is surfaced via the second return
	// value so callers (chainLeaf) can stop walking past it.
	winningAmender := func(targetID string) (resolveBead, bool) {
		for _, cand := range amendersOf[targetID] {
			if !validForGate[cand.Bead.ID] {
				continue
			}
			return cand, true
		}
		return resolveBead{}, false
	}

	// chainLeaf walks X -> its winning amender -> that amender's winning
	// amender -> ... until either no further valid amender exists, or the
	// current node is itself retracted (a retracted node terminates the walk
	// immediately: §2's "以後 X への amends は無効" means a retracted Bead's
	// own further amenders, if any existed, are moot — the chain's resolved
	// value is NULL from that point regardless). Returns the final leaf Bead
	// ID (possibly = startID itself) and whether that leaf is retracted.
	// visited guards against a pathological cycle (structurally should not
	// occur — see bead.Bead's doc comment on amends/retracts acyclicity by
	// construction — but a defensive bound keeps this total rather than
	// looping forever if that invariant were ever violated).
	chainLeaf := func(startID string) (leafID string, leafRetracted bool) {
		visited := make(map[string]bool, len(beads))
		cur := startID
		for {
			if visited[cur] {
				return cur, retracted[cur] // defensive cycle guard
			}
			visited[cur] = true
			if retracted[cur] {
				return cur, true
			}
			next, ok := winningAmender(cur)
			if !ok {
				return cur, false
			}
			cur = next.Bead.ID
		}
	}

	for _, rb := range beads {
		id := rb.Bead.ID
		if retracted[id] {
			continue // step 1 already finalized this Bead; amends never revive a retracted target
		}
		if !requiresAttestation(rb) && len(amendersOf[id]) == 0 {
			continue // plain fact with no correction: already "active"/self, from initialization
		}
		if out[id].Status == "unattested" {
			// An unattested Bead does not supersede ITS OWN amends target
			// (handled from the target's perspective below via
			// winningAmender simply skipping it), but it is itself still
			// "unattested" (step 2 already set that), not touched further
			// here.
			continue
		}

		winner, ok := winningAmender(id)
		if !ok {
			continue // no valid amender: stays active/self
		}
		leafID, leafRetracted := chainLeaf(id)
		st := out[id]
		st.Status = "amended"
		st.SupersededBy = winner.Bead.ID
		if leafRetracted {
			st.CurrentBeadID = ""
		} else {
			st.CurrentBeadID = leafID
		}
		out[id] = st
	}

	return out
}

// beadOrderLess implements specs/U4_state_derivation.md's §2 ordering key,
// "newest first" — where newest means LAST APPENDED: Offset DESC, id DESC.
//
// # Why append order, and not recorded_at
//
// This comparator used to order on recorded_at as a STRING, on the stated
// assumption that "RFC3339 timestamps compare correctly as strings". That
// assumption is false for the format this system actually writes, and the
// resulting defect is a clinical one.
//
// pod/record.go writes recorded_at with time.RFC3339Nano, which OMITS TRAILING
// ZEROS in the fractional second. Two Beads written 8ms apart serialize as:
//
//	appended first : "2026-07-11T13:51:46.89Z"       (i.e. .890000)
//	appended second: "2026-07-11T13:51:46.897924Z"
//
// Chronologically the second is later. Lexicographically the FIRST is greater,
// because 'Z' (0x5A) sorts above '7' (0x37). A string compare therefore names
// the earlier-appended Bead as the newer one. 208 such inverted pairs exist in
// the production store. Applied to a correction chain this silently makes a
// SUPERSEDED amendment the patient's current record — deterministically, with no
// clock anomaly required.
//
// Parsing recorded_at into a time.Time would fix that inversion while keeping a
// wall-clock dependency the system does not need: time.Now() is not monotonic
// across NTP correction, VM suspend, or container migration.
//
// The append-only log already carries the order this code is trying to recover.
// Offset IS that order: pod.Store.Append appends and returns the frame's start
// offset, no frame is ever moved or rewritten (there is no compaction path in
// internal/engine/pod), and reindex re-derives the same offsets from the Pods.
// So ordering on Offset is correct by construction, independent of any clock,
// and reproducible from the fact layer alone — which the two-layer invariant
// requires.
//
// Correction chains never cross Pods: Engine.Ingest resolves patient_root from
// Parents and rejects cross-patient corrections, and create_bead independently
// validates that every amends/retracts target shares the new Bead's
// patient_root. One patient's Beads live in one Pod, so a bare Offset is a total
// order over any set of Beads that can actually compete in one resolution.
//
// pod_id is deliberately NOT part of the key: it is a SQLite surrogate assigned
// by RegisterPod in index-registration order, i.e. index state rather than
// fact-layer state, and it is not a stable ordering axis across a reindex.
//
// recorded_at is retained on resolveBead for audit and display. It must not be
// used to order.
//
// This is also deliberately NOT bead.Bead.Timestamp (the clinical event time) —
// see specs/U4_state_derivation.md's "穴2" fix and resolve_test.go's fixture,
// which fails if someone sorts by Timestamp instead.
func beadOrderLess(a, b resolveBead) bool {
	if a.Offset != b.Offset {
		return a.Offset > b.Offset // later append == newer
	}
	// Unreachable for two distinct Beads in one Pod (a frame's offset is unique),
	// but keeps the order total and deterministic regardless.
	return a.Bead.ID > b.Bead.ID
}
