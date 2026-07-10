"""Model loading + text-to-vector logic, isolated from the HTTP layer so
tests can exercise request/response shape handling without a real
sentence-transformers model load (see tests/embed_sidecar/test_app.py's
monkeypatched EmbedModel double).

# Prefix convention (intfloat/multilingual-e5-* family)

E5 models are trained with a task-prefix convention: passages (documents
being indexed) get "passage: " prepended, search queries get "query: "
prepended (intfloat's model card; not doing this measurably hurts retrieval
quality — E5 was fine-tuned expecting it). This sidecar needs to know, per
request, which prefix to use.

# How the prefix is dispatched: the request's `model` field

medbeadsd serve now supports per-role model names (`-embed-model` for the
passage/indexer side, `-embed-model-query` for the query side of
retrieve/rag_search — cmd/medbeadsd/serve.go's buildEmbedClients), and the
backfill CLI (`medbeadsd embed -embed-model ...`) is passage-only. So the
production configuration is:

    sidecar:  --prefix-mode model_suffix
    backfill: medbeadsd embed  ... -embed-model e5-passage
    serve:    medbeadsd serve ... -embed-model e5-passage -embed-model-query e5-query

PrefixMode.MODEL_SUFFIX: requests whose `model` ends with "-query" get
"query: ", "-passage" gets "passage: ", any other model name gets no
prefix. The fallback makes a mismatched operator setup degrade to
symmetric-no-prefix on that role rather than applying a wrong prefix —
but note the ASYMMETRY hazard: if only ONE side sends a suffixed name,
that side alone gets a prefix and retrieval quality suffers. Keep the
three-line configuration above consistent.

PrefixMode.NONE (the default): no prefix for either role — symmetric,
correct-but-degraded E5 usage. Safe default for ad-hoc runs; use
model_suffix + the flag pair for real benchmarks.
"""

from __future__ import annotations

import enum
from dataclasses import dataclass
from typing import Protocol

EMBED_DIM = 768  # must match internal/engine/index/embed.go's EmbedDim / migrations/0004_embed.sql's FLOAT[768]

DEFAULT_MODEL_NAME = "intfloat/multilingual-e5-base"

QUERY_PREFIX = "query: "
PASSAGE_PREFIX = "passage: "


class PrefixMode(enum.Enum):
    """How to pick an E5 task-prefix per request. See module docstring."""

    NONE = "none"
    MODEL_SUFFIX = "model_suffix"


def prefix_for(model: str, mode: PrefixMode) -> str:
    """Returns the E5 task-prefix to prepend to every input string for one
    /v1/embeddings request whose JSON body's `model` field is model, under
    prefix-dispatch mode. NONE always returns "". MODEL_SUFFIX inspects
    model's suffix; anything not ending in "-query"/"-passage" (including
    plain "e5" / DEFAULT_MODEL_NAME, i.e. today's real Go traffic) returns
    "" rather than guessing -- see module docstring's "can't do the wrong
    thing" property.
    """
    if mode is PrefixMode.NONE:
        return ""
    if model.endswith("-query"):
        return QUERY_PREFIX
    if model.endswith("-passage"):
        return PASSAGE_PREFIX
    return ""


class SentenceEncoder(Protocol):
    """The subset of sentence_transformers.SentenceTransformer's API this
    package depends on, so tests can substitute a fast fake instead of
    loading a real ~1GB model (see conftest.py's FakeEncoder)."""

    def encode(self, sentences: list[str], normalize_embeddings: bool, convert_to_numpy: bool) -> object: ...

    def get_embedding_dimension(self) -> int: ...


@dataclass
class EmbedModel:
    """Wraps a loaded SentenceEncoder plus the prefix-dispatch mode, and
    turns a batch of raw input strings + a request model name into a batch
    of L2-normalized embedding vectors.

    L2 normalization (normalize_embeddings=True below) matters beyond just
    "cosine-friendly": internal/engine/index/embed.go's SemanticResult doc
    comment states vec0's bead_embed table (migrations/0004_embed.sql, no
    explicit distance_metric override => sqlite-vec's default, L2/Euclidean)
    ranks by raw L2 distance over the stored vectors, not cosine. For L2
    distance to rank the same as cosine similarity, every stored vector (and
    every query vector) must be unit-norm: ||a-b||^2 = 2 - 2*cos(a,b) when
    ||a||=||b||=1. So normalizing here is required for correct nearest-
    neighbor ranking under this project's existing (unchanged) vec0 schema,
    not an arbitrary choice.
    """

    encoder: SentenceEncoder
    model_name: str
    prefix_mode: PrefixMode = PrefixMode.NONE

    def dimension(self) -> int:
        return self.encoder.get_embedding_dimension()

    def embed(self, request_model: str, inputs: list[str]) -> list[list[float]]:
        """Embeds inputs (already batched by the caller) using request_model
        to pick a task-prefix (see prefix_for). Returns one L2-normalized
        vector per input, same order. Raises ValueError if the encoder's
        native dimension is not EMBED_DIM (a misconfigured model would
        otherwise silently corrupt the vec0 store's fixed FLOAT[768]
        column -- fail loudly here instead)."""
        dim = self.dimension()
        if dim != EMBED_DIM:
            raise ValueError(f"embed model {self.model_name!r} has dimension {dim}, want {EMBED_DIM}")

        prefix = prefix_for(request_model, self.prefix_mode)
        prefixed = [prefix + text for text in inputs]

        vectors = self.encoder.encode(prefixed, normalize_embeddings=True, convert_to_numpy=True)
        return [[float(x) for x in row] for row in vectors]


def load_model(model_name: str, prefix_mode: PrefixMode = PrefixMode.NONE) -> EmbedModel:
    """Loads model_name via sentence_transformers.SentenceTransformer,
    picking MPS (Apple Silicon GPU) over CPU when available -- import is
    local to this function so importing bench.embed_sidecar.model (e.g. from
    a unit test that only needs prefix_for/EmbedModel with a fake encoder)
    never requires torch/sentence-transformers to be installed."""
    import torch
    from sentence_transformers import SentenceTransformer

    device = "mps" if torch.backends.mps.is_available() else "cpu"
    encoder = SentenceTransformer(model_name, device=device)
    return EmbedModel(encoder=encoder, model_name=model_name, prefix_mode=prefix_mode)
