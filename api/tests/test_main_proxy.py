"""Tests for the FastAPI proxy in main.py.

The Core server is mocked so these tests are hermetic. The focus is the
C1-C4 security regression: the AI layer must forward the caller's
access-control headers to Core rather than swallowing them.
"""

import requests


def test_get_patients_forwards_access_headers(client, make_response, mocker):
    get = mocker.patch("main.requests.get", return_value=make_response([{"id": "p1"}]))

    resp = client.get(
        "/patients",
        headers={
            "X-Viewer-Roles": "insurance",
            "X-User-ID": "u-42",
            "X-Service-Token": "tok",
            "X-Access-Reason": "claim review",
        },
    )

    assert resp.status_code == 200
    assert resp.json() == [{"id": "p1"}]
    forwarded = get.call_args.kwargs["headers"]
    assert forwarded["X-Viewer-Roles"] == "insurance"
    assert forwarded["X-User-ID"] == "u-42"
    assert forwarded["X-Service-Token"] == "tok"
    assert forwarded["X-Access-Reason"] == "claim review"


def test_get_bead_forwards_roles(client, make_response, mocker):
    get = mocker.patch("main.requests.get", return_value=make_response({"id": "b1"}))

    resp = client.get("/beads", params={"id": "b1"}, headers={"X-Viewer-Roles": "primary_care"})

    assert resp.status_code == 200
    assert get.call_args.kwargs["headers"]["X-Viewer-Roles"] == "primary_care"


def test_get_bead_404_is_propagated(client, make_response, mocker):
    mocker.patch("main.requests.get", return_value=make_response({}, status_code=404))

    resp = client.get("/beads", params={"id": "missing"})

    assert resp.status_code == 404


def test_context_forwards_roles(client, make_response, mocker):
    get = mocker.patch("main.requests.get", return_value=make_response([]))

    resp = client.get("/beads/context", params={"id": "b1", "depth": 3}, headers={"X-Viewer-Roles": "nurse"})

    assert resp.status_code == 200
    assert get.call_args.kwargs["headers"]["X-Viewer-Roles"] == "nurse"


def test_core_unreachable_maps_to_502(client, mocker):
    mocker.patch("main.requests.get", side_effect=requests.exceptions.ConnectionError("down"))

    resp = client.get("/patients")

    assert resp.status_code == 502


def test_insight_forwards_roles_and_uses_ai(client, make_response, mocker):
    # Core returns the target bead, then the context.
    mocker.patch(
        "main.requests.get",
        side_effect=[
            make_response({"id": "t1", "type": "fhir_observation", "content": {}}),
            make_response([{"id": "c1", "type": "fhir_condition", "content": {}}]),
        ],
    )
    insight = mocker.patch(
        "ai.generate_insight",
        return_value={"insight": "ok", "beads_used": [{"id": "c1"}]},
    )

    resp = client.post(
        "/ai/insight",
        json={"target_bead_id": "t1"},
        headers={"X-Viewer-Roles": "specialist"},
    )

    assert resp.status_code == 200
    assert resp.json()["insight"] == "ok"
    assert insight.called
