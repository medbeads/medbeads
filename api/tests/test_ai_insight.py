"""Tests for ai.generate_insight with the Gemini model mocked out."""

from unittest.mock import Mock

import ai


def test_generate_insight_without_api_key(monkeypatch):
    monkeypatch.setattr(ai, "API_KEY", None)
    result = ai.generate_insight({"type": "fhir_observation", "content": {}}, [])
    assert "API Key is not set" in result["insight"]
    assert result["beads_used"] == []


def test_generate_insight_with_mocked_model(monkeypatch):
    monkeypatch.setattr(ai, "API_KEY", "fake-key")

    fake_model = Mock()
    fake_model.generate_content.return_value = Mock(text="**Summary**: looks fine")
    monkeypatch.setattr(ai.genai, "GenerativeModel", lambda *a, **k: fake_model)

    context = [
        {"id": "b1", "type": "fhir_condition", "content": {"code": {"text": "Diabetes"}}},
    ]
    result = ai.generate_insight({"type": "fhir_observation", "content": {}}, context)

    assert result["insight"] == "**Summary**: looks fine"
    assert len(result["beads_used"]) == 1
    assert result["beads_used"][0]["id"] == "b1"
    assert result["beads_used"][0]["description"] == "Diabetes"


def test_generate_insight_handles_model_exception(monkeypatch):
    monkeypatch.setattr(ai, "API_KEY", "fake-key")

    fake_model = Mock()
    fake_model.generate_content.side_effect = RuntimeError("quota exceeded")
    monkeypatch.setattr(ai.genai, "GenerativeModel", lambda *a, **k: fake_model)

    result = ai.generate_insight({"type": "fhir_observation", "content": {}}, [])

    assert "AI Inference Failed" in result["insight"]
    assert "quota exceeded" in result["insight"]
