"""Causal/temporal order agreement (R8.3): for temporal_order category
scenarios, does the answering LLM's stated ordering match ground truth?

Scored deterministically by *string containment*, not a second LLM judge
call: bench.scenarios.generate._generate_temporal_order's question is
always the fixed shape "'{earlier}' と '{later}' はどちらが先か?" and its
ground-truth `answer` is always the earlier event's exact display-name
string (see bench/bench/scenarios/generate.py) — so this module appends a
forced-choice instruction (TEMPORAL_ANSWER_PROMPT_SUFFIX) to the answering
prompt asking the LLM to state "A" or "B" for exactly this reason: making
the answer's choice machine-parseable removes any need for fuzzy string
matching or a second judge call to decide "did the LLM name the same event
as ground truth", per the lead's "文字列規約: answer に選択肢 A/B を含める
プロンプト設計にして判定を決定論化".

bench.run wires option_a/option_b from the scenario's own two
evidence-ordering event names before calling the answering LLM (see
bench/bench/run/pipeline.py's temporal_order branch) — this module only
scores an already-produced answer_text against the known-correct option,
it does not itself call an LLM.
"""

from __future__ import annotations

import re
from dataclasses import dataclass

# Appended (lead spec) to the base answer prompt only for temporal_order
# scenarios, so the LLM's raw answer_text always contains a parseable
# "Answer: A" / "Answer: B" line in addition to its normal free-text
# answer — option_a/option_b are filled in by the caller (bench.run) with
# the same two event display-name strings the scenario's question already
# names.
TEMPORAL_ANSWER_PROMPT_SUFFIX = """

This is a temporal-ordering question. After your explanation, on a final line write exactly \
"Answer: A" if {option_a} happened first, or "Answer: B" if {option_b} happened first."""

_CHOICE_RE = re.compile(r"Answer:\s*([AB])\b", re.IGNORECASE)


@dataclass(frozen=True)
class TemporalOrderScore:
    """One temporal_order scenario's agreement result.

    parsed_choice is None when answer_text contains no recognizable
    "Answer: A/B" line (a malformed/refused answer) — agreement is then
    False (an unparseable answer cannot be counted as a correct ordering
    call), and parse_failed is True so a caller can separate "wrong
    ordering" from "could not even parse an ordering" in aggregate
    reporting (two different failure modes, same as
    HallucinationScore.total_claims==0 separating refusal from
    fabrication).
    """

    parsed_choice: str | None
    correct_choice: str
    agrees: bool
    parse_failed: bool

    def to_json_dict(self) -> dict[str, object]:
        return {
            "parsed_choice": self.parsed_choice,
            "correct_choice": self.correct_choice,
            "agrees": self.agrees,
            "parse_failed": self.parse_failed,
        }


def score_temporal_order(answer_text: str, *, correct_choice: str) -> TemporalOrderScore:
    """correct_choice is "A" or "B" (whichever option string bench.run
    substituted for {option_a}/{option_b} matches the scenario's own
    ground-truth `answer` field — see bench/bench/run/pipeline.py)."""
    if correct_choice not in ("A", "B"):
        raise ValueError(f"score_temporal_order: correct_choice must be 'A' or 'B', got {correct_choice!r}")

    match = _CHOICE_RE.search(answer_text)
    parsed = match.group(1).upper() if match else None
    return TemporalOrderScore(
        parsed_choice=parsed,
        correct_choice=correct_choice,
        agrees=(parsed == correct_choice),
        parse_failed=(parsed is None),
    )


__all__ = ["TemporalOrderScore", "score_temporal_order", "TEMPORAL_ANSWER_PROMPT_SUFFIX"]
