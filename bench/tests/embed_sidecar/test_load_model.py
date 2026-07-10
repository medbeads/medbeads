"""Real-model-load tests for bench.embed_sidecar.model.load_model: actually
downloads/loads intfloat/multilingual-e5-base via sentence-transformers.
Marked slow_model and skipped by default (CI has no model cache and this
task's environment must not be assumed to always have network/disk budget
for a ~1GB download) -- opt in with MEDBEADS_RUN_SLOW_MODEL_TESTS=1.
"""

from __future__ import annotations

import os

import pytest

from bench.embed_sidecar.model import DEFAULT_MODEL_NAME, EMBED_DIM, PrefixMode, load_model

pytestmark = pytest.mark.slow_model

RUN_SLOW = os.environ.get("MEDBEADS_RUN_SLOW_MODEL_TESTS") == "1"


@pytest.mark.skipif(not RUN_SLOW, reason="set MEDBEADS_RUN_SLOW_MODEL_TESTS=1 to run (downloads/loads a real ~1GB model)")
def test_load_default_model_has_expected_dimension() -> None:
    model = load_model(DEFAULT_MODEL_NAME, prefix_mode=PrefixMode.NONE)
    assert model.dimension() == EMBED_DIM


@pytest.mark.skipif(not RUN_SLOW, reason="set MEDBEADS_RUN_SLOW_MODEL_TESTS=1 to run (downloads/loads a real ~1GB model)")
def test_load_default_model_embeds_and_normalizes() -> None:
    import math

    model = load_model(DEFAULT_MODEL_NAME, prefix_mode=PrefixMode.NONE)
    vectors = model.embed(DEFAULT_MODEL_NAME, ["hypertension", "high blood pressure"])
    assert len(vectors) == 2
    for v in vectors:
        assert len(v) == EMBED_DIM
        norm = math.sqrt(sum(x * x for x in v))
        assert norm == pytest.approx(1.0, abs=1e-4)
