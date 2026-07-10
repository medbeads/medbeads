"""Fixtures for bench.retrieval's integration tests: a real (socket-bound)
embedding sidecar backed by embed_sidecar's FakeEncoder double (see
tests/embed_sidecar/conftest.py) — no real sentence-transformers model
load/download needed, but a genuine HTTP server on 127.0.0.1 that a spawned
`medbeadsd serve -embedder <url>` subprocess can actually reach (unlike
FastAPI's in-process TestClient, which only test_app.py's own unit tests
use).
"""

from __future__ import annotations

import socket
import threading
import time
from collections.abc import Iterator

import httpx
import pytest
import uvicorn

from bench.embed_sidecar.app import create_app
from bench.embed_sidecar.model import EmbedModel, PrefixMode
from tests.embed_sidecar.conftest import FakeEncoder


def _free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


@pytest.fixture
def fake_embed_sidecar_url() -> Iterator[str]:
    """Starts embed_sidecar's FastAPI app (FakeEncoder-backed, EMBED_DIM=768
    so it matches index.db's vec0 column width exactly) on a real
    127.0.0.1:<free port> uvicorn server, in a background thread, for the
    duration of one test. Yields the base URL (e.g.
    "http://127.0.0.1:54321"); tears the server down on fixture exit.
    """
    model = EmbedModel(encoder=FakeEncoder(), model_name="fake-e5", prefix_mode=PrefixMode.NONE)
    app = create_app(model)

    port = _free_port()
    config = uvicorn.Config(app, host="127.0.0.1", port=port, log_level="warning")
    server = uvicorn.Server(config)

    thread = threading.Thread(target=server.run, daemon=True)
    thread.start()

    base_url = f"http://127.0.0.1:{port}"
    deadline = time.monotonic() + 10.0
    last_exc: Exception | None = None
    while time.monotonic() < deadline:
        try:
            resp = httpx.get(f"{base_url}/healthz", timeout=1.0)
            if resp.status_code == 200:
                break
        except Exception as exc:  # noqa: BLE001 - retry until deadline
            last_exc = exc
        time.sleep(0.05)
    else:
        raise RuntimeError(f"fake_embed_sidecar_url: server did not become healthy in time: {last_exc}")

    try:
        yield base_url
    finally:
        server.should_exit = True
        thread.join(timeout=5.0)
