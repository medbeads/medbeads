package projector

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/gowebpki/jcs"
	"github.com/medbeads/medbeads/internal/engine/index"
)

// ProjectionName is the fixed projection_manifest.projection_name this
// package's Reproject writes and flips — one named projection ("this
// store's clinical_links"), per migrations/0006's partial-unique-active
// index (at most one 'active' row per projection_name).
const ProjectionName = "clinical_links_v31"

// beadReader is the narrow subset of *engine.Engine Reproject needs: reading
// a single Bead's content back (to decode the link_rule Bead named by a
// knowledge Bead ID) and enumerating one patient's already-indexed Beads is
// not needed here — Reproject reads bead_tags/beads directly via idx
// (queryPatientTags) rather than replaying Pod content per Bead, since it
// must NOT re-scan Pods (see Reproject's own doc comment). Only GetBead is
// needed, and only for the link_rule Bead(s) named in knowledgeBeadIDs.
//
// This mirrors package apc's "ingester" interface convention (apc/
// scanner.go): projector depends on this interface, not on package engine
// itself, so it stays a sibling of engine/pod, engine/index under
// internal/engine/ (see doc.go).
type beadReader interface {
	GetBead(id string) (BeadContent, error)
}

// BeadContent is the minimal shape Reproject needs back from a Bead lookup:
// just its Content map. Defined here (rather than importing bead.Bead
// directly into beadReader's signature) so this package's public API
// surface does not force every caller to depend on package bead's Bead type
// merely to satisfy beadReader — a caller adapts engine.Engine.GetBead's
// richer bead.Bead return value into this shape (see cmd/medbeadsd's wiring
// or a future adapter).
type BeadContent struct {
	Content map[string]any
}

// Result summarizes one Reproject call, for callers (cmd/medbeadsd, tests)
// that want a count rather than re-querying clinical_links themselves.
type Result struct {
	RunID             string
	PatientsProjected int
	LinksWritten      int
}

// Reproject is U3b's full-reprojection entry point (specs/
// U3_link_projector.md's U3b section): it reads the already-indexed
// bead_tags/beads for every patient (plus the shared Pod) from idx, and for
// each one deterministically recomputes its clinical_links rows under the
// given knowledgeBeadIDs (currently just the one cooccurrence link_rule
// Bead's ID) + codeVersion, replacing that patient's previous-run rows with
// the new run's rows in a single per-patient transaction, then flips
// projection_manifest's single active row for ProjectionName to the new run
// in one final small transaction.
//
// # Reproject is not Reindex
//
// Reproject never touches Pod files and never calls index.Reindex or
// index.CatchUp: it is a pure re-derivation from index.db's own already-
// projected bead_tags/beads tables (specs/U3_link_projector.md: "Reproject
// は Pod 再スキャンしない"). Reindex (rebuilding index.db itself from Pods) is
// a distinct, unrelated recovery operation; Reproject is what runs when the
// *knowledge* (link_rule/dictionary Beads) changes, not when the index is
// lost.
//
// # Why patient_root-batched, not one global transaction
//
// A single transaction spanning every patient's DELETE+INSERT was
// considered and rejected (specs/U3_link_projector.md's crux — data-
// reviewer + Codex peer agreement, docs/decisions.md 2026-07-11 U3 entry):
// at the 1,135-patient / ~1.04M-Bead scale this store targets, holding
// SQLite's single writer lock for one all-patients transaction would
// starve concurrent Ingest calls (index.Open's SetMaxOpenConns(1) design —
// see index.go) for however long the whole reprojection takes, risking a
// 5-second busy_timeout abort on a concurrent write. Reindex/CatchUp already
// established the precedent of bounding transaction size (batchSize=500 in
// reindex.go) rather than assuming a single all-Pods transaction is safe;
// Reproject follows the same principle at patient-root granularity instead
// (a natural, already-existing scope boundary: get_links/retrieve reads are
// themselves patient-scoped, so per-patient atomicity is exactly the
// consistency granularity a reader actually needs — see the DELETE+INSERT
// scoping below).
//
// # Read consistency during a Reproject run
//
// Because DELETE+INSERT for a given patient_root happens inside one
// transaction, any reader (get_links, retrieve) querying that patient mid-
// run always sees either the complete old run's rows or the complete new
// run's rows for that patient, never a mix — the per-patient transaction
// boundary is what makes this hold, independent of when the final manifest
// flip happens. The manifest flip itself only changes which run_id counts
// as "active" for audit/provenance purposes; it does not gate readers that
// query clinical_links directly by patient_root (a future get_links MCP
// tool could additionally filter by the currently-active run_id if it
// wants a global point-in-time view, but this is a U3c/U4 read-side
// concern, not Reproject's).
//
// # Parameters
//
// knowledgeBeadIDs is the JSON-recorded set of dictionary + link_rule Bead
// IDs this run consulted (manifest.knowledge_bead_ids) — for U3b this is
// just the single cooccurrence link_rule Bead's own ID, found via
// LoadActiveCooccurrenceRule. codeVersion is an opaque caller-supplied
// string (e.g. a git SHA + build tag) recorded verbatim in
// manifest.code_version. builtAt is a caller-supplied RFC3339 timestamp
// (ASSUMED: Reproject does not call time.Now() itself — see this package's
// determinism discipline elsewhere, e.g. computeLinkID/linkCreatedAt; a
// caller wanting "now" computes it once at the call site, mirroring
// apc/link.go's nowRFC3339 var-not-inline-call pattern for testability).
func Reproject(idx *index.DB, reader beadReader, knowledgeBeadIDs []string, codeVersion string, builtAt string) (Result, error) {
	rule, err := loadRule(idx, reader, knowledgeBeadIDs)
	if err != nil {
		return Result{}, fmt.Errorf("projector: reproject: %w", err)
	}

	configHash, err := computeConfigHash(knowledgeBeadIDs, codeVersion)
	if err != nil {
		return Result{}, fmt.Errorf("projector: reproject: %w", err)
	}
	watermarks, err := queryInputWatermarks(idx.SQLDB())
	if err != nil {
		return Result{}, fmt.Errorf("projector: reproject: %w", err)
	}
	runID, err := computeRunID(ProjectionName, knowledgeBeadIDs, configHash, codeVersion, builtAt)
	if err != nil {
		return Result{}, fmt.Errorf("projector: reproject: %w", err)
	}

	if err := insertBuildingManifest(idx.SQLDB(), ProjectionName, runID, codeVersion, knowledgeBeadIDs, configHash, watermarks, builtAt); err != nil {
		return Result{}, fmt.Errorf("projector: reproject: %w", err)
	}

	patients, err := idx.ListPatients()
	if err != nil {
		return Result{}, fmt.Errorf("projector: reproject: list patients: %w", err)
	}

	var res Result
	res.RunID = runID
	for _, p := range patients {
		written, err := reprojectPatient(idx.SQLDB(), rule, p.ID, runID)
		if err != nil {
			return res, fmt.Errorf("projector: reproject: patient %s: %w", p.ID, err)
		}
		res.PatientsProjected++
		res.LinksWritten += written
	}

	if err := flipManifestActive(idx.SQLDB(), ProjectionName, runID); err != nil {
		return res, fmt.Errorf("projector: reproject: %w", err)
	}

	return res, nil
}

// loadRule resolves the cooccurrence LinkRule from idx's shared Beads,
// decoding each candidate's content via reader.GetBead
// (LoadActiveCooccurrenceRule's getContent callback), restricted to the
// caller-supplied knowledgeBeadIDs set (specs/U4_state_derivation.md's U3
// follow-up): when knowledgeBeadIDs names a specific rule Bead, that Bead —
// not whichever same-rule_id revision happens to have the lexicographically
// greatest ID — wins. An empty knowledgeBeadIDs preserves
// LoadActiveCooccurrenceRule's original "greatest ID among every matching
// Bead wins" behavior, which is what every current caller of Reproject
// relies on (they all pass a single-element slice naming the one rule they
// just resolved/seeded, so this filter is a no-op for them today; it only
// changes behavior once a caller passes a set containing more than one
// same-rule_id Bead ID).
func loadRule(idx *index.DB, reader beadReader, knowledgeBeadIDs []string) (LinkRule, error) {
	rule, err := LoadActiveCooccurrenceRule(idx, func(id string) (map[string]any, error) {
		b, err := reader.GetBead(id)
		if err != nil {
			return nil, err
		}
		return b.Content, nil
	}, knowledgeBeadIDs...)
	if err != nil {
		return LinkRule{}, err
	}
	return rule, nil
}

// reprojectPatient replaces patientRoot's clinical_links rows (any row not
// already stamped with runID — i.e. every row from a prior run) with the
// newly-computed set, in a single transaction: DELETE the old run's rows for
// this patient, then INSERT every newly-computed row stamped with runID.
// Returns how many rows were written (inserted) for this patient.
func reprojectPatient(sqlDB *sql.DB, rule LinkRule, patientRoot, runID string) (int, error) {
	tags, err := queryPatientTags(sqlDB, patientRoot)
	if err != nil {
		return 0, err
	}
	links := projectPatientLinks(rule, patientRoot, tags)

	tx, err := sqlDB.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after commit is a no-op

	if _, err := tx.Exec(
		`DELETE FROM clinical_links WHERE patient_root = ? AND (projection_run_id IS NULL OR projection_run_id <> ?)`,
		patientRoot, runID,
	); err != nil {
		return 0, fmt.Errorf("delete stale clinical_links for %s: %w", patientRoot, err)
	}

	for _, link := range links {
		evidenceJSON, err := canonicalJSON(nonNilStrings(link.EvidenceBeadIDs))
		if err != nil {
			return 0, fmt.Errorf("encode evidence_bead_ids: %w", err)
		}
		scoreJSON, err := canonicalJSON(link.ScoreBreakdown)
		if err != nil {
			return 0, fmt.Errorf("encode score_breakdown: %w", err)
		}

		if _, err := tx.Exec(`
			INSERT INTO clinical_links
				(link_id, bead_a, bead_b, patient_root, relation, matched_tag,
				 severity, evidence_basis, evidence_bead_ids, score_breakdown,
				 rule_id, rule_version, projection_run_id, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (bead_a, bead_b, relation, matched_tag) DO UPDATE SET
				link_id = excluded.link_id,
				severity = excluded.severity,
				evidence_basis = excluded.evidence_basis,
				evidence_bead_ids = excluded.evidence_bead_ids,
				score_breakdown = excluded.score_breakdown,
				rule_id = excluded.rule_id,
				rule_version = excluded.rule_version,
				projection_run_id = excluded.projection_run_id,
				created_at = excluded.created_at`,
			link.LinkID, link.BeadA, link.BeadB, link.PatientRoot, link.Relation, link.MatchedTag,
			link.Severity, link.EvidenceBasis, evidenceJSON, scoreJSON,
			link.RuleID, link.RuleVersion, runID, link.CreatedAt,
		); err != nil {
			return 0, fmt.Errorf("insert clinical_link %s/%s/%s: %w", link.BeadA, link.BeadB, link.MatchedTag, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return len(links), nil
}

// nonNilStrings returns ss, or an empty (non-nil) []string if ss is nil, so
// canonicalJSON always encodes evidence_bead_ids as "[]" rather than "null"
// (clinical_links.evidence_bead_ids is NOT NULL DEFAULT '[]' — see
// migrations/0006), mirroring bead.normalizeStrings' identical null-vs-[]
// JCS concern for Parents/Amends/Retracts.
func nonNilStrings(ss []string) []string {
	if ss == nil {
		return []string{}
	}
	return ss
}

// queryInputWatermarks returns pods.path -> indexed_upto for every pod row,
// as the canonical JSON object manifest.input_watermarks records (specs/
// U2_projection_schema.md's schema comment: "JSON: pod path ->
// indexed_upto(増分再現に必須)"). Sorted by path for deterministic encoding.
func queryInputWatermarks(sqlDB *sql.DB) (string, error) {
	rows, err := sqlDB.Query(`SELECT path, indexed_upto FROM pods ORDER BY path`)
	if err != nil {
		return "", fmt.Errorf("query input watermarks: %w", err)
	}
	defer rows.Close()

	out := make(map[string]any)
	for rows.Next() {
		var path string
		var upto int64
		if err := rows.Scan(&path, &upto); err != nil {
			return "", fmt.Errorf("query input watermarks: scan: %w", err)
		}
		out[path] = upto
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("query input watermarks: %w", err)
	}
	return canonicalJSON(out)
}

// computeConfigHash derives manifest.config_hash deterministically from the
// sorted knowledgeBeadIDs and codeVersion — a caller re-running Reproject
// with the identical knowledge set and code version always gets the
// identical config_hash, which is what makes it useful as an at-a-glance
// "did the effective configuration change" audit signal alongside the two
// raw inputs it is derived from.
func computeConfigHash(knowledgeBeadIDs []string, codeVersion string) (string, error) {
	sorted := append([]string(nil), knowledgeBeadIDs...)
	sort.Strings(sorted)
	payload := map[string]any{
		"knowledge_bead_ids": sorted,
		"code_version":       codeVersion,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("compute config hash: marshal: %w", err)
	}
	canonical, err := jcs.Transform(raw)
	if err != nil {
		return "", fmt.Errorf("compute config hash: jcs transform: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

// computeRunID derives projection_manifest.run_id deterministically from
// (projectionName, sorted knowledgeBeadIDs, configHash, codeVersion,
// builtAt) — content-derived, not time.Now()/uuid, so that two runs of the
// same projection given identical inputs (including the identical builtAt
// string a caller supplies) are trivially detectable as "the same run" and
// so run_id generation needs no separate random source. builtAt is included
// in the hash specifically so that two *distinct* invocations against the
// same knowledge/config (e.g. a legitimate re-run after a transient failure)
// still get distinct run_ids, since a caller is expected to supply a fresh
// builtAt for each real invocation. projectionName is a parameter (not the
// package-level ProjectionName constant) for the same reuse reason as
// insertBuildingManifest/flipManifestActive above — it also guarantees two
// different projectors (Reproject's clinical_links_v31 vs record_state's
// record_state_v31) never collide on run_id even if every other input
// happened to match.
func computeRunID(projectionName string, knowledgeBeadIDs []string, configHash, codeVersion, builtAt string) (string, error) {
	sorted := append([]string(nil), knowledgeBeadIDs...)
	sort.Strings(sorted)
	payload := map[string]any{
		"projection_name":    projectionName,
		"knowledge_bead_ids": sorted,
		"config_hash":        configHash,
		"code_version":       codeVersion,
		"built_at":           builtAt,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("compute run id: marshal: %w", err)
	}
	canonical, err := jcs.Transform(raw)
	if err != nil {
		return "", fmt.Errorf("compute run id: jcs transform: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

// insertBuildingManifest inserts runID's projection_manifest row with
// status='building', the first step of a projector's manifest lifecycle
// (building -> [rows written] -> active, with the prior active row flipped
// to superseded in the same final transaction — see flipManifestActive).
//
// projectionName is the projection_manifest.projection_name this run belongs
// to (e.g. Reproject's ProjectionName, or record_state's own
// StatusProjectionName — see record_state.go): parameterized rather than
// hardcoded so both projectors in this package share one manifest-lifecycle
// implementation instead of duplicating it (specs/U4_state_derivation.md's
// projector-structure section: "insertBuildingManifest/flipManifestActive
// … を projection_name パラメタ化して再利用").
func insertBuildingManifest(sqlDB *sql.DB, projectionName, runID, codeVersion string, knowledgeBeadIDs []string, configHash, inputWatermarks, builtAt string) error {
	knowledgeJSON, err := canonicalJSON(nonNilStrings(knowledgeBeadIDs))
	if err != nil {
		return fmt.Errorf("encode knowledge_bead_ids: %w", err)
	}
	if _, err := sqlDB.Exec(`
		INSERT INTO projection_manifest
			(run_id, projection_name, code_version, knowledge_bead_ids, config_hash,
			 input_watermarks, built_at, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'building')`,
		runID, projectionName, codeVersion, knowledgeJSON, configHash, inputWatermarks, builtAt,
	); err != nil {
		return fmt.Errorf("insert building manifest %s: %w", runID, err)
	}
	return nil
}

// flipManifestActive is a projector run's final step: in one small
// transaction, supersede projectionName's current active run (if any) and
// activate runID — the atomic flip migrations/0006's partial-unique-active
// index exists to make safe (the database itself refuses to let two runs of
// the same projection_name be 'active' simultaneously). See
// insertBuildingManifest's doc comment for why projectionName is a parameter
// rather than the package-level ProjectionName constant.
func flipManifestActive(sqlDB *sql.DB, projectionName, runID string) error {
	tx, err := sqlDB.Begin()
	if err != nil {
		return fmt.Errorf("flip manifest: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after commit is a no-op

	now := builtAtNow()
	if _, err := tx.Exec(`
		UPDATE projection_manifest
		SET status = 'superseded', superseded_at = ?
		WHERE projection_name = ? AND status = 'active'`,
		now, projectionName,
	); err != nil {
		return fmt.Errorf("flip manifest: supersede prior active: %w", err)
	}
	if _, err := tx.Exec(`
		UPDATE projection_manifest
		SET status = 'active', activated_at = ?
		WHERE run_id = ?`,
		now, runID,
	); err != nil {
		return fmt.Errorf("flip manifest: activate %s: %w", runID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("flip manifest: commit: %w", err)
	}
	return nil
}

// builtAtNow is the one wall-clock read in this package, used only for
// superseded_at/activated_at (bookkeeping timestamps analogous to
// bead_apc_scan.scanned_at — an index.db column, not a hash-target /
// content-derived value; see apc/link.go's nowRFC3339 for the identical
// "this one column is legitimately wall-clock" distinction). It is a var
// (not an inline time.Now() call) so tests can override it, mirroring
// nowRFC3339's own testability pattern.
var builtAtNow = func() string { return time.Now().UTC().Format(time.RFC3339) }
