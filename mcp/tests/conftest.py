import pytest


@pytest.fixture(autouse=True)
def clean_viewer_env(monkeypatch):
    """Start each test from a known viewer context (no role/token leakage)."""
    for key in (
        "MEDBEADS_VIEWER_ROLES",
        "MEDBEADS_USER_ID",
        "MEDBEADS_SERVICE_TOKEN",
        "MEDBEADS_ACCESS_REASON",
    ):
        monkeypatch.delenv(key, raising=False)
