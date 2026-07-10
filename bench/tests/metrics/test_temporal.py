from __future__ import annotations

import pytest

from bench.metrics.temporal import TEMPORAL_ANSWER_PROMPT_SUFFIX, score_temporal_order


def test_score_temporal_order_agrees_on_matching_choice() -> None:
    score = score_temporal_order("The condition came first.\nAnswer: A", correct_choice="A")
    assert score.parsed_choice == "A"
    assert score.agrees is True
    assert score.parse_failed is False


def test_score_temporal_order_disagrees_on_wrong_choice() -> None:
    score = score_temporal_order("Answer: B", correct_choice="A")
    assert score.parsed_choice == "B"
    assert score.agrees is False
    assert score.parse_failed is False


def test_score_temporal_order_case_insensitive_label() -> None:
    score = score_temporal_order("answer: a", correct_choice="A")
    assert score.parsed_choice == "A"
    assert score.agrees is True


def test_score_temporal_order_unparseable_answer_is_parse_failed_and_disagrees() -> None:
    score = score_temporal_order("I'm not sure which happened first.", correct_choice="A")
    assert score.parsed_choice is None
    assert score.parse_failed is True
    assert score.agrees is False


def test_score_temporal_order_rejects_invalid_correct_choice() -> None:
    with pytest.raises(ValueError):
        score_temporal_order("Answer: A", correct_choice="C")


def test_prompt_suffix_renders_both_options() -> None:
    rendered = TEMPORAL_ANSWER_PROMPT_SUFFIX.format(option_a="Hypertension diagnosed", option_b="Lisinopril prescribed")
    assert "Hypertension diagnosed" in rendered
    assert "Lisinopril prescribed" in rendered
    assert "Answer: A" in rendered
    assert "Answer: B" in rendered
