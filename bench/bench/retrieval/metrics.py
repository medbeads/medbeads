"""recall/precision@budget over a RetrievalResult vs. a scenario's
evidence_bead_ids — R8.3's retrieval-only metrics pair, computed here (not
under a future bench/metrics/ package, which is 3b's LLM-attribution/
hallucination-rate territory) because they are a complete, self-contained
function of RetrievalResult.bead_ids alone: no LLM call, no claim
decomposition, nothing 3b-specific — R8.2's arms already produce everything
these two numbers need.

"@budget" here means "over whatever bead_ids a Retriever's retrieve() call
actually returned under a given token_budget" — the metric is a property of
one already-budgeted RetrievalResult, not a separate budget parameter this
module re-applies.
"""

from __future__ import annotations

from dataclasses import dataclass

from bench.retrieval.base import RetrievalResult


@dataclass(frozen=True)
class RetrievalScore:
    """recall/precision for one RetrievalResult against one scenario's
    evidence_bead_ids, plus the raw counts they were computed from (so a
    reviewer can sanity-check a 0/0 edge case without recomputing it)."""

    recall: float
    precision: float
    true_positives: int
    retrieved_count: int
    evidence_count: int

    def to_json_dict(self) -> dict[str, float | int]:
        return {
            "recall": self.recall,
            "precision": self.precision,
            "true_positives": self.true_positives,
            "retrieved_count": self.retrieved_count,
            "evidence_count": self.evidence_count,
        }


def score_retrieval(result: RetrievalResult, evidence_bead_ids: list[str]) -> RetrievalScore:
    """recall = |retrieved ∩ evidence| / |evidence|, precision = |retrieved ∩
    evidence| / |retrieved| — the standard set-overlap definitions, computed
    over bead_ids as opaque set members (both sides are "sha256:..."
    strings from the same manifest/MCP-response ID space, so string equality
    is exact-match, no normalization needed).

    Edge cases (never raises, both are legitimate scenario/result shapes):
      - evidence_bead_ids empty: recall is defined as 0.0 (there is no
        possible "found the evidence" — a scenario with zero ground-truth
        evidence is a scenario-authoring bug elsewhere, not this function's
        problem to special-case as 1.0/undefined).
      - result.bead_ids empty: precision is defined as 0.0 (nothing was
        retrieved, so there is nothing to be correct or incorrect about, but
        "no false positives" should not read as "perfect precision" either
        — 0.0 keeps a budget=0/empty-result run from scoring artificially
        well on this axis).
    """
    retrieved = set(result.bead_ids)
    evidence = set(evidence_bead_ids)
    true_positives = len(retrieved & evidence)

    recall = true_positives / len(evidence) if evidence else 0.0
    precision = true_positives / len(retrieved) if retrieved else 0.0

    return RetrievalScore(
        recall=recall,
        precision=precision,
        true_positives=true_positives,
        retrieved_count=len(retrieved),
        evidence_count=len(evidence),
    )
