"""Unit tests for bench.retrieval.base: estimate_tokens and pack_greedy."""

from __future__ import annotations

from bench.retrieval.base import estimate_tokens, pack_greedy


def test_estimate_tokens_matches_go_byte_length_over_3() -> None:
    # Mirrors internal/engine/graph/context.go's EstimateTokens: len(s) / 3
    # over UTF-8 bytes (integer division, floor).
    assert estimate_tokens("") == 0
    assert estimate_tokens("abc") == 1
    assert estimate_tokens("abcdef") == 2
    assert estimate_tokens("ab") == 0  # 2 // 3 == 0


def test_estimate_tokens_counts_utf8_bytes_not_characters() -> None:
    # A multi-byte UTF-8 character must count by its byte length, not by
    # Python's len() (character count) — same as Go's len(string) over its
    # UTF-8-encoded byte representation.
    single_multibyte_char = "あ"  # U+3042 HIRAGANA LETTER A, 3 UTF-8 bytes
    assert estimate_tokens(single_multibyte_char) == 3 // 3


def test_pack_greedy_fits_everything_under_generous_budget() -> None:
    ranked = [("a", "short text"), ("b", "another short text")]
    bead_ids, texts, used = pack_greedy(ranked, budget=10_000)
    assert bead_ids == ["a", "b"]
    assert texts == ["short text", "another short text"]
    assert used == estimate_tokens("short text") + estimate_tokens("another short text")


def test_pack_greedy_stops_at_first_item_that_does_not_fit() -> None:
    # "budget 到達で打ち切り" (stop at budget): a later, possibly smaller item
    # is NOT tried once one item does not fit — this is a hard stop, not a
    # best-effort bin-pack.
    a_text = "x" * 30  # estimate_tokens = 10
    b_text = "y" * 30  # would also cost 10, but budget only has 5 left
    c_text = "z"  # cheap (0 tokens) but never reached
    ranked = [("a", a_text), ("b", b_text), ("c", c_text)]
    bead_ids, texts, used = pack_greedy(ranked, budget=15)
    assert bead_ids == ["a"]
    assert texts == [a_text]
    assert used == 10


def test_pack_greedy_empty_input() -> None:
    bead_ids, texts, used = pack_greedy([], budget=1000)
    assert bead_ids == []
    assert texts == []
    assert used == 0


def test_pack_greedy_zero_budget_packs_nothing() -> None:
    ranked = [("a", "even one byte costs something once long enough")]
    bead_ids, texts, used = pack_greedy(ranked, budget=0)
    assert bead_ids == []
    assert used == 0
