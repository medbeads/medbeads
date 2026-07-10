"""bench.run: `uv run python -m bench.run` — scenarios x arms orchestration
(R8.4). See bench/bench/run/pipeline.py for the run loop and
bench/bench/run/__main__.py for the CLI.
"""

from __future__ import annotations

from bench.run.pipeline import (
    ALL_ARMS,
    FTS_QUERY_MODE_SAFE_WORD,
    FTS_QUERY_MODE_SHARED_SAFE_WORD,
    ArmResult,
    RunConfig,
    fts_safe_query,
    load_completed_keys,
    run_bench,
)

__all__ = [
    "ALL_ARMS",
    "ArmResult",
    "RunConfig",
    "load_completed_keys",
    "run_bench",
    "fts_safe_query",
    "FTS_QUERY_MODE_SAFE_WORD",
    "FTS_QUERY_MODE_SHARED_SAFE_WORD",
]
