"""Unit tests for bench.embed_sidecar.model: prefix dispatch, normalization,
dimension validation. No real model load (see conftest.py's FakeEncoder)."""

from __future__ import annotations

import math

import pytest

from bench.embed_sidecar.model import (
    EMBED_DIM,
    PASSAGE_PREFIX,
    QUERY_PREFIX,
    EmbedModel,
    PrefixMode,
    prefix_for,
)
from tests.embed_sidecar.conftest import FakeEncoder, WrongDimEncoder


def test_prefix_for_none_mode_always_empty() -> None:
    assert prefix_for("e5-query", PrefixMode.NONE) == ""
    assert prefix_for("e5-passage", PrefixMode.NONE) == ""
    assert prefix_for("ruri-v3", PrefixMode.NONE) == ""


def test_prefix_for_model_suffix_mode_dispatches_on_suffix() -> None:
    assert prefix_for("e5-query", PrefixMode.MODEL_SUFFIX) == QUERY_PREFIX
    assert prefix_for("e5-passage", PrefixMode.MODEL_SUFFIX) == PASSAGE_PREFIX


def test_prefix_for_model_suffix_mode_unknown_model_is_empty_not_a_guess() -> None:
    # This is the safety property: today's real Go traffic sends one fixed
    # model string (e.g. "ruri-v3" or "e5") for BOTH document and query
    # embedding (see model.py's module docstring) -- MODEL_SUFFIX mode must
    # not guess a prefix for that string; it must fall back to "" exactly
    # like NONE mode would, so enabling MODEL_SUFFIX today is a no-op, never
    # a wrong-prefix regression.
    assert prefix_for("ruri-v3", PrefixMode.MODEL_SUFFIX) == ""
    assert prefix_for("intfloat/multilingual-e5-base", PrefixMode.MODEL_SUFFIX) == ""


def test_embed_applies_prefix_before_encoding(fake_encoder: FakeEncoder) -> None:
    model = EmbedModel(encoder=fake_encoder, model_name="fake-e5", prefix_mode=PrefixMode.MODEL_SUFFIX)
    model.embed("fake-e5-query", ["hypertension"])
    assert fake_encoder.last_call_sentences == ["query: hypertension"]

    model.embed("fake-e5-passage", ["blood pressure 140/90"])
    assert fake_encoder.last_call_sentences == ["passage: blood pressure 140/90"]


def test_embed_none_mode_sends_raw_text(fake_encoder: FakeEncoder) -> None:
    model = EmbedModel(encoder=fake_encoder, model_name="fake-e5", prefix_mode=PrefixMode.NONE)
    model.embed("fake-e5", ["hypertension"])
    assert fake_encoder.last_call_sentences == ["hypertension"]


def test_embed_preserves_order_and_count(embed_model: EmbedModel) -> None:
    vectors = embed_model.embed("fake-e5", ["a", "b", "c"])
    assert len(vectors) == 3
    assert all(len(v) == EMBED_DIM for v in vectors)
    # Different inputs -> different vectors (order not collapsed/shuffled).
    assert vectors[0] != vectors[1]
    assert vectors[1] != vectors[2]


def test_embed_output_is_l2_normalized(embed_model: EmbedModel) -> None:
    # vec0's bead_embed table has no explicit distance_metric override, so
    # sqlite-vec ranks by L2 distance (see internal/engine/index/embed.go's
    # SemanticResult doc comment) -- for that to rank identically to cosine
    # similarity, every embedding this sidecar returns must be unit-norm.
    vectors = embed_model.embed("fake-e5", ["some clinical text", "another one"])
    for v in vectors:
        norm = math.sqrt(sum(x * x for x in v))
        assert norm == pytest.approx(1.0, abs=1e-6)


def test_embed_rejects_wrong_dimension_encoder() -> None:
    model = EmbedModel(encoder=WrongDimEncoder(), model_name="bad-model", prefix_mode=PrefixMode.NONE)
    with pytest.raises(ValueError, match="dimension"):
        model.embed("bad-model", ["text"])
