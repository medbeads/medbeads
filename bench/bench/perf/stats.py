"""Latency distribution math: pure, I/O-free, unit-testable in isolation.

Mirrors internal/engine/perf_bench_test.go's computeDurationStats (same
nearest-rank percentile method) so the Go and Python halves of the M1 perf
harness report figures using the same definition of "p95" — a reviewer
comparing the two shouldn't have to reconcile two different percentile
conventions on top of two different languages.
"""

from __future__ import annotations

import math
from dataclasses import dataclass


@dataclass(frozen=True)
class LatencyStats:
    """median/p95/p99 (seconds) plus sample size, over one latency sample."""

    n: int
    median_s: float
    p95_s: float
    p99_s: float
    max_s: float

    def to_json_dict(self) -> dict[str, float | int]:
        return {
            "n": self.n,
            "median_s": self.median_s,
            "p95_s": self.p95_s,
            "p99_s": self.p99_s,
            "max_s": self.max_s,
        }


def _percentile(sorted_samples: list[float], p: float) -> float:
    """Nearest-rank percentile: ceil(p * n), 1-indexed, clamped to [1, n].

    Same method as internal/engine/perf_bench_test.go's computeDurationStats
    (deliberately not linear interpolation — nearest-rank is simpler, and at
    this sample size (~100s of points) the two methods differ by at most one
    sample's worth of latency, which does not matter for a go/no-go
    threshold check).
    """
    n = len(sorted_samples)
    if n == 0:
        raise ValueError("_percentile: empty sample")
    idx = math.ceil(p * n)
    idx = max(1, min(idx, n))
    return sorted_samples[idx - 1]


def compute_latency_stats(samples_s: list[float]) -> LatencyStats:
    """LatencyStats over samples_s (each a latency in seconds).

    Raises ValueError on an empty sample — a perf run producing zero timed
    calls is a harness bug (e.g. no patients/queries sampled), not a "0ms"
    result worth reporting silently.
    """
    if not samples_s:
        raise ValueError("compute_latency_stats: samples_s must not be empty")
    ordered = sorted(samples_s)
    return LatencyStats(
        n=len(ordered),
        median_s=_percentile(ordered, 0.5),
        p95_s=_percentile(ordered, 0.95),
        p99_s=_percentile(ordered, 0.99),
        max_s=ordered[-1],
    )
