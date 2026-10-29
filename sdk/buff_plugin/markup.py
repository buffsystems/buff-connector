"""Wiki markup, as text a model can read.

A Confluence page is stored as XHTML with Confluence's own elements mixed in:
`ac:structured-macro` around a code block, `ac:layout` wrappers that carry no
meaning, `ri:page` links that name a page rather than address it. Handing that
to a model raw wastes context on plumbing and loses the structure the reader
relied on — a table becomes a run-on sentence, a heading disappears.

The conversion keeps the three things a reader needs and drops the rest: WHAT is
said, HOW it is organised (headings, lists, tables, quotes, code) and WHERE it
points (link addresses, page names, attachment names).

Nothing here truncates. A long page is long; deciding what to leave out is the
run's job, not this function's.
"""

from __future__ import annotations

import re
from html.parser import HTMLParser

from .words import say

_SPACE = re.compile(r"\s+")
# A bullet or a number with nothing after it: what a macro-generated list leaves
# behind, and never something a person wrote.
_MARKER = re.compile(r"(?:-|\d+\.)\s*")

# Content that is not text for a reader.
_SILENT = {"script", "style", "ac:parameter"}

_HEADINGS = {"h1", "h2", "h3", "h4", "h5", "h6"}

# Elements that start a new block (a blank line between them).
_BLOCKS = {
    "p", "div", "table", "ul", "ol", "blockquote", "pre",
    "ac:structured-macro", "ac:rich-text-body", "ac:layout",
    "ac:layout-section", "ac:layout-cell",
} | _HEADINGS

# Elements that start a new LINE inside a block.
_LINES = {"li", "tr", "br"}


class _Reader(HTMLParser):
    def __init__(self) -> None:
        super().__init__(convert_charrefs=True)
        self.lines: list[str] = []
        self.line: list[str] = []
        self.pending_blank = False
        self.silent = 0
        self.code = 0
        self.lists: list[dict] = []
        self.href: str | None = None
        self.link: list[str] = []
        self.cells = 0
        self.in_cell = 0
        self.quote = 0

    # ---- line building -------------------------------------------------

    def write(self, text: str) -> None:
        if self.href is not None:
            self.link.append(text)
            return
        if self.pending_blank and (self.line or self.lines):
            self.flush()
            if self.lines and self.lines[-1] != "":
                self.lines.append("")
        self.pending_blank = False
        self.line.append(text)

    def flush(self) -> None:
        text = "".join(self.line).strip()
        self.line = []
        if not text or _MARKER.fullmatch(text):
            return
        if self.quote and not text.startswith(">"):
            text = "> " + text
        self.lines.append(text)

    def newline(self) -> None:
        self.flush()
        self.pending_blank = False

    def newblock(self) -> None:
        self.flush()
        self.pending_blank = True

    # ---- parsing -------------------------------------------------------

    def handle_starttag(self, tag, attrs):
        attr = {k: (v or "") for k, v in attrs}
        if tag in _SILENT:
            self.silent += 1
            return
        if self.silent:
            return

        if tag in _HEADINGS:
            self.newblock()
            self.write("#" * int(tag[1]) + " ")
        elif tag in ("pre", "ac:plain-text-body"):
            self.newblock()
            self.write("```")
            self.newline()
            self.code += 1
        elif tag in ("ul", "ol"):
            self.newblock()
            self.lists.append({"ordered": tag == "ol", "n": 0})
        elif tag == "li":
            self.newline()
            item = self.lists[-1] if self.lists else {"ordered": False, "n": 0}
            item["n"] += 1
            self.write(f"{item['n']}. " if item["ordered"] else "- ")
        elif tag == "blockquote":
            self.newblock()
            self.quote += 1
        elif tag == "tr":
            self.newline()
            self.cells = 0
        elif tag in ("td", "th"):
            if self.cells:
                self.write(" | ")
            self.cells += 1
            self.in_cell += 1
        elif tag == "a":
            self.href, self.link = attr.get("href") or "", []
        elif tag == "ri:page":
            title = attr.get("ri:content-title") or attr.get("content-title") or ""
            if title:
                self.write(f"«{title}»")
        elif tag == "ri:attachment":
            name = attr.get("ri:filename") or attr.get("filename") or ""
            if name:
                self.write(say(f"[изображение: {name}]", f"[image: {name}]"))
        elif tag == "br":
            self.newline()
        elif tag in _BLOCKS:
            # Inside a table cell a paragraph is not a new block: a live page
            # wraps every cell in one, and honouring that turns a row into five
            # lines with stray separators between them.
            if not self.in_cell:
                self.newblock()

    def handle_startendtag(self, tag, attrs):
        self.handle_starttag(tag, attrs)

    def handle_endtag(self, tag):
        if tag in _SILENT:
            self.silent = max(0, self.silent - 1)
            return
        if self.silent:
            return
        if tag == "a" and self.href is not None:
            text = "".join(self.link).strip()
            href, self.href, self.link = self.href, None, []
            if text and href and href not in text:
                self.write(f"{text} ({href})")
            else:
                self.write(text or href)
        elif tag in ("pre", "ac:plain-text-body"):
            self.code = max(0, self.code - 1)
            self.newline()
            self.write("```")
            self.newblock()
        elif tag == "blockquote":
            self.newblock()
            self.quote = max(0, self.quote - 1)
        elif tag in ("ul", "ol"):
            if self.lists:
                self.lists.pop()
            self.newblock()
        elif tag in ("td", "th"):
            self.in_cell = max(0, self.in_cell - 1)
        elif tag in _LINES:
            self.newline()
        elif tag in _BLOCKS:
            if not self.in_cell:
                self.newblock()

    def handle_data(self, data):
        if self.silent:
            return
        if self.code:
            for i, part in enumerate(data.split("\n")):
                if i:
                    self.newline()
                if part:
                    self.write(part)
            return
        # Runs of whitespace collapse, but a space at the EDGE of the run stays:
        # it is what separates «см.» from the link after it, and its loss is what
        # glued a full stop onto «Confluence 11.0».
        text = _SPACE.sub(" ", data)
        if not text.strip() and not self.line:
            return
        self.write(text)

    def handle_comment(self, data):
        # Some parsers hand CDATA to the comment hook; inside a code macro it is
        # the code itself and must survive.
        if self.code and data.startswith("[CDATA["):
            body = data[len("[CDATA["):]
            self.handle_data(body[:-2] if body.endswith("]]") else body)

    def unknown_decl(self, data):
        # CDATA arrives here on Python's parser: `CDATA[…]`.
        if self.code and data.startswith("CDATA["):
            self.handle_data(data[len("CDATA["):])


def html_to_text(html: str | None) -> str:
    """Storage-format XHTML (or any HTML) as readable text."""
    if not html:
        return ""
    reader = _Reader()
    reader.feed(str(html))
    reader.close()
    reader.flush()
    lines = [ln.rstrip() for ln in reader.lines]
    while lines and not lines[0]:
        lines.pop(0)
    while lines and not lines[-1]:
        lines.pop()
    return "\n".join(lines)
