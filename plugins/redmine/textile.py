"""Markdown → Textile, for a Redmine that formats text with Textile (cloud-front#478).

The model answers in Markdown. A Redmine set to Textile shows that as raw stars and
brackets, so the answer is turned into Textile before it is posted. Only what the
answers use is converted — emphasis, inline code, links, headings, lists, tables and
code blocks; anything else passes through as text, which Textile shows as written.
"""

from __future__ import annotations

import re

_FENCE = re.compile(r"^```\s*([\w+-]*)\s*$")
_HEADING = re.compile(r"^(#{1,6})\s+(.*)$")
_BULLET = re.compile(r"^(\s*)[-*+]\s+(.*)$")
_NUMBERED = re.compile(r"^(\s*)\d+[.)]\s+(.*)$")
_TABLE_RULE = re.compile(r"^\s*\|?\s*:?-{3,}:?\s*(\|\s*:?-{3,}:?\s*)*\|?\s*$")
_CODE = re.compile(r"`([^`]+)`")
_LINK = re.compile(r"\[([^\]]+)\]\(([^)\s]+)\)")
_BOLD = re.compile(r"\*\*(.+?)\*\*|__(.+?)__")
_ITALIC = re.compile(r"(?<![\w*])\*(?!\s)(.+?)(?<!\s)\*(?![\w*])")


def _inline(text: str) -> str:
    """Emphasis, links and code in one line; nothing inside inline code is touched."""
    codes: list[str] = []

    def keep(m: re.Match) -> str:
        codes.append(m.group(1))
        return f"\0{len(codes) - 1}\0"

    text = _CODE.sub(keep, text)
    text = _LINK.sub(lambda m: f'"{m.group(1)}":{m.group(2)}', text)
    text = _BOLD.sub(lambda m: f"\1{m.group(1) or m.group(2)}\1", text)
    text = _ITALIC.sub(lambda m: f"_{m.group(1)}_", text)
    text = text.replace("\1", "*")
    return re.sub(r"\0(\d+)\0", lambda m: f"@{codes[int(m.group(1))]}@", text)


def _depth(indent: str) -> int:
    return len(indent.replace("\t", "  ")) // 2 + 1


def _cells(line: str) -> list[str]:
    return [c.strip() for c in line.strip().strip("|").split("|")]


def to_textile(md: str) -> str:
    out: list[str] = []
    lines = (md or "").split("\n")
    i = 0
    while i < len(lines):
        line = lines[i]
        fence = _FENCE.match(line)
        if fence:
            lang = fence.group(1)
            body: list[str] = []
            i += 1
            while i < len(lines) and not _FENCE.match(lines[i]):
                body.append(lines[i])
                i += 1
            attr = f' class="{lang}"' if lang else ""
            out.append(f"<pre><code{attr}>\n" + "\n".join(body) + "\n</code></pre>")
            i += 1
            continue
        if "|" in line and i + 1 < len(lines) and _TABLE_RULE.match(lines[i + 1]):
            out.append("|" + "|".join(f"_. {_inline(c)} " for c in _cells(line)) + "|")
            i += 2
            while i < len(lines) and lines[i].strip().startswith("|"):
                out.append("|" + "|".join(f" {_inline(c)} " for c in _cells(lines[i])) + "|")
                i += 1
            continue
        heading = _HEADING.match(line)
        bullet = _BULLET.match(line)
        numbered = _NUMBERED.match(line)
        if heading:
            out.append(f"h{len(heading.group(1))}. {_inline(heading.group(2))}")
        elif bullet:
            out.append("*" * _depth(bullet.group(1)) + " " + _inline(bullet.group(2)))
        elif numbered:
            out.append("#" * _depth(numbered.group(1)) + " " + _inline(numbered.group(2)))
        else:
            out.append(_inline(line))
        i += 1
    return "\n".join(out)
