"""Unit tests for bench.perf.stats — pure distribution math, no I/O."""

from __future__ import annotations

import pytest

from bench.perf.stats import compute_latency_stats


def test_compute_latency_stats_basic_distribution():
    # 1..100 ms as seconds; nearest-rank p95 of a 100-point 1..100 sample is
    # element index ceil(0.95*100)=95 (1-indexed) => value 95.
    samples = [i / 1000 for i in range(1, 101)]
    stats = compute_latency_stats(samples)

    assert stats.n == 100
    assert stats.median_s == pytest.approx(0.050)
    assert stats.p95_s == pytest.approx(0.095)
    assert stats.p99_s == pytest.approx(0.099)
    assert stats.max_s == pytest.approx(0.100)


def test_compute_latency_stats_single_sample():
    stats = compute_latency_stats([0.123])
    assert stats.n == 1
    assert stats.median_s == pytest.approx(0.123)
    assert stats.p95_s == pytest.approx(0.123)
    assert stats.p99_s == pytest.approx(0.123)
    assert stats.max_s == pytest.approx(0.123)


def test_compute_latency_stats_unsorted_input_is_sorted_internally():
    # Same multiset as the basic test, but shuffled input order — the
    # result must be identical regardless of input order.
    samples = [i / 1000 for i in range(1, 101)]
    shuffled = samples[::-1]
    stats_a = compute_latency_stats(samples)
    stats_b = compute_latency_stats(shuffled)
    assert stats_a == stats_b


def test_compute_latency_stats_empty_raises():
    with pytest.raises(ValueError):
        compute_latency_stats([])


def test_latency_stats_to_json_dict_roundtrip_shape():
    stats = compute_latency_stats([0.01, 0.02, 0.03])
    d = stats.to_json_dict()
    assert set(d.keys()) == {"n", "median_s", "p95_s", "p99_s", "max_s"}
    assert d["n"] == 3
