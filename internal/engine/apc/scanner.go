package apc

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/medbeads/medbeads/internal/engine/bead"
	"github.com/medbeads/medbeads/internal/engine/index"
)

// ingester is the subset of *engine.Engine the Scanner needs: writing new
// Beads (sibling_link generation) plus reading one back for generation
// tracking. Package apc depends only on this interface (and *index.DB
// directly for the read-heavy candidate queries IndexBead's own API does
// not expose), not on package engine itself — mirroring package graph's
// "does not import engine" convention (see graph/doc.go) for the same
// reason: apc is a sibling of engine/pod, engine/index, engine/graph under
// internal/engine/, and it is engine's job (a later unit, or cmd/medbeadsd)
// to wire a *Scanner on top of its own *engine.Engine, not apc's job to
// import engine.
type ingester interface {
	Ingest(b bead.Bead) (bead.Bead, error)
	GetBead(id string) (bead.Bead, error)
}

// Scanner is the APC batch scanner (specs/DESIGN_v3.md §7,
// docs/requirements.md R5): given an ingester and direct SQL access to
// index.db (via idx), Scan finds newly-ingested Beads, matches them against
// already-scanned Beads in the same patient by shared antigen, scores each
// candidate pair, and ingests a sibling_link Bead (via engine.Ingest, so it
// is itself a hash-verified, tamper-evident Bead — DESIGN §7's whole point)
// for every pair clearing the score threshold.
type Scanner struct {
	engine ingester
	idx    *index.DB
	cfg    Config
}

// New returns a Scanner over engine (for Ingest/GetBead) and idx (for the
// direct SQL queries Scan needs: bead_apc_scan watermark, bead_antigens
// lookups, sibling_pairs de-duplication). Both should point at the same
// data directory as the same *engine.Engine idx was obtained from
// (engine.Engine.Index()) — Scan does not itself verify this.
func New(engine ingester, idx *index.DB, cfg Config) *Scanner {
	return &Scanner{engine: engine, idx: idx, cfg: cfg}
}

// Result summarizes one Scan call's effect, for callers (cmd/medbeadsd, MCP
// apc_trigger/apc_status, tests) that want a count rather than re-querying
// bead_apc_scan themselves.
type Result struct {
	// BeadsScanned is how many not-yet-watermarked Beads this call examined.
	BeadsScanned int
	// SiblingLinksCreated is how many new sibling_link Beads this call
	// ingested.
	SiblingLinksCreated int
}

// Scan is the batch entry point (v3.0 is batch-only per DESIGN §7; no
// resident goroutine is started here — a caller wanting periodic scanning
// invokes Scan on its own schedule, e.g. after each ingest batch or on a
// cmd/medbeadsd timer, which is out of this package's scope).
//
// # Incremental scope (bead_apc_scan watermark)
//
// Scan only considers Beads with no bead_apc_scan row yet ("new Bead") as
// scan *anchors*, matched against every already-scanned Bead in the same
// patient ("患者内スキャン済み" — see candidatesFor) — this is the
// incremental scope DESIGN §7 calls for: "新Bead vs 患者内スキャン済みのみ照合".
// An already-scanned Bead is never re-used as an anchor on a later Scan
// call, so a stable set of Beads is never re-compared against itself
// (idempotent: a second Scan call with no new Beads examines zero anchors
// and creates zero links). Every Bead this call examines (anchor or
// candidate) is marked scanned (a bead_apc_scan row inserted/updated) before
// Scan returns, whether or not it produced any sibling_link — so a Bead with
// zero matches is never re-examined as an anchor either.
//
// # Runaway prevention (docs/requirements.md R5.2 / DESIGN §7)
//
//  1. sibling_pairs UNIQUE(bead_a, bead_b, matched_antigen): enforced by the
//     schema (migrations/0002_apc.sql); Scan additionally pre-checks this
//     table before scoring a pair to skip work, but the UNIQUE constraint is
//     what makes this hold even if that pre-check is ever bypassed.
//  2. Config.MaxSiblingsPerBead: a Bead already at this count (per its
//     bead_apc_scan.sibling_count) is never scored as a match target again.
//  3. Config.MaxGeneration + Config.GenerationDecay: a Bead whose own
//     scan_generation is already >= MaxGeneration is skipped as a candidate
//     entirely (SIBLING_SPEC §6.5 max_sibling_depth); an eligible pair's
//     score is decayed by GenerationDecay^generation, generation being the
//     higher of the two Beads' scan_generation (see scorePair).
//  4. Config.AntigenFrequencyThreshold: an antigen present on more than this
//     fraction of the patient's Beads is dropped from the candidate-search
//     and scoring antigen set entirely (the IDF filter — see
//     candidatesFor's frequentAntigens computation).
//  5. Config.MaxSiblingLinksPerPatientPerScan: once a patient hits this many
//     new sibling_link Beads within this single Scan call, no further links
//     are generated for that patient for the remainder of the call (other
//     patients are unaffected; already-visited Beads are still marked
//     scanned per the incremental-scope guarantee above, so the next Scan
//     call does not re-visit them and cannot re-attempt the links this call
//     skipped — RescanPatient is the explicit, deliberate way to force
//     re-matching for a patient).
func (s *Scanner) Scan() (Result, error) {
	anchors, err := s.unscannedBeads()
	if err != nil {
		return Result{}, fmt.Errorf("apc: scan: %w", err)
	}

	// Mark every anchor in this batch scanned up front (bead_apc_scan row
	// inserted, count 0), before matching any of them against candidates.
	// This is what makes intra-batch matching complete: candidatesFor's
	// query requires a candidate to already have a bead_apc_scan row (the
	// "患者内スキャン済み" half of DESIGN §7's "新Bead vs 患者内スキャン済みの
	// み照合" rule) — if that row were only created as each anchor's own
	// scanOne ran (i.e. one at a time, interleaved with matching), two
	// Beads freshly ingested in the same batch would only match each other
	// in the direction determined by which one happened to sort earlier in
	// unscannedBeads' ORDER BY b.id, silently missing the other direction's
	// pair until some future Scan call incidentally revisited it — which
	// never happens, since watermarking means neither Bead is ever an
	// anchor again. Pre-marking every anchor scanned first makes every
	// anchor visible as a candidate to every other anchor in the same
	// batch, symmetrically, so a single Scan call converges every matching
	// pair within its own batch, not just an arbitrary subset of them
	// determined by ID sort order.
	for _, anchor := range anchors {
		anchorGeneration, err := s.beadGeneration(anchor)
		if err != nil {
			return Result{}, fmt.Errorf("apc: scan: %w", err)
		}
		if err := s.markScanned(anchor.ID, 0, anchorGeneration); err != nil {
			return Result{}, fmt.Errorf("apc: scan: %w", err)
		}
	}

	var res Result
	linksThisPatientScan := make(map[string]int)

	for _, anchor := range anchors {
		res.BeadsScanned++

		created, err := s.scanOne(anchor, linksThisPatientScan)
		if err != nil {
			return res, fmt.Errorf("apc: scan %s: %w", anchor.ID, err)
		}
		res.SiblingLinksCreated += created
	}

	return res, nil
}

// scanOne matches one anchor Bead against every eligible already-scanned
// Bead sharing a patient and an antigen, ingests a sibling_link for each
// match clearing threshold (subject to the rate limit in
// linksThisPatientScan). Every anchor in this Scan() call was already
// marked scanned by Scan itself (a bead_apc_scan row inserted up front, see
// Scan's doc comment for why this must happen before any anchor is matched
// — it is what makes intra-batch matching symmetric), so scanOne only reads
// that state back (scanState) rather than writing it.
//
// anchorGeneration is anchor's own generation in the sibling_link chain: 0
// for an ordinary Bead, or content.scan_generation for a Bead of type
// "sibling_link" (a Bead created by a prior Scan call, now itself up for
// scanning as a new anchor — this is how "二次応答" / a link-of-a-link
// becomes possible at all, per SIBLING_SPEC §4.5/§12). Runaway prevention c
// is enforced here: an anchor already at MaxGeneration produces no further
// links (its own generation cannot be exceeded by scoring against a
// candidate — see the per-candidate generation check below, which folds in
// anchorGeneration either way).
func (s *Scanner) scanOne(anchor scannedBeadRef, linksThisPatientScan map[string]int) (int, error) {
	anchorSiblingCount, anchorGeneration, err := s.scanState(anchor.ID)
	if err != nil {
		return 0, err
	}

	if anchor.PatientRoot == "" {
		// Sibling matching is patient-scoped by design (SIBLING_SPEC §6.4:
		// "bead_A.patient == bead_B.patient") — a shared-Pod Bead (no single
		// patient_root) has no patient to match within.
		return 0, nil
	}
	if anchorGeneration >= s.cfg.MaxGeneration {
		// Runaway prevention c: this anchor is already at (or past) the
		// max sibling-chain depth — it may still exist and be readable, but
		// generates no further sibling_link Beads from here.
		return 0, nil
	}

	candidates, err := s.candidatesFor(anchor)
	if err != nil {
		return 0, err
	}

	created := 0
	for _, cand := range candidates {
		if linksThisPatientScan[anchor.PatientRoot] >= s.cfg.MaxSiblingLinksPerPatientPerScan {
			break // runaway prevention e: per-patient/per-scan rate limit reached
		}
		if anchorSiblingCount+created >= s.cfg.MaxSiblingsPerBead {
			break // runaway prevention b: anchor already at its sibling cap
		}

		candSiblingCount, candGeneration, err := s.scanState(cand.id)
		if err != nil {
			return created, err
		}
		if candSiblingCount >= s.cfg.MaxSiblingsPerBead {
			continue // runaway prevention b: candidate already at its sibling cap
		}

		pairGeneration := anchorGeneration
		if candGeneration > pairGeneration {
			pairGeneration = candGeneration
		}
		if pairGeneration >= s.cfg.MaxGeneration {
			// Runaway prevention c: the resulting link would be
			// pairGeneration+1, which must stay <= MaxGeneration.
			continue
		}

		_, madeLink, err := s.tryLink(anchor, cand, pairGeneration)
		if err != nil {
			return created, err
		}
		if !madeLink {
			continue
		}
		created++
		linksThisPatientScan[anchor.PatientRoot]++
		if err := s.bumpSiblingCount(cand.id, 1); err != nil {
			return created, err
		}
	}

	if created > 0 {
		if err := s.bumpSiblingCount(anchor.ID, created); err != nil {
			return created, err
		}
	}

	return created, nil
}

// beadGeneration returns anchor's own place in the sibling-link chain: 0 for
// any ordinary Bead, or content.scan_generation for a Bead of type
// "sibling_link" (read back via GetBead, since scannedBeadRef only carries
// type/timestamp, not content). A sibling_link Bead with a missing or
// non-numeric scan_generation field (should not happen for a Bead this
// scanner itself built — buildSiblingLinkBead always sets it — but a
// manually- or agent-created sibling_link Bead is possible) is treated as
// generation 0 rather than erroring: failing scan-generation bookkeeping
// safe (falling back to "treat as first-generation", the more permissive
// but still spec-compliant reading) is preferable to Scan aborting entirely
// over one malformed Bead's content.
func (s *Scanner) beadGeneration(anchor scannedBeadRef) (int, error) {
	if anchor.Type != "sibling_link" {
		return 0, nil
	}
	b, err := s.engine.GetBead(anchor.ID)
	if err != nil {
		return 0, fmt.Errorf("bead generation %s: %w", anchor.ID, err)
	}
	switch g := b.Content["scan_generation"].(type) {
	case float64:
		return int(g), nil
	case int:
		return g, nil
	default:
		return 0, nil
	}
}

// candidate is one already-scanned Bead being considered as a sibling match
// for an anchor, plus the (already IDF-filtered) antigens it shares with the
// anchor.
type candidate struct {
	id        string
	beadType  string
	timestamp string
	shared    []string
}

// candidatesFor finds every already-scanned Bead in anchor's patient sharing
// at least one antigen with it, after dropping any antigen at or above
// Config.AntigenFrequencyThreshold's patient-local frequency (runaway
// prevention d — the IDF filter: DESIGN §7 point 4, "患者内出現率が閾値超の
// antigen はトリガーから除外"). Antigen frequency is computed over the
// patient's distinct Beads that carry at least one antigen (bead_antigens
// rows), not over all of the patient's Beads regardless of whether they
// carry antigens — a patient whose antigen-bearing Beads are mostly of one
// kind should not have that kind's antigens under-counted by diluting the
// denominator with untagged Beads.
func (s *Scanner) candidatesFor(anchor scannedBeadRef) ([]candidate, error) {
	anchorAntigens, err := s.idx.GetAntigens(anchor.ID)
	if err != nil {
		return nil, fmt.Errorf("get antigens for %s: %w", anchor.ID, err)
	}
	if len(anchorAntigens) == 0 {
		return nil, nil
	}

	frequent, err := s.frequentAntigens(anchor.PatientRoot)
	if err != nil {
		return nil, err
	}

	triggerAntigens := make([]string, 0, len(anchorAntigens))
	for _, ag := range anchorAntigens {
		if frequent[ag] {
			continue
		}
		triggerAntigens = append(triggerAntigens, ag)
	}
	if len(triggerAntigens) == 0 {
		return nil, nil
	}

	rows, err := s.candidateRows(anchor, triggerAntigens)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// candidateRows runs the actual bead_antigens x beads join: every Bead in
// anchor.PatientRoot, other than anchor itself, that shares at least one of
// triggerAntigens with it and has already been scanned (bead_apc_scan
// exists) — "新Bead vs 患者内スキャン済み" (DESIGN §7). A candidate of
// type='sibling_link' is deliberately excluded here too — not because it
// cannot legitimately be an anchor (it can: scanOne/beadGeneration is exactly
// how "二次応答" scans a sibling_link Bead as an anchor against ordinary
// Beads), but because letting it also appear on the *other* side, as a
// same-batch candidate matched purely on the antigens it copied from its own
// parents (buildSiblingLinkBead sets a sibling_link's Antigens to the
// matched-antigen set verbatim), would double-count that overlap: the
// sibling_link Bead and (at least) one of its own parents would both surface
// as separate "matches" for the very same underlying antigen fact, which is
// redundant rather than new evidence, and would materially inflate this
// path's link-generation volume with no corresponding scoring gain (see
// frequentAntigens' identical exclusion for the IDF-self-contamination half
// of this same concern). Results are grouped by candidate Bead ID with their
// shared-antigen set, ordered by ID for deterministic Scan output.
func (s *Scanner) candidateRows(anchor scannedBeadRef, triggerAntigens []string) ([]candidate, error) {
	placeholders := make([]string, len(triggerAntigens))
	args := make([]any, 0, len(triggerAntigens)+2)
	for i, ag := range triggerAntigens {
		placeholders[i] = "?"
		args = append(args, ag)
	}
	args = append(args, anchor.PatientRoot, anchor.ID)

	query := fmt.Sprintf(`
		SELECT ba.bead_id, ba.antigen, b.type, b.timestamp
		FROM bead_antigens ba
		JOIN beads b ON b.id = ba.bead_id
		JOIN bead_apc_scan s ON s.bead_id = ba.bead_id
		WHERE ba.antigen IN (%s) AND ba.patient_root = ? AND ba.bead_id != ?
		  AND b.type != 'sibling_link'
		ORDER BY ba.bead_id`,
		strings.Join(placeholders, ", "))

	rows, err := s.idx.SQLDB().Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("candidate rows: %w", err)
	}
	defer rows.Close()

	byID := make(map[string]*candidate)
	var order []string
	for rows.Next() {
		var id, antigen, typ, ts string
		if err := rows.Scan(&id, &antigen, &typ, &ts); err != nil {
			return nil, fmt.Errorf("candidate rows: scan: %w", err)
		}
		c, ok := byID[id]
		if !ok {
			c = &candidate{id: id, beadType: typ, timestamp: ts}
			byID[id] = c
			order = append(order, id)
		}
		c.shared = append(c.shared, antigen)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("candidate rows: %w", err)
	}

	sort.Strings(order)
	out := make([]candidate, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	return out, nil
}

// frequentAntigens returns the set of antigens whose patient-local frequency
// (distinct antigen-bearing Beads carrying that antigen, divided by the
// patient's total distinct antigen-bearing Beads) is >=
// Config.AntigenFrequencyThreshold — the IDF exclusion set (runaway
// prevention d).
//
// Both the numerator and denominator here exclude Beads of
// type='sibling_link' entirely (a JOIN against beads, not a bare COUNT over
// bead_antigens). This guards against IDF self-contamination: a
// sibling_link Bead's own Antigens are set to exactly its matched-antigen
// set (buildSiblingLinkBead), i.e. a *copy* of antigens that already exist
// on its two parent Beads, not new clinical evidence. Counting them would
// make an antigen's measured frequency drift upward purely as a side effect
// of how many sibling_link Beads Scan has already generated for it — a
// non-deterministic quantity that depends on scan history/timing/generation
// order rather than the patient's actual underlying data, which would in
// turn make frequentAntigens' IDF-filter decision (and therefore which
// pairs Scan considers at all) depend on incidental scan-execution timing.
// That breaks exactly the determinism/reproducibility property this whole
// pipeline is built to preserve (specs/DESIGN_v3.md §4's Bead-ID
// determinism rationale extends, in spirit, to APC's own generation
// behavior being reproducible from the same underlying ingest history).
// This was reproduced directly: after a sibling_link Bead was created and
// the index rebuilt via Reindex, a subsequent Scan's frequentAntigens count
// included the sibling_link Bead's copied antigen, pushing that antigen
// over the 30% default threshold and causing every further legitimate match
// on it to be filtered out as "too frequent" — a scan-order-dependent
// regression that excluding sibling_link Beads here eliminates.
func (s *Scanner) frequentAntigens(patientRoot string) (map[string]bool, error) {
	var totalBeads int
	if err := s.idx.SQLDB().QueryRow(`
		SELECT COUNT(DISTINCT ba.bead_id)
		FROM bead_antigens ba
		JOIN beads b ON b.id = ba.bead_id
		WHERE ba.patient_root = ? AND b.type != 'sibling_link'`,
		patientRoot,
	).Scan(&totalBeads); err != nil {
		return nil, fmt.Errorf("frequent antigens: count patient beads: %w", err)
	}
	if totalBeads == 0 {
		return map[string]bool{}, nil
	}

	rows, err := s.idx.SQLDB().Query(`
		SELECT ba.antigen, COUNT(DISTINCT ba.bead_id)
		FROM bead_antigens ba
		JOIN beads b ON b.id = ba.bead_id
		WHERE ba.patient_root = ? AND b.type != 'sibling_link'
		GROUP BY ba.antigen`,
		patientRoot,
	)
	if err != nil {
		return nil, fmt.Errorf("frequent antigens: query: %w", err)
	}
	defer rows.Close()

	out := make(map[string]bool)
	for rows.Next() {
		var antigen string
		var count int
		if err := rows.Scan(&antigen, &count); err != nil {
			return nil, fmt.Errorf("frequent antigens: scan: %w", err)
		}
		if float64(count)/float64(totalBeads) >= s.cfg.AntigenFrequencyThreshold {
			out[antigen] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("frequent antigens: %w", err)
	}
	return out, nil
}

// tryLink scores the (anchor, cand) pair and, if it clears
// Config.MinScoreThreshold and is not already recorded in sibling_pairs
// (runaway prevention a), ingests the resulting sibling_link Bead. It
// returns madeLink=false (with no error) for a pair that scores below
// threshold or was already linked for every one of its shared antigens —
// both are expected, common outcomes, not failures.
//
// This method itself no longer writes sibling_pairs or the bidirectional
// edge_type='sibling' bead_edges rows: index.IndexBead now derives both
// directly from any sibling_link Bead's own Parents/content.matched_antigens
// whenever one is indexed (see write.go's indexSiblingLink), which
// s.engine.Ingest(link) below triggers via its own IndexBead call. Writing
// them here too would only have been redundant (both use INSERT OR IGNORE /
// a UNIQUE constraint), but centralizing the derivation in IndexBead is what
// makes it also fire on Reindex/CatchUp — a plain Scanner-side write would
// silently vanish on any reindex, per specs/DESIGN_v3.md §1's "インデックス
// は正本から完全再構築可能" invariant (this was the exact bug a Reindex round-
// trip probe found: sibling_pairs/edge rows did not survive re-derivation
// from the Pod alone).
//
// pairGeneration is max(anchor's generation, cand's generation) — the depth
// of the *inputs* being matched, used only to decay the score (scorePair).
// The new sibling_link Bead this call may create is one level deeper than
// its inputs (a link between two generation-N Beads is itself generation
// N+1), which is what buildSiblingLinkBead's content.scan_generation
// records — runaway prevention c compares that recorded value against
// Config.MaxGeneration on each Bead's next turn as an anchor (see
// beadGeneration/scanOne), not the pairGeneration used here for decay.
func (s *Scanner) tryLink(anchor scannedBeadRef, cand candidate, pairGeneration int) (linkID string, madeLink bool, err error) {
	score := scorePair(anchor.Type, cand.beadType, anchor.Timestamp, cand.timestamp, cand.shared, pairGeneration, s.cfg)
	if score.Total < float64(s.cfg.MinScoreThreshold) {
		return "", false, nil
	}

	newAntigens, err := s.unlinkedAntigens(anchor.ID, cand.id, score.MatchedAntigens)
	if err != nil {
		return "", false, err
	}
	if len(newAntigens) == 0 {
		// Runaway prevention a: every one of this pair's matched antigens is
		// already recorded in sibling_pairs from a previous Scan call — the
		// pair has nothing new to link on.
		return "", false, nil
	}

	anchorBead, err := s.engine.GetBead(anchor.ID)
	if err != nil {
		return "", false, fmt.Errorf("get anchor bead %s: %w", anchor.ID, err)
	}
	candBead, err := s.engine.GetBead(cand.id)
	if err != nil {
		return "", false, fmt.Errorf("get candidate bead %s: %w", cand.id, err)
	}

	link := buildSiblingLinkBead(anchorBead, candBead, newAntigens, score.Total, pairGeneration+1)
	saved, err := s.engine.Ingest(link)
	if err != nil {
		return "", false, fmt.Errorf("ingest sibling_link for %s/%s: %w", anchor.ID, cand.id, err)
	}

	return saved.ID, true, nil
}

// unlinkedAntigens filters matched down to the antigens not already present
// in sibling_pairs for this (a, b) pair (runaway prevention a — the UNIQUE
// constraint's application-level pre-check; migrations/0002_apc.sql's
// UNIQUE(bead_a, bead_b, matched_antigen) is the enforced guarantee this
// pre-check exists to avoid tripping on a routine, expected re-scan).
func (s *Scanner) unlinkedAntigens(aID, bID string, matched []string) ([]string, error) {
	pairA, pairB := aID, bID
	if pairB < pairA {
		pairA, pairB = pairB, pairA
	}

	existing := make(map[string]bool, len(matched))
	rows, err := s.idx.SQLDB().Query(
		`SELECT matched_antigen FROM sibling_pairs WHERE bead_a = ? AND bead_b = ?`,
		pairA, pairB,
	)
	if err != nil {
		return nil, fmt.Errorf("unlinked antigens: query: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var ag string
		if err := rows.Scan(&ag); err != nil {
			return nil, fmt.Errorf("unlinked antigens: scan: %w", err)
		}
		existing[ag] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("unlinked antigens: %w", err)
	}

	out := make([]string, 0, len(matched))
	for _, ag := range matched {
		if !existing[ag] {
			out = append(out, ag)
		}
	}
	return out, nil
}

// scannedBeadRef is one row of the bead_apc_scan-watermark query: enough of
// a Bead's identity/location to score and match it without a second
// round-trip for type/timestamp/patient_root.
type scannedBeadRef struct {
	ID          string
	PatientRoot string
	Type        string
	Timestamp   string
}

// unscannedBeads returns every Bead with no bead_apc_scan row yet, ordered
// by id for deterministic Scan output — the "新Bead" anchor set (DESIGN §7).
func (s *Scanner) unscannedBeads() ([]scannedBeadRef, error) {
	rows, err := s.idx.SQLDB().Query(`
		SELECT b.id, COALESCE(b.patient_root, ''), b.type, b.timestamp
		FROM beads b
		LEFT JOIN bead_apc_scan s ON s.bead_id = b.id
		WHERE s.bead_id IS NULL
		ORDER BY b.id`)
	if err != nil {
		return nil, fmt.Errorf("unscanned beads: %w", err)
	}
	defer rows.Close()

	var out []scannedBeadRef
	for rows.Next() {
		var ref scannedBeadRef
		if err := rows.Scan(&ref.ID, &ref.PatientRoot, &ref.Type, &ref.Timestamp); err != nil {
			return nil, fmt.Errorf("unscanned beads: scan: %w", err)
		}
		out = append(out, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("unscanned beads: %w", err)
	}
	return out, nil
}

// scanState returns beadID's current (sibling_count, scan_generation) from
// bead_apc_scan, or (0, 0) if it has no row yet (a candidate discovered via
// bead_antigens that has somehow never been through Scan itself — should not
// normally happen since candidateRows joins against bead_apc_scan, but
// scanState is also called for the anchor, which by definition has no row
// yet on its first visit).
func (s *Scanner) scanState(beadID string) (siblingCount, generation int, err error) {
	err = s.idx.SQLDB().QueryRow(
		`SELECT sibling_count, scan_generation FROM bead_apc_scan WHERE bead_id = ?`,
		beadID,
	).Scan(&siblingCount, &generation)
	switch {
	case err == sql.ErrNoRows:
		return 0, 0, nil
	case err != nil:
		return 0, 0, fmt.Errorf("scan state %s: %w", beadID, err)
	}
	return siblingCount, generation, nil
}

// markScanned upserts beadID's bead_apc_scan row: inserts it with the given
// siblingCount/generation if absent, or (if already present) advances
// scanned_at and raises generation to max(existing, generation) without
// touching sibling_count (bumpSiblingCount owns that increment separately,
// called after the anchor's full candidate loop completes in scanOne).
func (s *Scanner) markScanned(beadID string, siblingCount, generation int) error {
	now := nowRFC3339()
	_, err := s.idx.SQLDB().Exec(`
		INSERT INTO bead_apc_scan (bead_id, scanned_at, scan_generation, sibling_count)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (bead_id) DO UPDATE SET
			scanned_at = excluded.scanned_at,
			scan_generation = MAX(scan_generation, excluded.scan_generation)`,
		beadID, now, generation, siblingCount)
	if err != nil {
		return fmt.Errorf("mark scanned %s: %w", beadID, err)
	}
	return nil
}

// bumpSiblingCount increments beadID's bead_apc_scan.sibling_count by delta
// (runaway prevention b's persisted counter). beadID must already have a
// bead_apc_scan row (markScanned is always called for the anchor before this
// in scanOne).
func (s *Scanner) bumpSiblingCount(beadID string, delta int) error {
	_, err := s.idx.SQLDB().Exec(
		`UPDATE bead_apc_scan SET sibling_count = sibling_count + ? WHERE bead_id = ?`,
		delta, beadID)
	if err != nil {
		return fmt.Errorf("bump sibling count %s: %w", beadID, err)
	}
	return nil
}
