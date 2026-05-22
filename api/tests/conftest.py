"""Shared pytest fixtures for the MedBeads AI API tests.

Importing ``main`` runs ``ai`` at module load, which reads GEMINI_API_KEY. We
force it empty *before* the import so tests never configure or call the real
Gemini API, and behavior is deterministic in CI.
"""

import os
import sys
from pathlib import Path
from unittest.mock import Mock

import pytest

# Force no real API key before the application modules are imported.
os.environ["GEMINI_API_KEY"] = ""

# Make main.py / ai.py (one directory up) importable as top-level modules.
sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

from fastapi.testclient import TestClient  # noqa: E402
import main  # noqa: E402


@pytest.fixture
def client():
    """A FastAPI TestClient for the AI API app."""
    return TestClient(main.app)


@pytest.fixture
def make_response():
    """Builds a fake ``requests`` response object for mocking Core calls."""

    def _make(json_data, status_code=200):
        resp = Mock()
        resp.status_code = status_code
        resp.json.return_value = json_data
        resp.raise_for_status.return_value = None
        return resp

    return _make
