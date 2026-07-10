"""FastAPI app: POST /v1/embeddings, OpenAI-compatible, shape matched
directly against internal/engine/embedder/client.go (the Go client this
sidecar serves) -- that file is the contract owner, not this one.

Request (client.go's embeddingsRequest, lines 64-67):
    {"model": "...", "input": ["...", ...]}

Response (client.go's embeddingsResponse, lines 78-82, plus the extra
`object`/`usage` fields client.go documents as "accepted but ignored" --
included here anyway for genuine OpenAI-API compatibility, since other
tooling besides this one Go client may hit this sidecar):
    {
      "object": "list",
      "data": [{"object": "embedding", "embedding": [float, ...], "index": int}, ...],
      "model": "...",
      "usage": {"prompt_tokens": int, "total_tokens": int}
    }

client.go's Embed (line 90) treats len(texts)==0 as a no-op that never
makes an HTTP request, so this server never needs to special-case an empty
`input` array from that client -- but a malformed/empty `input` from any
other caller is rejected with 422 by Pydantic's min_length=1 validator
below rather than silently returning an empty data[] (matching client.go's
own "len(parsed.Data) != len(texts) is an error" strictness in spirit: a
request this server cannot honor should fail loudly, not degrade).
"""

from __future__ import annotations

from typing import Literal

from fastapi import FastAPI
from pydantic import BaseModel, Field

from bench.embed_sidecar.model import EmbedModel


class EmbeddingsRequest(BaseModel):
    model: str
    input: list[str] = Field(min_length=1)


class EmbeddingDatum(BaseModel):
    object: Literal["embedding"] = "embedding"
    embedding: list[float]
    index: int


class Usage(BaseModel):
    prompt_tokens: int
    total_tokens: int


class EmbeddingsResponse(BaseModel):
    object: Literal["list"] = "list"
    data: list[EmbeddingDatum]
    model: str
    usage: Usage


def create_app(embed_model: EmbedModel) -> FastAPI:
    """Builds the FastAPI app around an already-loaded EmbedModel. Kept as a
    factory (rather than a module-level app singleton) so tests can inject a
    fake EmbedModel (no real model load) and __main__.py can inject a real
    one loaded from CLI flags."""
    app = FastAPI(title="medbeads-embed-sidecar")

    @app.post("/v1/embeddings", response_model=EmbeddingsResponse)
    def embeddings(req: EmbeddingsRequest) -> EmbeddingsResponse:
        vectors = embed_model.embed(req.model, req.input)
        prompt_tokens = sum(len(text.split()) for text in req.input)
        return EmbeddingsResponse(
            data=[EmbeddingDatum(embedding=vec, index=i) for i, vec in enumerate(vectors)],
            model=req.model,
            usage=Usage(prompt_tokens=prompt_tokens, total_tokens=prompt_tokens),
        )

    @app.get("/healthz")
    def healthz() -> dict[str, str]:
        return {"status": "ok", "model": embed_model.model_name}

    return app
