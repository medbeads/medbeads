"""render_l0: Python port of internal/engine/graph/context.go's renderL0, so
rag/fts's own greedily-packed L0 text is byte-for-byte comparable (same
rendering rule, hence the same estimate_tokens cost) to what dag's L0
anchor tier actually sends an agent — R8.2's "チャンク = Bead に統一" is only
a fair comparison if every arm renders the same Bead content into text the
same way.

Mirrors renderL0/collectContentStrings exactly: walk every string value
reachable from Content (recursively through dicts/lists), sort them
(dict key iteration order is not stable in Go either — renderL0's own doc
comment explains why it sorts), then "{type}: {joined by space}", or just
{type} if Content has no string values at all.
"""

from __future__ import annotations

from typing import Any


def _collect_content_strings(value: Any, out: list[str]) -> None:
    if isinstance(value, str):
        if value:
            out.append(value)
    elif isinstance(value, dict):
        for elem in value.values():
            _collect_content_strings(elem, out)
    elif isinstance(value, list):
        for elem in value:
            _collect_content_strings(elem, out)
    # everything else (int/float/bool/None): ignored, matching renderL0's
    # Go switch's default no-op branch.


def render_l0(bead_type: str, content: dict[str, Any]) -> str:
    parts: list[str] = []
    _collect_content_strings(content, parts)
    parts.sort()
    if not parts:
        return bead_type
    return f"{bead_type}: " + " ".join(parts)
