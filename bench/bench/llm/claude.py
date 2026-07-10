"""ClaudeClient: the real LlmClient implementation (anthropic SDK,
AsyncAnthropic) — R8.3's "answer(question, context_texts) ->
{answer_text, usage}"薄いクライアント, temperature=0 fixed, max_tokens
configurable, every round-trip logged via TranscriptLogger.

Retry/timeout: the anthropic SDK's own AsyncAnthropic already retries
RateLimitError/APIConnectionError/5xx with exponential backoff
(`max_retries`, VERIFIED via anthropic 0.116.0's client constructor
signature) and enforces a per-request timeout (`timeout`) — this module
configures both explicitly (rather than reimplementing retry/backoff by
hand) and additionally logs a failed-after-retries call to the transcript
with `error` set, so a real run's JSONL log is a complete record of every
attempt's *outcome* even when the SDK's own retries happened invisibly
underneath.

Prompt template versioning: PROMPT_TEMPLATE is a module-level constant
(never f-string-assembled ad hoc at the call site) so run_manifest.json can
record its sha256 (R8.4's "プロンプト hash") — any future template edit
naturally changes PROMPT_TEMPLATE_VERSION's hash and is visible in every
subsequent run's manifest without a separate changelog to keep in sync.
"""

from __future__ import annotations

import hashlib
from typing import TYPE_CHECKING

from bench.llm.base import AnswerResult
from bench.llm.transcript import TranscriptLogger, TranscriptRow, now_iso

if TYPE_CHECKING:
    import anthropic

DEFAULT_MODEL = "claude-sonnet-5"
DEFAULT_MAX_TOKENS = 1024
DEFAULT_TIMEOUT_S = 60.0
DEFAULT_MAX_RETRIES = 5

# English (Synthea's source data is English-only — see bench/README.md's
# embedding-sidecar rationale for the same "corpus is English" constraint;
# a Japanese-phrased instruction would be an unfair-to-neither-arm but
# still avoidable translation hop for an English-only benchmark corpus).
# {context} is every context_texts item joined by blank lines, in the
# order the calling Retriever's own RetrievalResult.texts returned them
# (already the arm's own priority order — this template does not re-rank).
PROMPT_TEMPLATE = """You are answering a clinical question using ONLY the patient record excerpts provided below. \
Do not use any outside knowledge. If the excerpts do not contain enough information to answer, respond exactly: \
"Not stated in the record." Answer concisely, in one or two sentences.

Patient record excerpts:
{context}

Question: {question}

Answer:"""

PROMPT_TEMPLATE_VERSION = hashlib.sha256(PROMPT_TEMPLATE.encode("utf-8")).hexdigest()[:16]


def render_answer_prompt(question: str, context_texts: list[str]) -> str:
    context = "\n\n".join(t for t in context_texts if t) or "(no context retrieved)"
    return PROMPT_TEMPLATE.format(context=context, question=question)


class ClaudeClient:
    """LlmClient implementation over anthropic.AsyncAnthropic.

    api_key is read by the SDK itself from the ANTHROPIC_API_KEY env var
    when not passed explicitly (anthropic.AsyncAnthropic's own default) —
    this class never reads the env var directly, so "no key configured"
    surfaces as the SDK's own AuthenticationError on first call, not a
    silently-different code path here.
    """

    def __init__(
        self,
        *,
        transcript: TranscriptLogger,
        model: str = DEFAULT_MODEL,
        max_tokens: int = DEFAULT_MAX_TOKENS,
        timeout_s: float = DEFAULT_TIMEOUT_S,
        max_retries: int = DEFAULT_MAX_RETRIES,
        api_key: str | None = None,
    ) -> None:
        import anthropic as _anthropic

        self.model = model
        self._max_tokens = max_tokens
        self._transcript = transcript
        self._client: anthropic.AsyncAnthropic = _anthropic.AsyncAnthropic(
            api_key=api_key,
            timeout=timeout_s,
            max_retries=max_retries,
        )

    async def answer(
        self, *, question: str, context_texts: list[str], scenario_id: str, arm: str
    ) -> AnswerResult:
        prompt = render_answer_prompt(question, context_texts)
        return await self.complete(
            system="",
            user=prompt,
            scenario_id=scenario_id,
            arm=arm,
            call_kind="answer",
        )

    async def complete(
        self, *, system: str, user: str, scenario_id: str, arm: str, call_kind: str
    ) -> AnswerResult:
        # kwargs IS the request logged below (same dict, not a
        # separately-hand-assembled lookalike) — data-reviewer note:
        # logging a *reconstructed* request dict here previously drifted
        # from the actual anthropic SDK call (it always included a
        # "system"/"user" key pair the real request never sends in that
        # shape: system is a top-level kwarg only when non-empty, and the
        # real payload key is "messages", not "user"). A transcript row's
        # `request` field must be an honest record of what was actually
        # sent over the wire, not an approximation.
        kwargs: dict = {
            "model": self.model,
            "max_tokens": self._max_tokens,
            "temperature": 0,
            "messages": [{"role": "user", "content": user}],
        }
        if system:
            kwargs["system"] = system

        response_text = ""
        input_tokens = 0
        output_tokens = 0
        error: str | None = None
        try:
            message = await self._client.messages.create(**kwargs)
            response_text = "".join(
                block.text for block in message.content if getattr(block, "type", None) == "text"
            )
            input_tokens = message.usage.input_tokens
            output_tokens = message.usage.output_tokens
        except Exception as exc:  # noqa: BLE001 - logged then re-raised, never swallowed
            error = f"{type(exc).__name__}: {exc}"
            raise
        finally:
            self._transcript.log(
                TranscriptRow(
                    timestamp=now_iso(),
                    scenario_id=scenario_id,
                    arm=arm,
                    call_kind=call_kind,
                    model=self.model,
                    prompt_template_version=PROMPT_TEMPLATE_VERSION,
                    request=kwargs,
                    response_text=response_text,
                    input_tokens=input_tokens,
                    output_tokens=output_tokens,
                    error=error,
                )
            )

        return AnswerResult(
            answer_text=response_text,
            input_tokens=input_tokens,
            output_tokens=output_tokens,
            meta={"model": self.model},
        )
