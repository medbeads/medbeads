"""Unit tests for bench.retrieval.metrics.score_retrieval — pure functions,
no MCP/medbeadsd needed."""

from __future__ import annotations

from bench.retrieval.base import RetrievalResult
from bench.retrieval.metrics import score_retrieval


def _result(bead_ids: list[str]) -> RetrievalResult:
    return RetrievalResult(
        arm="test",
        bead_ids=bead_ids,
        texts=["" for _ in bead_ids],
        used_tokens=0,
        latency_ms=0.0,
    )


def test_perfect_recall_and_precision() -> None:
    result = _result(["a", "b", "c"])
    score = score_retrieval(result, ["a", "b", "c"])
    assert score.recall == 1.0
    assert score.precision == 1.0
    assert score.true_positives == 3
    assert score.retrieved_count == 3
    assert score.evidence_count == 3


def test_partial_overlap() -> None:
    result = _result(["a", "b", "x", "y"])  # 2 true positives, 2 false positives
    score = score_retrieval(result, ["a", "b", "c"])  # 1 evidence item ("c") missed
    assert score.true_positives == 2
    assert score.recall == 2 / 3
    assert score.precision == 2 / 4


def test_no_overlap() -> None:
    result = _result(["x", "y"])
    score = score_retrieval(result, ["a", "b"])
    assert score.recall == 0.0
    assert score.precision == 0.0
    assert score.true_positives == 0


def test_empty_evidence_recall_is_zero_not_undefined() -> None:
    result = _result(["a"])
    score = score_retrieval(result, [])
    assert score.recall == 0.0
    assert score.evidence_count == 0


def test_empty_retrieval_precision_is_zero_not_perfect() -> None:
    result = _result([])
    score = score_retrieval(result, ["a", "b"])
    assert score.precision == 0.0
    assert score.retrieved_count == 0


def test_duplicate_bead_ids_in_result_do_not_inflate_true_positives() -> None:
    # bead_ids is retrieval order, which could in principle contain a repeat
    # (e.g. an arm's own bug) — score_retrieval treats bead_ids as a set, so
    # a duplicate must not inflate true_positives beyond the evidence set's
    # own size.
    result = _result(["a", "a", "a"])
    score = score_retrieval(result, ["a"])
    assert score.true_positives == 1
    assert score.recall == 1.0
    assert score.retrieved_count == 1  # set() collapses the duplicate
