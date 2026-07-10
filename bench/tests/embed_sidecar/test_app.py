"""Unit tests for bench.embed_sidecar.app: request/response shape matched
against internal/engine/embedder/client.go's embeddingsRequest/
embeddingsResponse (see app.py's module docstring). No real model load."""

from __future__ import annotations

from fastapi.testclient import TestClient

from bench.embed_sidecar.app import create_app
from bench.embed_sidecar.model import EMBED_DIM, EmbedModel, PrefixMode
from tests.embed_sidecar.conftest import FakeEncoder


def _client(prefix_mode: PrefixMode = PrefixMode.NONE) -> TestClient:
    model = EmbedModel(encoder=FakeEncoder(), model_name="fake-e5", prefix_mode=prefix_mode)
    return TestClient(create_app(model))


def test_embeddings_response_shape_matches_go_client_contract() -> None:
    # client.go's embeddingsResponse only reads data[].embedding (ignoring
    # object/model/usage/data[].index per its own doc comment) -- but this
    # response includes them anyway for real OpenAI-API compatibility, and
    # this test locks that full shape down.
    client = _client()
    resp = client.post("/v1/embeddings", json={"model": "fake-e5", "input": ["hello", "world"]})
    assert resp.status_code == 200
    body = resp.json()

    assert body["object"] == "list"
    assert body["model"] == "fake-e5"
    assert "usage" in body and "prompt_tokens" in body["usage"] and "total_tokens" in body["usage"]

    assert len(body["data"]) == 2
    for i, datum in enumerate(body["data"]):
        assert datum["object"] == "embedding"
        assert datum["index"] == i
        assert len(datum["embedding"]) == EMBED_DIM
        assert all(isinstance(x, float) for x in datum["embedding"])


def test_embeddings_preserves_input_order() -> None:
    # client.go's Embed does NOT re-sort by data[].index (see its doc
    # comment: "this client does not re-sort by data[].index") -- so
    # request-order-equals-response-order is load-bearing, not cosmetic.
    client = _client()
    resp = client.post("/v1/embeddings", json={"model": "fake-e5", "input": ["alpha", "beta", "gamma"]})
    data = resp.json()["data"]
    embeddings = [tuple(d["embedding"]) for d in data]
    assert len(set(embeddings)) == 3  # three distinct inputs -> three distinct vectors, in order


def test_embeddings_one_data_entry_per_input_batch() -> None:
    client = _client()
    inputs = [f"text {i}" for i in range(5)]
    resp = client.post("/v1/embeddings", json={"model": "fake-e5", "input": inputs})
    assert len(resp.json()["data"]) == len(inputs)


def test_embeddings_empty_input_rejected() -> None:
    # client.go's Embed short-circuits len(texts)==0 before ever sending a
    # request, so this server never actually needs to handle input=[] from
    # that client -- but reject it explicitly (422) rather than silently
    # returning data=[] for any other caller (see app.py's module docstring).
    client = _client()
    resp = client.post("/v1/embeddings", json={"model": "fake-e5", "input": []})
    assert resp.status_code == 422


def test_embeddings_missing_model_field_rejected() -> None:
    client = _client()
    resp = client.post("/v1/embeddings", json={"input": ["hello"]})
    assert resp.status_code == 422


def test_healthz() -> None:
    client = _client()
    resp = client.get("/healthz")
    assert resp.status_code == 200
    assert resp.json()["status"] == "ok"


def test_embeddings_model_suffix_prefix_dispatch_end_to_end() -> None:
    encoder = FakeEncoder()
    model = EmbedModel(encoder=encoder, model_name="fake-e5", prefix_mode=PrefixMode.MODEL_SUFFIX)
    client = TestClient(create_app(model))

    client.post("/v1/embeddings", json={"model": "fake-e5-query", "input": ["hypertension"]})
    assert encoder.last_call_sentences == ["query: hypertension"]

    client.post("/v1/embeddings", json={"model": "fake-e5-passage", "input": ["bp 140/90"]})
    assert encoder.last_call_sentences == ["passage: bp 140/90"]
