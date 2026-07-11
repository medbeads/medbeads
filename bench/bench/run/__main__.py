"""CLI: `uv run python -m bench.run --scenarios <yaml> --data-dir <dir> \
--medbeadsd <bin> --embedder <url> --arms rag,fts,dag \
--budget 2000 --out runs/<name>/`

Runs every scenario x arm pair (resume-aware, skipping any (scenario_id,
arm) already present in <out>/results.jsonl), writing results.jsonl +
run_manifest.json + summary.json into --out (R8.4).

Uses bench.llm.ClaudeClient by default (real Claude API calls — billed);
pass --fake-llm to use bench.llm.FakeLlmClient instead (no network calls,
deterministic — for dry runs/CI/local iteration without spending money).
"""

from __future__ import annotations

import argparse
import asyncio
import sys
from pathlib import Path

from bench.llm.base import LlmClient
from bench.llm.claude import DEFAULT_MAX_TOKENS, DEFAULT_MODEL, ClaudeClient
from bench.llm.fake import FakeLlmClient
from bench.llm.transcript import TranscriptLogger
from bench.run.pipeline import (
    ALL_ARMS,
    FTS_QUERY_MODE_SAFE_WORD,
    FTS_QUERY_MODE_SHARED_SAFE_WORD,
    RunConfig,
    run_bench,
)


def _parse_args(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(prog="python -m bench.run", description=__doc__)
    parser.add_argument("--scenarios", required=True, type=Path, help="scenarios YAML path (bench.scenarios output)")
    parser.add_argument("--data-dir", required=True, type=Path, help="medbeadsd data directory to read from")
    parser.add_argument("--medbeadsd", required=True, type=Path, help="path to the medbeadsd binary")
    parser.add_argument("--out", required=True, type=Path, help="output directory (results/manifest/summary)")
    parser.add_argument(
        "--arms",
        default=",".join(ALL_ARMS),
        help=f"comma-separated arm list (default: {','.join(ALL_ARMS)})",
    )
    parser.add_argument("--budget", type=int, default=2000, help="token budget per retrieve() call (default 2000)")
    parser.add_argument("--embedder", default=None, help="embedding sidecar base URL (required for rag/dag arms)")
    parser.add_argument("--embed-model", default=None, help="passage-side embed model name")
    parser.add_argument("--embed-model-query", default=None, help="query-side embed model name")
    parser.add_argument("--model", default=DEFAULT_MODEL, help=f"Claude model (default {DEFAULT_MODEL})")
    parser.add_argument("--max-tokens", type=int, default=DEFAULT_MAX_TOKENS, help="max_tokens per LLM call")
    parser.add_argument(
        "--no-judge",
        action="store_true",
        help="skip hallucination-rate judge calls entirely (retrieval/token metrics only)",
    )
    parser.add_argument(
        "--fake-llm",
        action="store_true",
        help="use bench.llm.FakeLlmClient instead of the real Claude API (no network calls, no billing)",
    )
    parser.add_argument(
        "--fts-query-mode",
        choices=(FTS_QUERY_MODE_SAFE_WORD, FTS_QUERY_MODE_SHARED_SAFE_WORD),
        default=FTS_QUERY_MODE_SAFE_WORD,
        help=(
            f"'{FTS_QUERY_MODE_SAFE_WORD}' (default): rag gets the full free-text question, "
            f"fts/dag get an FTS5-safe single word. "
            f"'{FTS_QUERY_MODE_SHARED_SAFE_WORD}': every arm, including rag, gets the same "
            "FTS5-safe single word (an input-matched control run — see bench/bench/run/pipeline.py's "
            "docstring above FTS_QUERY_MODE_SAFE_WORD). A full benchmark run should collect BOTH "
            "conditions and report the FTS5-syntax-driven query asymmetry as a Limitations caveat."
        ),
    )
    return parser.parse_args(argv)


def _build_llm(args: argparse.Namespace, transcript: TranscriptLogger) -> tuple[LlmClient, LlmClient | None]:
    if args.fake_llm:
        llm = FakeLlmClient(transcript=transcript, model="fake-llm-v1")
        judge = None if args.no_judge else llm
        return llm, judge
    llm = ClaudeClient(transcript=transcript, model=args.model, max_tokens=args.max_tokens)
    judge = None if args.no_judge else llm
    return llm, judge


def main(argv: list[str] | None = None) -> int:
    args = _parse_args(sys.argv[1:] if argv is None else argv)

    arms = tuple(a.strip() for a in args.arms.split(",") if a.strip())
    config = RunConfig(
        scenarios_path=args.scenarios,
        data_dir=args.data_dir,
        medbeadsd_path=args.medbeadsd,
        out_dir=args.out,
        arms=arms,
        budget=args.budget,
        embedder_url=args.embedder,
        embed_model=args.embed_model,
        embed_model_query=args.embed_model_query,
        run_judge=not args.no_judge,
        fts_query_mode=args.fts_query_mode,
    )

    transcript_path = args.out / "llm_transcript.jsonl"
    with TranscriptLogger(transcript_path) as transcript:
        llm, judge = _build_llm(args, transcript)
        report = asyncio.run(run_bench(config, llm=llm, judge=judge))

    print(
        f"run complete: {report.completed_pairs} pair(s) run, {report.skipped_pairs} skipped (resume), "
        f"{report.total_pairs} total -> {args.out}",
        file=sys.stderr,
    )
    for arm, stats in sorted(report.by_arm.items()):
        print(
            f"  {arm}: pairs={stats['pairs']} scored_this_run={stats['scored_this_run']} "
            f"mean_recall={stats['mean_recall']} mean_precision={stats['mean_precision']} "
            f"tokens={stats['token_usage_total']}",
            file=sys.stderr,
        )

    return 0


if __name__ == "__main__":
    raise SystemExit(main())
