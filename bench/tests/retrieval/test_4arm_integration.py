"""Integration test: real scratch ingest (--limit 10) + real APC scan (via
apc_trigger, MCP — R8.5) + real (fake-encoder-backed) embedding sidecar,
then all 4 retrieval arms (rag/fts/dag_nosib/dag_full) against the same
scenario, checking:

  1. every arm returns a RetrievalResult for the same (question,
     patient_id, budget) input,
  2. dag_full vs. dag_nosib actually differ for a patient with a real
     sibling_link (the whole point of R8.2's arm split — dag_nosib's
     include_siblings=False must measurably drop sibling-only Beads
     dag_full includes).

Requires the `go` toolchain and the real Synthea dataset (see
tests/conftest.py) — skipped automatically if either is unavailable. Never
touches ~/medbeads-synthea/medbeads_data (the busy real store) — every
ingest here goes into tmp_path via --limit, per this task's scratch-data
mandate.
"""

from __future__ import annotations

import asyncio
import re
import subprocess
from pathlib import Path

import pytest

from bench.ingest.mcp_client import MedBeadsClient
from bench.ingest.run import run_ingest
from bench.retrieval.dag import DagRetriever
from bench.retrieval.fts import FtsRetriever
from bench.retrieval.metrics import score_retrieval
from bench.retrieval.rag import RagRetriever
from bench.scenarios.generate import generate_scenarios

REPO_ROOT = Path(__file__).resolve().parents[3]

# FTS5's default (unicode61) query-string parser gives special meaning to
# punctuation (', -, ", :, ...) — see bench/bench/perf/queries.py's own
# _WORD_RE/_longest_word doc comment for the VERIFIED real-server error this
# produces on an unquoted multi-word/punctuated string. This test's own
# scenario questions and sibling_link descriptions are full sentences
# (Japanese question text, or apc/link.go's "APC scanner matched ... (score
# N.NN): ..." description), so every fts/dag arm query used here reduces to
# one safe plain-ASCII-letters word first — the exact same constraint any
# real fts/dag_nosib/dag_full caller (including a future LLM-driven bench
# run, 3b, feeding bench.scenarios' own generated question text straight in)
# will need to satisfy; this is not a test-only workaround, and is reported
# as a real caveat in this unit's final report.
_WORD_RE = re.compile(r"[A-Za-z]+")
_MIN_WORD_LEN = 4


def _fts_safe_query(text: str) -> str:
    words = [w for w in _WORD_RE.findall(text) if len(w) >= _MIN_WORD_LEN]
    if not words:
        return "encounter"  # universal fallback: every scratch patient has fhir_encounter Beads
    return max(words, key=len).lower()


async def _ingest_scratch(
    tmp_path: Path, medbeadsd_binary: Path, synthea_fhir_dir: Path, *, limit: int
) -> tuple[Path, Path]:
    data_dir = tmp_path / "data"
    manifest_path = tmp_path / "manifest.jsonl"
    run_manifest_path = tmp_path / "run_manifest.json"
    summary = await run_ingest(
        fhir_dir=synthea_fhir_dir,
        data_dir=data_dir,
        medbeadsd_path=medbeadsd_binary,
        manifest_path=manifest_path,
        run_manifest_path=run_manifest_path,
        limit=limit,
    )
    assert summary.ok_patients == limit, f"scratch ingest: {summary.failures}"
    return data_dir, manifest_path


def _run_embed_backfill(medbeadsd_binary: Path, data_dir: Path, embedder_url: str) -> None:
    """One-shot `medbeadsd embed -data <dir> -embedder <url>` CLI backfill
    (a standalone subcommand, not an MCP tool — same tier as building the
    medbeadsd binary itself: infrastructure this test needs to set up
    before a `serve -embedder` session can do semantic search over data
    that already exists, mirroring bench/README.md's own documented
    invocation)."""
    result = subprocess.run(
        [str(medbeadsd_binary), "embed", "-data", str(data_dir), "-embedder", embedder_url],
        capture_output=True,
        text=True,
        timeout=60,
    )
    assert result.returncode == 0, f"medbeadsd embed failed:\nstdout={result.stdout}\nstderr={result.stderr}"


async def _run_apc_to_convergence(client: MedBeadsClient, *, max_rounds: int = 10) -> int:
    """Runs apc_trigger (MCP, system role — R8.5-compliant) repeatedly until
    a round scans zero Beads, mirroring internal/mcpserver/integration_test.go's
    own convergence loop. Returns total sibling_links_created across every
    round."""
    total_links = 0
    for _ in range(max_rounds):
        out = await client.apc_trigger()
        total_links += out.get("sibling_links_created", 0)
        if out.get("beads_scanned", 0) == 0:
            break
    return total_links


@pytest.fixture
def scratch_data_with_siblings(
    tmp_path: Path, medbeadsd_binary: Path, synthea_fhir_dir: Path, fake_embed_sidecar_url: str
) -> tuple[Path, Path]:
    """Ingests 10 scratch patients, runs APC to convergence (for real
    sibling_link data), and backfills embeddings (for semantic=True) — the
    full setup this module's tests share. Returns (data_dir, manifest_path).
    """
    data_dir, manifest_path = asyncio.run(
        _ingest_scratch(tmp_path, medbeadsd_binary, synthea_fhir_dir, limit=10)
    )

    async def _apc() -> int:
        async with MedBeadsClient(medbeadsd_binary, data_dir, role="system") as client:
            return await _run_apc_to_convergence(client)

    total_links = asyncio.run(_apc())
    if total_links == 0:
        pytest.skip(
            "APC scan produced no sibling_link Beads for this 10-patient scratch sample "
            "(shared-antigen coverage varies by which patients --limit 10 selects); "
            "dag_full-vs-dag_nosib comparison needs at least one real sibling pair"
        )

    _run_embed_backfill(medbeadsd_binary, data_dir, fake_embed_sidecar_url)

    return data_dir, manifest_path


def test_all_four_arms_return_a_retrieval_result_for_the_same_scenario(
    medbeadsd_binary: Path, synthea_fhir_dir: Path, scratch_data_with_siblings: tuple[Path, Path],
    fake_embed_sidecar_url: str,
) -> None:
    asyncio.run(
        _run_all_four_arms(medbeadsd_binary, synthea_fhir_dir, scratch_data_with_siblings, fake_embed_sidecar_url)
    )


async def _run_all_four_arms(
    medbeadsd_binary: Path,
    synthea_fhir_dir: Path,
    scratch_data_with_siblings: tuple[Path, Path],
    fake_embed_sidecar_url: str,
) -> None:
    data_dir, manifest_path = scratch_data_with_siblings

    scenarios = generate_scenarios(fhir_dir=synthea_fhir_dir, manifest_path=manifest_path)
    assert scenarios, "scratch ingest should produce at least one scenario"
    scenario = scenarios[0]
    # rag (pure vector) can safely use the full free-text question; fts/dag
    # arms route through FTS5 MATCH underneath (search_beads / retrieve's
    # anchor search), which cannot tolerate raw punctuation/multi-word text
    # unquoted — see this module's _fts_safe_query doc comment.
    fts_query = _fts_safe_query(scenario.question)

    budget = 2000

    async with MedBeadsClient(
        medbeadsd_binary, data_dir, role="viewer", embedder_url=fake_embed_sidecar_url
    ) as client:
        rag = RagRetriever(client)
        fts = FtsRetriever(client)
        dag_nosib = DagRetriever(client, include_siblings=False)
        dag_full = DagRetriever(client, include_siblings=True)

        for retriever, question in (
            (rag, scenario.question),
            (fts, fts_query),
            (dag_nosib, fts_query),
            (dag_full, fts_query),
        ):
            result = await retriever.retrieve(
                question=question, patient_id=scenario.patient_id, budget=budget
            )
            assert result.arm == retriever.arm
            assert len(result.bead_ids) == len(result.texts), (
                f"{retriever.arm}: bead_ids/texts length mismatch"
            )
            assert result.used_tokens <= budget, f"{retriever.arm}: used_tokens exceeds budget"
            assert result.latency_ms >= 0.0

            score = score_retrieval(result, scenario.evidence_bead_ids)
            assert 0.0 <= score.recall <= 1.0
            assert 0.0 <= score.precision <= 1.0


def test_dag_full_and_dag_nosib_differ_for_a_patient_with_real_siblings(
    medbeadsd_binary: Path, scratch_data_with_siblings: tuple[Path, Path], fake_embed_sidecar_url: str
) -> None:
    asyncio.run(
        _run_dag_sibling_difference(medbeadsd_binary, scratch_data_with_siblings, fake_embed_sidecar_url)
    )


async def _run_dag_sibling_difference(
    medbeadsd_binary: Path, scratch_data_with_siblings: tuple[Path, Path], fake_embed_sidecar_url: str
) -> None:
    data_dir, manifest_path = scratch_data_with_siblings

    async with MedBeadsClient(medbeadsd_binary, data_dir, role="system") as client:
        # Find a real sibling_link Bead directly (system role, MCP-only —
        # R8.5) to identify a patient + anchor Bead this test can query for,
        # since generate_scenarios draws from FHIR/manifest content alone
        # and has no visibility into which patient APC actually linked.
        patients = await client.list_patients()
        sibling_link_beads: list[dict] = []
        for patient in patients:
            timeline = await client.get_timeline(patient["id"])
            sibling_link_beads = [b for b in timeline if b.get("type") == "sibling_link"]
            if sibling_link_beads:
                break
        assert sibling_link_beads, "scratch_data_with_siblings fixture guarantees at least one sibling_link"

        link_bead = await client.get_bead(sibling_link_beads[0]["id"])
        # A sibling_link Bead's Parents field is the two linked Bead IDs
        # (internal/engine/apc/link.go's buildSiblingLinkBead sets
        # Parents: []string{pairA.ID, pairB.ID} — not a content field) — use
        # the first as this test's retrieve() anchor, and resolve the
        # *patient* root separately via get_bead on that anchor (a
        # sibling_link Bead's own patient_root is not directly exposed by
        # get_bead's beadView, but its parent Bead's is the same patient).
        parents = link_bead.get("parents", [])
        assert len(parents) == 2, f"sibling_link Bead should have exactly 2 parents, got {parents!r}"
        anchor_bead_id = parents[0]

        anchor_bead = await client.get_bead(anchor_bead_id)
        # _longest_display_string over "first string found" (see its own doc
        # comment): a more clinically descriptive query than a short
        # structural code (e.g. "AMB", encounter.class.code) — a real
        # improvement, though not this test's actual root-cause fix (see
        # budget below).
        raw_text = _longest_display_string(anchor_bead.get("content", {})) or anchor_bead.get("type", "")
        query_text = _fts_safe_query(raw_text)

        patient_root = await _resolve_patient_root(client, anchor_bead_id)

    async with MedBeadsClient(
        medbeadsd_binary, data_dir, role="viewer", embedder_url=fake_embed_sidecar_url
    ) as client:
        # budget must be large enough that semantic=True's anchor set
        # (semanticAnchorK=50 unbounded neighbor hits, internal/mcpserver/
        # retrieve.go) does not exhaust the whole budget at the anchor (L0)
        # tier alone before BuildContext ever reaches the sibling tier —
        # VERIFIED via standalone debugging against this same scratch
        # dataset: at budget=4000, both calls hit used_tokens≈3999 with
        # provenance={'anchor'} only (52 anchors' L0 content alone fills the
        # budget), so dag_nosib and dag_full converge on an identical
        # bead_ids set even though include_siblings genuinely differs
        # server-side — a false negative for this test's whole point, not a
        # real absence of difference. budget=60000 (VERIFIED, same dataset)
        # leaves enough room for lower-priority tiers (ancestor/sibling/
        # descendant) to actually compete for space and diverge.
        budget = 60_000
        dag_nosib = DagRetriever(client, include_siblings=False, chain_depth=5)
        dag_full = DagRetriever(client, include_siblings=True, chain_depth=5)

        result_nosib = await dag_nosib.retrieve(question=query_text, patient_id=patient_root, budget=budget)
        result_full = await dag_full.retrieve(question=query_text, patient_id=patient_root, budget=budget)

        assert set(result_nosib.bead_ids) != set(result_full.bead_ids), (
            "dag_nosib and dag_full returned identical bead_ids sets for a patient with a real "
            f"sibling_link; nosib={result_nosib.bead_ids} full={result_full.bead_ids}"
        )
        # dag_nosib must never report a sibling-tier provenance at all — the
        # direct, mechanical check that include_siblings=False actually
        # skipped both sibling tiers server-side (graph.WithSiblings(false)),
        # not just an incidental difference from budget truncation timing.
        nosib_provenances = set(result_nosib.meta["provenance"])
        assert "sibling" not in nosib_provenances, (
            f"dag_nosib (include_siblings=False) must never report provenance='sibling', got {nosib_provenances}"
        )


async def _resolve_patient_root(client: MedBeadsClient, bead_id: str) -> str:
    """The patient_root a Bead belongs to, resolved via list_patients +
    get_timeline (MCP-only, R8.5-compliant): get_bead's own beadView does
    not carry patient_root (internal/mcpserver/render.go's beadView has no
    such field — only beadRefView does, via search results), so this walks
    every patient's timeline looking for bead_id. Scratch data (--limit 10)
    keeps this cheap; not a pattern meant for production-scale use.
    """
    patients = await client.list_patients()
    for patient in patients:
        timeline = await client.get_timeline(patient["id"])
        if any(b["id"] == bead_id for b in timeline):
            return patient["id"]
    raise AssertionError(f"bead {bead_id} not found in any patient's get_timeline")


def _longest_display_string(content: dict) -> str | None:
    """A clinically-descriptive string value from content, preferring FHIR's
    own `display`/`text` keys (the same "these are the human-readable name
    fields" convention bench/bench/perf/queries.py's
    _codeable_concept_names already relies on for identical reasons) over a
    generic "any string anywhere" walk.

    Earlier versions of this helper picked the single *longest* string
    reachable anywhere in content — VERIFIED (real scratch-data debugging)
    that this reliably prefers boilerplate long strings with no clinical
    specificity at all: Synthea stamps a `system` URI field (e.g.
    "Organization?identifier=https://github.com/synthetichealth/synthea|...")
    onto nearly every resource, which is both longer than any real display
    name in the fixture and — worse — near-*identical* across the entire
    corpus, so an FTS/semantic query derived from it matches the vast
    majority of Beads regardless of patient, saturating retrieve's anchor
    tier and starving the sibling tier of any budget to compete for
    (exactly the false-negative failure mode this test exists to avoid).
    display/text-keyed values are dramatically less likely to hit this
    trap, since those are FHIR's own "designed to be read by a human"
    fields, not identifiers/URIs.
    """
    preferred: str | None = None
    fallback: str | None = None

    def walk(value: object, key: str | None) -> None:
        nonlocal preferred, fallback
        if isinstance(value, str):
            text = value.strip()
            if not text:
                return
            if key in ("display", "text") and (preferred is None or len(text) > len(preferred)):
                preferred = text
            elif "://" not in text and "?" not in text and (fallback is None or len(text) > len(fallback)):
                # Exclude URL/query-string-shaped values (identifier
                # system URIs, reference query strings) from the fallback
                # pool too — the same trap that made "longest string
                # anywhere" unsafe applies to any non-preferred-key string
                # that merely looks like a URI.
                fallback = text
        elif isinstance(value, dict):
            for k, v in value.items():
                walk(v, k)
        elif isinstance(value, list):
            for item in value:
                walk(item, key)

    walk(content, None)
    return preferred or fallback
