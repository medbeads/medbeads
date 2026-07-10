"""FakeLlmClient: a deterministic LlmClient double for tests (R8's "フェイク
LLM(決定論、コンテキスト内の文字列から answer を合成)を DI できる設計" and
"パイプライン全体の e2e テストはフェイクで").

Two independent behaviors, selected by call_kind (mirrors ClaudeClient's own
answer()->complete(call_kind="answer") shape, so bench.run's orchestration
code never needs to know which backend it is talking to):

  - "answer": synthesizes an answer by concatenating the first
    `max_context_sentences` sentence-like fragments across context_texts
    (i.e. a real substring of the given context, never invented text) —
    the whole point is that this is *not* an LLM: it can never hallucinate
    by construction, since it only ever echoes context bytes.  If
    context_texts is empty, returns the fixed "Not stated in the record."
    string (mirrors ClaudeClient's own PROMPT_TEMPLATE instruction for the
    no-context case), so run-pipeline e2e tests see the same string shape
    a real budget=0 arm would produce.
  - "judge" (or any other call_kind): returns a scripted response looked up
    from `judge_responses` (a caller-supplied dict keyed by the *user*
    prompt string, exact match) — this is how tests fake fixed judge
    outputs (e.g. bench.metrics.hallucination's 3-way classification) end
    to end without a real judge call. Missing keys raise KeyError with the
    prompt text included in the message, so a test's fixture and the
    module-under-test's actual prompt text going out of sync fails loudly
    at the call site rather than as a silent default.

usage (input_tokens/output_tokens) is synthesized deterministically as
len(text)//4 (an arbitrary-but-stable stand-in "token" count, not a real
tokenizer — it only needs to be deterministic and monotonic in text length
for run-pipeline e2e tests to assert non-trivial usage sums), so token-
efficiency-metric tests can assert real, non-zero, reproducible numbers
without depending on a live API call.
"""

from __future__ import annotations

import re

from bench.llm.base import AnswerResult
from bench.llm.transcript import TranscriptLogger, TranscriptRow, now_iso

_SENTENCE_RE = re.compile(r"[^.!?\n]+[.!?]?")


def _fake_token_count(text: str) -> int:
    return max(1, len(text) // 4)


def _synthesize_answer(context_texts: list[str], *, max_context_sentences: int) -> str:
    if not context_texts:
        return "Not stated in the record."
    fragments: list[str] = []
    for text in context_texts:
        for match in _SENTENCE_RE.finditer(text):
            fragment = match.group(0).strip()
            if fragment:
                fragments.append(fragment)
            if len(fragments) >= max_context_sentences:
                break
        if len(fragments) >= max_context_sentences:
            break
    if not fragments:
        return "Not stated in the record."
    return " ".join(fragments)


class FakeLlmClient:
    """Deterministic LlmClient double — see this module's docstring."""

    def __init__(
        self,
        *,
        transcript: TranscriptLogger,
        model: str = "fake-llm-v1",
        max_context_sentences: int = 2,
        judge_responses: dict[str, str] | None = None,
    ) -> None:
        self.model = model
        self._transcript = transcript
        self._max_context_sentences = max_context_sentences
        self._judge_responses = judge_responses or {}
        # Every (scenario_id, arm, call_kind) tuple this fake has answered,
        # in call order — a test-only introspection aid (e.g. asserting
        # call counts/resume behavior) with no effect on answer() output.
        self.calls: list[tuple[str, str, str]] = []

    async def answer(
        self, *, question: str, context_texts: list[str], scenario_id: str, arm: str
    ) -> AnswerResult:
        answer_text = _synthesize_answer(context_texts, max_context_sentences=self._max_context_sentences)
        request_text = f"Q: {question}\nCONTEXT: {' | '.join(context_texts)}"
        return self._respond(
            request_text=request_text,
            response_text=answer_text,
            scenario_id=scenario_id,
            arm=arm,
            call_kind="answer",
        )

    async def complete(
        self, *, system: str, user: str, scenario_id: str, arm: str, call_kind: str
    ) -> AnswerResult:
        if call_kind == "answer":
            # Same contract as answer() when called this way directly
            # (bench.run may route through complete() uniformly) — but
            # answer() is the normal call path; complete(call_kind="answer")
            # is not separately scripted here since answer() already covers
            # it.
            response_text = _synthesize_answer([user], max_context_sentences=self._max_context_sentences)
        elif user in self._judge_responses:
            response_text = self._judge_responses[user]
        else:
            raise KeyError(
                f"FakeLlmClient: no scripted judge_responses entry for prompt (call_kind={call_kind!r}): {user!r}"
            )
        return self._respond(
            request_text=f"SYSTEM: {system}\nUSER: {user}",
            response_text=response_text,
            scenario_id=scenario_id,
            arm=arm,
            call_kind=call_kind,
        )

    def _respond(
        self, *, request_text: str, response_text: str, scenario_id: str, arm: str, call_kind: str
    ) -> AnswerResult:
        self.calls.append((scenario_id, arm, call_kind))
        input_tokens = _fake_token_count(request_text)
        output_tokens = _fake_token_count(response_text)
        self._transcript.log(
            TranscriptRow(
                timestamp=now_iso(),
                scenario_id=scenario_id,
                arm=arm,
                call_kind=call_kind,
                model=self.model,
                prompt_template_version="fake",
                request={"text": request_text},
                response_text=response_text,
                input_tokens=input_tokens,
                output_tokens=output_tokens,
            )
        )
        return AnswerResult(
            answer_text=response_text,
            input_tokens=input_tokens,
            output_tokens=output_tokens,
            meta={"model": self.model, "fake": True},
        )
