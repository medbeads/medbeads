"""Fixtures for embed_sidecar tests: a fake SentenceEncoder double so
request/response-shape and prefix-dispatch tests never need to load a real
~1GB sentence-transformers model (see model.py's SentenceEncoder Protocol)."""

from __future__ import annotations

import math

import pytest

from bench.embed_sidecar.model import EMBED_DIM, EmbedModel, PrefixMode


class FakeEncoder:
    """Deterministic stand-in for sentence_transformers.SentenceTransformer:
    encode() returns one EMBED_DIM-length vector per input string, derived
    from the string's content (so two different inputs get different
    vectors -- useful for asserting order-preservation) and always unit-norm
    when normalize_embeddings=True is honored (mirrors the real encoder's
    contract), so tests can assert on ||v||==1 without a real model."""

    def __init__(self, dim: int = EMBED_DIM) -> None:
        self._dim = dim
        self.last_call_sentences: list[str] | None = None

    def get_embedding_dimension(self) -> int:
        return self._dim

    def encode(self, sentences: list[str], normalize_embeddings: bool, convert_to_numpy: bool) -> list[list[float]]:
        self.last_call_sentences = list(sentences)
        out = []
        for s in sentences:
            seed = sum(ord(c) for c in s) or 1
            vec = [((seed * (i + 1)) % 97) / 97.0 + 0.01 for i in range(self._dim)]
            if normalize_embeddings:
                norm = math.sqrt(sum(x * x for x in vec))
                vec = [x / norm for x in vec]
            out.append(vec)
        return out


class WrongDimEncoder(FakeEncoder):
    def __init__(self) -> None:
        super().__init__(dim=EMBED_DIM - 1)


@pytest.fixture
def fake_encoder() -> FakeEncoder:
    return FakeEncoder()


@pytest.fixture
def embed_model(fake_encoder: FakeEncoder) -> EmbedModel:
    return EmbedModel(encoder=fake_encoder, model_name="fake-e5", prefix_mode=PrefixMode.NONE)
