"""TranscriptLogger: append-only JSONL writer for every LLM round-trip
(answer calls AND judge calls — see bench.metrics.hallucination), per the
lead's "全往復を JSONL 記録(request/response/usage/timestamp/scenario_id/
arm)" and "judge の入出力も JSONL 記録(M2 完了基準「judge 一致率 >85%」の人手
検証用サンプル抽出を可能に)".

One row per call, written the moment the call returns (success or
exception — see log_call's try/finally) so a run that dies partway through
still leaves a complete, resumable transcript of every call that actually
happened (mirrors bench.ingest.run's manifest.jsonl "write as you go, flush
every row" discipline).
"""

from __future__ import annotations

import json
import threading
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Any


@dataclass(frozen=True)
class TranscriptRow:
    """One JSONL row's full field set. call_kind distinguishes an
    answer-prompt call ("answer") from a hallucination-judge call ("judge")
    within the same log file/schema — the lead's own two use cases for "全
    往復を JSONL 記録", kept in one file rather than two so a chronological
    replay of one run's LLM traffic never needs to interleave two separate
    files by timestamp.
    """

    timestamp: str
    scenario_id: str
    arm: str
    call_kind: str
    model: str
    prompt_template_version: str
    request: dict[str, Any]
    response_text: str
    input_tokens: int
    output_tokens: int
    error: str | None = None

    def to_json_dict(self) -> dict[str, Any]:
        return {
            "timestamp": self.timestamp,
            "scenario_id": self.scenario_id,
            "arm": self.arm,
            "call_kind": self.call_kind,
            "model": self.model,
            "prompt_template_version": self.prompt_template_version,
            "request": self.request,
            "response_text": self.response_text,
            "input_tokens": self.input_tokens,
            "output_tokens": self.output_tokens,
            "error": self.error,
        }


class TranscriptLogger:
    """Appends TranscriptRow objects to path as JSONL, one line per call,
    flushed immediately (the log is the audit trail a >$0 API run must not
    lose on a crash — see this module's docstring). Thread/asyncio-task
    safe via a plain Lock: bench.run's orchestration awaits calls
    sequentially per (scenario, arm) but nothing here assumes that stays
    true forever (e.g. a future concurrent-arms optimization), so the write
    itself is guarded rather than relying on caller discipline.
    """

    def __init__(self, path: Path) -> None:
        self._path = path
        self._lock = threading.Lock()
        path.parent.mkdir(parents=True, exist_ok=True)
        # Open in append mode: a resumed run (R8.4's resume contract) must
        # never truncate a transcript from an earlier, already-billed
        # partial run.
        self._fh = path.open("a", encoding="utf-8")

    def log(self, row: TranscriptRow) -> None:
        with self._lock:
            self._fh.write(json.dumps(row.to_json_dict(), sort_keys=True, ensure_ascii=False))
            self._fh.write("\n")
            self._fh.flush()

    def close(self) -> None:
        with self._lock:
            self._fh.close()

    def __enter__(self) -> "TranscriptLogger":
        return self

    def __exit__(self, exc_type, exc, tb) -> None:
        self.close()


def now_iso() -> str:
    return datetime.now(timezone.utc).isoformat()
