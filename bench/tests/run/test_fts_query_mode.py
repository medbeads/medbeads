"""RunConfig.fts_query_mode (lead ruling, data-reviewer follow-up): pure
unit tests over _retrieval_query_for_arm — no MCP/medbeadsd needed."""

from __future__ import annotations

import pytest

from bench.run.pipeline import (
    ALL_ARMS,
    ARM_DAG_FULL,
    ARM_DAG_NOSIB,
    ARM_FTS,
    ARM_RAG,
    FTS_QUERY_MODE_SAFE_WORD,
    FTS_QUERY_MODE_SHARED_SAFE_WORD,
    RunConfig,
    _retrieval_query_for_arm,
    fts_safe_query,
)

_QUESTION = "患者の condition 'Essential hypertension' に対して処方された薬は?"


def test_safe_word_mode_gives_rag_the_full_question() -> None:
    assert _retrieval_query_for_arm(_QUESTION, ARM_RAG, FTS_QUERY_MODE_SAFE_WORD) == _QUESTION


@pytest.mark.parametrize("arm", [ARM_FTS, ARM_DAG_NOSIB, ARM_DAG_FULL])
def test_safe_word_mode_gives_fts_arms_the_reduced_word(arm: str) -> None:
    assert _retrieval_query_for_arm(_QUESTION, arm, FTS_QUERY_MODE_SAFE_WORD) == fts_safe_query(_QUESTION)


def test_shared_safe_word_mode_gives_every_arm_the_same_reduced_word() -> None:
    expected = fts_safe_query(_QUESTION)
    for arm in ALL_ARMS:
        assert _retrieval_query_for_arm(_QUESTION, arm, FTS_QUERY_MODE_SHARED_SAFE_WORD) == expected


def test_shared_safe_word_mode_rag_query_equals_other_arms_query() -> None:
    """The whole point of shared_safe_word: rag's query must be identical
    in content to every FTS5-bound arm's query, so no recall/precision gap
    between rag and the others can be attributed to rag simply getting a
    richer input string."""
    rag_query = _retrieval_query_for_arm(_QUESTION, ARM_RAG, FTS_QUERY_MODE_SHARED_SAFE_WORD)
    fts_query = _retrieval_query_for_arm(_QUESTION, ARM_FTS, FTS_QUERY_MODE_SHARED_SAFE_WORD)
    assert rag_query == fts_query


def test_unknown_fts_query_mode_raises() -> None:
    with pytest.raises(ValueError):
        _retrieval_query_for_arm(_QUESTION, ARM_RAG, "not_a_real_mode")


def test_run_config_default_fts_query_mode_is_safe_word(tmp_path) -> None:
    config = RunConfig(
        scenarios_path=tmp_path / "s.yaml",
        data_dir=tmp_path / "data",
        medbeadsd_path=tmp_path / "medbeadsd",
        out_dir=tmp_path / "out",
    )
    assert config.fts_query_mode == FTS_QUERY_MODE_SAFE_WORD


def test_run_config_config_hash_changes_with_fts_query_mode(tmp_path) -> None:
    base_kwargs = dict(
        scenarios_path=tmp_path / "s.yaml",
        data_dir=tmp_path / "data",
        medbeadsd_path=tmp_path / "medbeadsd",
        out_dir=tmp_path / "out",
    )
    safe_word = RunConfig(**base_kwargs, fts_query_mode=FTS_QUERY_MODE_SAFE_WORD)
    shared = RunConfig(**base_kwargs, fts_query_mode=FTS_QUERY_MODE_SHARED_SAFE_WORD)
    assert safe_word.config_hash() != shared.config_hash()
