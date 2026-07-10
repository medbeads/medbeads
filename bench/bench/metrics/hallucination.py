"""Hallucination rate (R8.3): decompose an LLM answer into atomic claims,
then attribute each claim into one of three buckets, per the lead's spec:

  - "supported": the claim is backed by the *retrieved* context_texts an
    arm actually handed the answering LLM.
  - "context_out_all_in": the claim is NOT in context_texts, but IS
    supported somewhere in the patient's full record (all_record_texts) —
    this is a *retrieval* failure (the right fact exists but this arm
    didn't surface it), not a fabrication, and must be counted separately
    from a true hallucination so R8.3's two failure modes (retrieval
    recall vs. LLM fabrication) don't get conflated into one number.
  - "hallucination": the claim is not supported anywhere in the full
    record — the LLM invented it.

Both decomposition and attribution are done by one LLM judge call per
scenario/arm answer (Triad's Decomposer/Verifier roles collapsed into a
single call here, per the lead's "分解と判定も LLM(judge)で行う" — a pilot-
scale simplification, not a design mistake: keeping it one call keeps the
judge's own JSONL transcript trivially easy to sample for the M2 "judge 一
致率 >85%" human-verification pass, since each row already pairs one
answer with its full claim-by-claim verdict).

JUDGE_PROMPT_TEMPLATE is a versioned constant (sha256'd into
JUDGE_PROMPT_TEMPLATE_VERSION, recorded in run_manifest.json alongside the
answer template's own hash) for the same reason bench.llm.claude's
PROMPT_TEMPLATE is: any edit is visible in every later run's manifest.

Output contract: the judge is instructed to return ONLY a JSON array of
{"claim": str, "attribution": one of the three ATTRIBUTION_* constants}
objects — parsed by _parse_judge_response, which never raises on
malformed/partial JSON (a judge call is itself an LLM call and can
misbehave): unparseable output is scored as zero claims decomposed rather
than crashing the whole run, with the raw text preserved in
HallucinationScore.raw_judge_response for a human reviewer to inspect (the
JSONL transcript already has this too, via TranscriptLogger, but keeping
it on the score object as well means a caller doesn't have to cross-
reference the log file to see why a score came back empty).
"""

from __future__ import annotations

import hashlib
import json
import re
from dataclasses import dataclass

from bench.llm.base import LlmClient

ATTRIBUTION_SUPPORTED = "supported"
ATTRIBUTION_CONTEXT_OUT_ALL_IN = "context_out_all_in"
ATTRIBUTION_HALLUCINATION = "hallucination"

_VALID_ATTRIBUTIONS = frozenset(
    {ATTRIBUTION_SUPPORTED, ATTRIBUTION_CONTEXT_OUT_ALL_IN, ATTRIBUTION_HALLUCINATION}
)

# English (see bench.llm.claude.PROMPT_TEMPLATE's own docstring note: the
# corpus/answers this judges are English). The judge sees two separate text
# pools — RETRIEVED_CONTEXT (what the answering arm actually retrieved) and
# FULL_RECORD (every Bead text for the patient, ground-truth-complete) — so
# it can tell "not in context but true" apart from "not true anywhere".
JUDGE_PROMPT_TEMPLATE = """You are auditing an AI clinical assistant's answer for factual grounding.

Break the ANSWER below into a list of atomic factual claims (each claim should be a single, independently \
checkable statement). For each claim, decide which ONE of the following three labels applies:

- "supported": the claim is directly supported by the RETRIEVED_CONTEXT below.
- "context_out_all_in": the claim is NOT supported by RETRIEVED_CONTEXT, but IS supported somewhere in \
FULL_RECORD (i.e. the fact is true but the retrieval step failed to surface it).
- "hallucination": the claim is not supported anywhere in FULL_RECORD (the assistant invented it).

If the ANSWER is a refusal/non-answer (e.g. "Not stated in the record.") with no factual claims, return an \
empty JSON array.

Respond with ONLY a JSON array of objects, each of the exact form {{"claim": "...", "attribution": "..."}}. \
No other text, no markdown code fences.

RETRIEVED_CONTEXT:
{retrieved_context}

FULL_RECORD:
{full_record}

QUESTION: {question}

ANSWER: {answer}

JSON array:"""

JUDGE_PROMPT_TEMPLATE_VERSION = hashlib.sha256(JUDGE_PROMPT_TEMPLATE.encode("utf-8")).hexdigest()[:16]

_JSON_ARRAY_RE = re.compile(r"\[.*\]", re.DOTALL)


@dataclass(frozen=True)
class ClaimAttribution:
    claim: str
    attribution: str

    def to_json_dict(self) -> dict[str, str]:
        return {"claim": self.claim, "attribution": self.attribution}


@dataclass(frozen=True)
class HallucinationScore:
    """One answer's claim-level breakdown + the summary rate R8.3 asks for.

    hallucination_rate = hallucination_count / total_claims (0.0 when
    total_claims is 0 — a refusal/no-claim answer has nothing to
    hallucinate about, so it should not be penalized as if it were a
    100%-hallucinated answer nor rewarded as a perfect 0%; 0.0 with
    total_claims=0 lets a caller tell the two apart by checking
    total_claims before trusting the rate in isolation).
    """

    claims: list[ClaimAttribution]
    supported_count: int
    context_out_all_in_count: int
    hallucination_count: int
    hallucination_rate: float
    raw_judge_response: str

    @property
    def total_claims(self) -> int:
        return len(self.claims)

    def to_json_dict(self) -> dict[str, object]:
        return {
            "claims": [c.to_json_dict() for c in self.claims],
            "supported_count": self.supported_count,
            "context_out_all_in_count": self.context_out_all_in_count,
            "hallucination_count": self.hallucination_count,
            "total_claims": self.total_claims,
            "hallucination_rate": self.hallucination_rate,
        }


def _parse_judge_response(raw: str) -> list[ClaimAttribution]:
    """Best-effort JSON-array parse — see this module's docstring for why
    this never raises. Tolerates the judge wrapping the array in prose or a
    markdown fence by extracting the first [...] span via regex before
    parsing."""
    text = raw.strip()
    match = _JSON_ARRAY_RE.search(text)
    candidate = match.group(0) if match else text
    try:
        parsed = json.loads(candidate)
    except json.JSONDecodeError:
        return []
    if not isinstance(parsed, list):
        return []

    out: list[ClaimAttribution] = []
    for item in parsed:
        if not isinstance(item, dict):
            continue
        claim = item.get("claim")
        attribution = item.get("attribution")
        if not isinstance(claim, str) or not isinstance(attribution, str):
            continue
        if attribution not in _VALID_ATTRIBUTIONS:
            continue
        out.append(ClaimAttribution(claim=claim, attribution=attribution))
    return out


def _score_from_claims(claims: list[ClaimAttribution], raw_judge_response: str) -> HallucinationScore:
    supported = sum(1 for c in claims if c.attribution == ATTRIBUTION_SUPPORTED)
    context_out = sum(1 for c in claims if c.attribution == ATTRIBUTION_CONTEXT_OUT_ALL_IN)
    hallucinated = sum(1 for c in claims if c.attribution == ATTRIBUTION_HALLUCINATION)
    total = len(claims)
    rate = (hallucinated / total) if total else 0.0
    return HallucinationScore(
        claims=claims,
        supported_count=supported,
        context_out_all_in_count=context_out,
        hallucination_count=hallucinated,
        hallucination_rate=rate,
        raw_judge_response=raw_judge_response,
    )


async def score_hallucination(
    judge: LlmClient,
    *,
    question: str,
    answer_text: str,
    retrieved_context_texts: list[str],
    full_record_texts: list[str],
    scenario_id: str,
    arm: str,
) -> HallucinationScore:
    """Runs one judge call (LlmClient.complete, call_kind="judge") over
    (question, answer_text, retrieved vs. full-record context) and returns
    the parsed 3-way breakdown. judge is any LlmClient (ClaudeClient for a
    real run, FakeLlmClient with judge_responses scripted for tests) — this
    function itself has no backend-specific code, per R8's "フェイク LLM を
    DI できる設計".
    """
    prompt = JUDGE_PROMPT_TEMPLATE.format(
        retrieved_context="\n\n".join(retrieved_context_texts) or "(empty)",
        full_record="\n\n".join(full_record_texts) or "(empty)",
        question=question,
        answer=answer_text,
    )
    result = await judge.complete(
        system="",
        user=prompt,
        scenario_id=scenario_id,
        arm=arm,
        call_kind="judge",
    )
    claims = _parse_judge_response(result.answer_text)
    return _score_from_claims(claims, result.answer_text)


__all__ = [
    "ATTRIBUTION_SUPPORTED",
    "ATTRIBUTION_CONTEXT_OUT_ALL_IN",
    "ATTRIBUTION_HALLUCINATION",
    "JUDGE_PROMPT_TEMPLATE",
    "JUDGE_PROMPT_TEMPLATE_VERSION",
    "ClaimAttribution",
    "HallucinationScore",
    "score_hallucination",
]
