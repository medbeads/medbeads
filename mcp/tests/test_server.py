import pytest
import requests

import core_client
import server


class FakeResp:
    def __init__(self, status_code=200, json_data=None, text=""):
        self.status_code = status_code
        self._json = json_data if json_data is not None else {}
        self.text = text

    def json(self):
        return self._json


# --- viewer_headers: roles come from the environment, not the agent ---

def test_viewer_headers_defaults():
    h = core_client.viewer_headers()
    assert h["X-Viewer-Roles"] == "primary_care"
    assert h["X-User-ID"] == "mcp-agent"
    assert "X-Service-Token" not in h
    assert "X-Access-Reason" not in h


def test_viewer_headers_from_env(monkeypatch):
    monkeypatch.setenv("MEDBEADS_VIEWER_ROLES", "specialist,dept:genetics")
    monkeypatch.setenv("MEDBEADS_USER_ID", "dr-sato")
    monkeypatch.setenv("MEDBEADS_SERVICE_TOKEN", "tok")
    monkeypatch.setenv("MEDBEADS_ACCESS_REASON", "audit")
    h = core_client.viewer_headers()
    assert h["X-Viewer-Roles"] == "specialist,dept:genetics"
    assert h["X-User-ID"] == "dr-sato"
    assert h["X-Service-Token"] == "tok"
    assert h["X-Access-Reason"] == "audit"


# --- _get error mapping ---

def test_get_maps_404(mocker):
    mocker.patch("core_client.requests.get", return_value=FakeResp(404, text="nope"))
    with pytest.raises(core_client.CoreError):
        core_client.get_bead("missing")


def test_get_maps_http_error(mocker):
    mocker.patch("core_client.requests.get", return_value=FakeResp(500, text="boom"))
    with pytest.raises(core_client.CoreError):
        core_client.list_patients()


def test_get_maps_connection_error(mocker):
    mocker.patch(
        "core_client.requests.get",
        side_effect=requests.exceptions.ConnectionError("down"),
    )
    with pytest.raises(core_client.CoreError):
        core_client.list_patients()


# --- core_client request shaping ---

def test_list_patients_forwards_viewer_headers(mocker):
    get = mocker.patch(
        "core_client.requests.get", return_value=FakeResp(200, [{"id": "p1"}])
    )
    assert core_client.list_patients() == [{"id": "p1"}]
    assert get.call_args[0][0].endswith("/patients")
    assert get.call_args.kwargs["headers"]["X-Viewer-Roles"] == "primary_care"


def test_search_beads_params(mocker):
    get = mocker.patch("core_client.requests.get", return_value=FakeResp(200, []))
    core_client.search_beads("diabetes", "fhir_condition")
    assert get.call_args.kwargs["params"] == {
        "q": "diabetes",
        "resourceTypes": "fhir_condition",
    }


def test_get_context_params(mocker):
    get = mocker.patch("core_client.requests.get", return_value=FakeResp(200, []))
    core_client.get_context("bead-1", depth=10)
    assert get.call_args[0][0].endswith("/beads/context")
    assert get.call_args.kwargs["params"] == {"id": "bead-1", "depth": 10}


def test_patient_timeline_uses_reverse_lookup(mocker):
    get = mocker.patch("core_client.requests.get", return_value=FakeResp(200, []))
    core_client.get_patient_timeline("p1")
    params = get.call_args.kwargs["params"]
    assert params["id"] == "p1"
    assert params["lookup"] == "reverse"


# --- server tools delegate to core_client ---

def test_server_get_context_delegates(mocker):
    mocker.patch("core_client.get_context", return_value=[{"id": "ancestor"}])
    assert server.get_context("b1", 3) == [{"id": "ancestor"}]


def test_server_list_patients_delegates(mocker):
    mocker.patch("core_client.list_patients", return_value=[{"id": "p1"}])
    assert server.list_patients() == [{"id": "p1"}]


def test_server_tools_are_defined():
    for name in (
        "list_patients",
        "search_beads",
        "get_bead",
        "get_context",
        "get_patient_timeline",
        "get_resource_counts",
    ):
        assert callable(getattr(server, name)), f"{name} should be a callable tool"
    assert server.mcp is not None
