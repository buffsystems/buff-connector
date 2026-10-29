"""Web — the internet for the model, from inside the customer's perimeter.

Two tools, both read-only: `web_search` finds, `web_fetch` reads. What makes
this different from a fetch tool on a cloud backend is WHERE the boundary sits:
in the connector, under the customer's administrator, who gets the same knobs
every product with this feature offers — an allow list OR a deny list of sites
(bare domain, subdomains included, an optional path with `*` for a segment),
the content types the model may be handed, and stop words that refuse a query
or an address on the spot. One rule is not a knob: the internal network is
closed always — the connector refuses any address that resolves inside and
pins the connection to the public address it checked.

The lists live in the CONNECTOR, not here. A search result is dropped by
asking the host (`url_allowed`), and a fetch is refused by the host, so the
plugin cannot be talked out of the operator's policy — it never held it.

Redirects are not followed by the host, on purpose: a 302 is the classic way
past an allowlist. This plugin walks them one hop at a time, and every hop is
a fresh request the host checks like the first.
"""

from __future__ import annotations

from urllib.parse import quote, urljoin

from buff_plugin import HostError, Plugin, plural, plural_en, say

import engines
from page import media_type, page_text

plugin = Plugin("web", host_api=2)

ENGINE = plugin.config.get("engine") or "duckduckgo"
BASES = {
    "duckduckgo": (plugin.config.get("duckduckgo_base") or "").rstrip("/"),
    "searxng": (plugin.config.get("searxng_base") or "").rstrip("/"),
    "brave": (plugin.config.get("brave_base") or "").rstrip("/"),
}
# The engine by its own name, for what the check says to the operator.
ENGINE_NAMES = {"duckduckgo": "DuckDuckGo", "searxng": "SearXNG", "brave": "Brave Search"}
STOP_WORDS = [w for w in (plugin.config.get("stop_words") or []) if str(w).strip()]

USER_AGENT = "buff-connector web/1.0.0 (+https://buff.systems)"
ACCEPT_HTML = {"User-Agent": USER_AGENT, "Accept": "text/html,application/xhtml+xml,*/*;q=0.8",
               "Accept-Language": "ru,en;q=0.8"}
ACCEPT_JSON = {"User-Agent": USER_AGENT, "Accept": "application/json"}

# A redirect chain longer than this is a loop, not a destination.
REDIRECT_HOPS = 10


def stop_word_in(text: str) -> str:
    low = (text or "").lower()
    for word in STOP_WORDS:
        if str(word).lower() in low:
            return str(word)
    return ""


def refuse_if_stopped(text: str, what: str) -> None:
    word = stop_word_in(text)
    if word:
        raise RuntimeError(say(f"{what} содержит стоп-слово «{word}» — администратор коннектора запретил его",
                               f"{what} contains the stop word “{word}” — the connector's administrator forbade it"))


# ---- search ---------------------------------------------------------------

def search_url(engine: str, query: str) -> str:
    base = BASES.get(engine, "")
    if not base:
        raise RuntimeError(say(f"для движка {engine} не задан адрес в настройках подключения",
                               f"no address is set for the {engine} engine in the connection's settings"))
    q = quote(query, safe="")
    if engine == "duckduckgo":
        return f"{base}/html/?q={q}"
    if engine == "searxng":
        return f"{base}/search?q={q}&format=json"
    if engine == "brave":
        return f"{base}/res/v1/web/search?q={q}&count=20"
    raise RuntimeError(say(f"неизвестный движок {engine}", f"unknown engine {engine}"))


def run_search(query: str) -> list[dict]:
    url = search_url(ENGINE, query)
    if ENGINE == "brave":
        res = plugin.http("GET", url, credential="brave", headers=ACCEPT_JSON)
        return engines.parse_brave(res["status"], res["body"])
    if ENGINE == "searxng":
        res = plugin.http("GET", url, headers=ACCEPT_JSON)
        return engines.parse_searxng(res["status"], res["body"])
    res = plugin.http("GET", url, headers=ACCEPT_HTML)
    return engines.parse_duckduckgo(res["status"], res["body"])


def filter_results(results: list[dict]) -> tuple[list[dict], int]:
    """Drops what the operator's lists refuse, and says how many."""
    kept, hidden = [], 0
    for r in results:
        allowed, _ = plugin.url_allowed(r["url"])
        if allowed and not stop_word_in(r["url"]):
            kept.append(r)
        else:
            hidden += 1
    return kept, hidden


@plugin.tool(
    read_only=True,
    schema={"type": "object", "properties": {
        "query": {"type": "string"},
    }, "required": ["query"], "additionalProperties": False},
)
def web_search(query: str = ""):
    if not query.strip():
        raise RuntimeError(say("пустой запрос", "an empty query"))
    refuse_if_stopped(query, say("запрос", "the query"))
    try:
        results = run_search(query)
    except HostError as exc:
        raise RuntimeError(say(f"коннектор отказал: {exc}", f"the connector refused: {exc}")) from exc
    kept, hidden = filter_results(results)
    out = {"engine": ENGINE, "query": query, "results": kept}
    if hidden:
        out["hidden"] = hidden
    return out


# ---- fetch ----------------------------------------------------------------

def fetch(url: str) -> dict:
    """One GET with the host's checks — and each redirect hop is another."""
    refuse_if_stopped(url, say("адрес", "the address"))
    current = url
    for _ in range(REDIRECT_HOPS):
        res = plugin.http("GET", current, headers=ACCEPT_HTML)
        status = res["status"]
        location = _header(res, "Location")
        if status in (301, 302, 303, 307, 308) and location:
            current = urljoin(current, location)
            refuse_if_stopped(current, say("адрес", "the address"))
            continue
        content_type = _header(res, "Content-Type")
        title, text = page_text(content_type, res.get("body", ""))
        out = {"url": url, "status": status, "content_type": media_type(content_type), "text": text}
        if current != url:
            out["final_url"] = current
        if title:
            out["title"] = title
        return out
    raise RuntimeError(say(f"{url}: цепочка перенаправлений длиннее {REDIRECT_HOPS} — похоже на петлю",
                           f"{url}: a redirect chain longer than {REDIRECT_HOPS} — looks like a loop"))


def _header(res: dict, name: str) -> str:
    for k, v in (res.get("headers") or {}).items():
        if k.lower() == name.lower():
            return v
    return ""


@plugin.tool(
    read_only=True,
    schema={"type": "object", "properties": {
        "url": {"type": "string"},
    }, "required": ["url"], "additionalProperties": False},
)
def web_fetch(url: str = ""):
    if not url.strip():
        raise RuntimeError(say("пустой адрес", "an empty address"))
    try:
        return fetch(url.strip())
    except HostError as exc:
        raise RuntimeError(say(f"коннектор отказал: {exc}", f"the connector refused: {exc}")) from exc


# ---- check ----------------------------------------------------------------

@plugin.check
def check():
    try:
        results = run_search("buff.systems")
    except engines.Challenge as exc:
        return {"ok": False, "detail": str(exc)}
    except (HostError, RuntimeError) as exc:
        name = ENGINE_NAMES.get(ENGINE, ENGINE)
        return {"ok": False, "detail": say(f"Поиск {name} не ответил: {exc}", f"{name} search did not answer: {exc}")}
    n = len(results)
    name = ENGINE_NAMES.get(ENGINE, ENGINE)
    return {"ok": True, "detail": say(
        f"Поиск {name} работает: на пробный запрос — {n} {plural(n, 'результат', 'результата', 'результатов')}.",
        f"{name} search works: {n} {plural_en(n, 'result', 'results')} for a test query.")}


if __name__ == "__main__":
    plugin.run()
