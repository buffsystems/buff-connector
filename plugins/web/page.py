"""A fetched page as the model reads it.

HTML becomes text with its structure kept (headings, lists, tables, links —
the SDK's `html_to_text`); JSON is pretty-printed so a long API answer reads
as a document; anything else is handed over as it came. The title is read off
the page for the citation the model will write. Nothing here truncates: a
long page is long, and deciding what to leave out is the model's job.
"""

from __future__ import annotations

import json
from html.parser import HTMLParser

from buff_plugin import html_to_text


class _Title(HTMLParser):
    def __init__(self) -> None:
        super().__init__(convert_charrefs=True)
        self.inside = False
        self.parts: list[str] = []
        self.done = False

    def handle_starttag(self, tag, attrs):
        if tag == "title" and not self.done:
            self.inside = True

    def handle_data(self, data):
        if self.inside:
            self.parts.append(data)

    def handle_endtag(self, tag):
        if tag == "title" and self.inside:
            self.inside, self.done = False, True


def page_title(html: str) -> str:
    reader = _Title()
    reader.feed(html)
    reader.close()
    return " ".join("".join(reader.parts).split())


def media_type(content_type: str) -> str:
    return (content_type or "").split(";", 1)[0].strip().lower()


def page_text(content_type: str, body: str) -> tuple[str, str]:
    """(title, text) for a response body of the given Content-Type."""
    kind = media_type(content_type)
    if kind in ("text/html", "application/xhtml+xml"):
        return page_title(body), html_to_text(body)
    if kind.endswith("/json") or kind.endswith("+json"):
        try:
            return "", json.dumps(json.loads(body), ensure_ascii=False, indent=2)
        except ValueError:
            return "", body
    return "", body
