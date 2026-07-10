"""CLI: `uv run python -m bench.embed_sidecar [--model NAME] [--port N]
[--host HOST] [--prefix-mode {none,model_suffix}]`

Starts the OpenAI-compatible /v1/embeddings sidecar (see app.py, model.py)
over uvicorn. Loads the model once at startup (blocking -- the first run
against an uncached model name downloads it, ~1GB for
intfloat/multilingual-e5-base, so startup can take a while the first time).
"""

from __future__ import annotations

import argparse
import logging
import sys

from bench.embed_sidecar.app import create_app
from bench.embed_sidecar.model import DEFAULT_MODEL_NAME, PrefixMode, load_model

logging.basicConfig(level=logging.INFO, format="%(levelname)s %(name)s: %(message)s", stream=sys.stderr)
logger = logging.getLogger(__name__)


def _parse_args(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(prog="python -m bench.embed_sidecar", description=__doc__)
    parser.add_argument("--model", default=DEFAULT_MODEL_NAME, help=f"sentence-transformers model name (default: {DEFAULT_MODEL_NAME})")
    parser.add_argument("--host", default="127.0.0.1", help="bind host (default: 127.0.0.1)")
    parser.add_argument("--port", type=int, default=18100, help="bind port (default: 18100)")
    parser.add_argument(
        "--prefix-mode",
        choices=[m.value for m in PrefixMode],
        default=PrefixMode.NONE.value,
        help="E5 query/passage prefix dispatch strategy -- see bench/embed_sidecar/model.py's module "
        "docstring for why 'none' (no prefix, symmetric) is the safe default given medbeadsd serve's "
        "current single-model-string-for-both-roles behavior; 'model_suffix' is opt-in for future use "
        "once/if the Go side sends distinct model names per role (default: none)",
    )
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    args = _parse_args(sys.argv[1:] if argv is None else argv)

    logger.info("loading model %s (this may download ~1GB on first run)...", args.model)
    embed_model = load_model(args.model, prefix_mode=PrefixMode(args.prefix_mode))
    logger.info("model loaded: dim=%d, prefix_mode=%s", embed_model.dimension(), args.prefix_mode)

    app = create_app(embed_model)

    import uvicorn

    uvicorn.run(app, host=args.host, port=args.port, log_level="info")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
