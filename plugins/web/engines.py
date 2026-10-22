"""Search engines, each reduced to the one thing the model needs: a list of
{title, url, snippet}.

Three engines, three shapes:

  * DuckDuckGo has no API. Its no-JavaScript endpoint (`/html/`) answers a
    plain page, and the result links are wrapped in a redirect
    (`//duckduckgo.com/l/?uddg=<the real address>`). The endpoint is unofficial:
    under load it answers with a "select all squares containing a duck" page —
    status 202, no results — which must be recognised as what it is, not
    reported as "nothing found".
  * Brave Search API is JSON: `web.results[]` with `title`, `url`,
    `description`, `age`.
  * SearXNG is JSON when its operator enabled the format: `results[]` with
    `title`, `url`, `content`, `engine`, `publishedDate`.

Everything here is a pure function of the response: no network, so every shape
is a fixture and every fixture is a test.
"""

from __future__ import annotations

import json
from html.parser import HTMLParser
from urllib.parse import parse_qs, urlsplit


class Challenge(RuntimeError):
    """The engine asked a human to prove they are one."""


def unwrap_duckduckgo(href: str) -> str:
    """`//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com&rut=…` → the address."""
    if not href:
        return ""
    if href.startswith("//"):
        href = "https:" + href
    parts = urlsplit(href)
    if parts.netloc.endswith("duckduckgo.com") and parts.path.startswith("/l/"):
        return parse_qs(parts.query).get("uddg", [""])[0]
    return href


class _DuckReader(HTMLParser):
    """Walks the result page: a title link, a snippet link, per result."""

    def __init__(self) -> None:
        super().__init__(convert_charrefs=True)
        self.results: list[dict] = []
        self.current: dict | None = None
        self.field: str | None = None
        self.text: list[str] = []

    def handle_starttag(self, tag, attrs):
        if tag != "a":
            return
        a = dict(attrs)
        cls = a.get("class", "").split()
        if "result__a" in cls:
            self.current = {"title": "", "url": unwrap_duckduckgo(a.get("href", "")), "snippet": ""}
            self.field, self.text = "title", []
        elif "result__snippet" in cls and self.current is not None:
            self.field, self.text = "snippet", []

    def handle_data(self, data):
        if self.field:
            self.text.append(data)

    def handle_endtag(self, tag):
        if tag != "a" or not self.field or self.current is None:
            return
        self.current[self.field] = " ".join("".join(self.text).split())
        if self.field == "snippet":
            self.results.append(self.current)
            self.current = None
        self.field = None


def parse_duckduckgo(status: int, html: str) -> list[dict]:
    if "anomaly" in html and "challenge" in html and "result__a" not in html:
        raise Challenge(
            "DuckDuckGo попросил подтвердить, что запрос сделан человеком — так бывает с его "
            "неофициальным эндпоинтом под нагрузкой. Переключите движок на SearXNG или Brave "
            "в настройках подключения.")
    if status >= 400:
        raise RuntimeError(f"DuckDuckGo ответил {status}")
    reader = _DuckReader()
    reader.feed(html)
    reader.close()
    # A title without a snippet is still a result.
    if reader.current is not None and reader.current["title"]:
        reader.results.append(reader.current)
    return [r for r in reader.results if r["url"]]


def parse_brave(status: int, body: str) -> list[dict]:
    if status >= 400:
        raise RuntimeError(f"Brave Search ответил {status}: {_reason(body)}")
    data = json.loads(body) if body.strip() else {}
    out = []
    for r in (data.get("web") or {}).get("results") or []:
        item = {"title": r.get("title", ""), "url": r.get("url", ""),
                "snippet": r.get("description", "")}
        if r.get("age"):
            item["published"] = r["age"]
        out.append(item)
    return out


def parse_searxng(status: int, body: str) -> list[dict]:
    if status >= 400:
        hint = ""
        if status == 403:
            hint = " — скорее всего, в settings.yml экземпляра не включён формат json (search.formats)"
        raise RuntimeError(f"SearXNG ответил {status}{hint}")
    data = json.loads(body) if body.strip() else {}
    out = []
    for r in data.get("results") or []:
        item = {"title": r.get("title", ""), "url": r.get("url", ""),
                "snippet": r.get("content", "")}
        if r.get("publishedDate"):
            item["published"] = r["publishedDate"]
        if r.get("engine"):
            item["engine"] = r["engine"]
        out.append(item)
    return out


def _reason(body: str) -> str:
    try:
        data = json.loads(body)
    except ValueError:
        return body.strip()
    err = data.get("error") if isinstance(data, dict) else None
    if isinstance(err, dict):
        return err.get("detail") or err.get("message") or json.dumps(err, ensure_ascii=False)
    return json.dumps(data, ensure_ascii=False)
