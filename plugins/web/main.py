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

from buff_plugin import HostError, Plugin, plural

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
        raise RuntimeError(f"{what} содержит стоп-слово «{word}» — администратор коннектора запретил его")


# ---- search ---------------------------------------------------------------

def search_url(engine: str, query: str) -> str:
    base = BASES.get(engine, "")
    if not base:
        raise RuntimeError(f"для движка {engine} не задан адрес в настройках подключения")
    q = quote(query, safe="")
    if engine == "duckduckgo":
        return f"{base}/html/?q={q}"
    if engine == "searxng":
        return f"{base}/search?q={q}&format=json"
    if engine == "brave":
        return f"{base}/res/v1/web/search?q={q}&count=20"
    raise RuntimeError(f"неизвестный движок {engine}")


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
    description=(
        "Search the public internet. Reach for it when the answer depends on information that is "
        "current, external or absent from the project and the knowledge base: a library's release "
        "notes, an error message, a standard, a vendor's documentation, a price, a date. Returns "
        "titles, addresses and snippets; read a promising one with web_fetch. Results the "
        "administrator's lists exclude are not shown."),
    read_only=True,
    schema={"type": "object", "properties": {
        "query": {"type": "string", "description": "What to search for, as you would type it"},
    }, "required": ["query"], "additionalProperties": False},
)
def web_search(query: str = ""):
    if not query.strip():
        raise RuntimeError("пустой запрос")
    refuse_if_stopped(query, "запрос")
    try:
        results = run_search(query)
    except HostError as exc:
        raise RuntimeError(f"коннектор отказал: {exc}") from exc
    kept, hidden = filter_results(results)
    out = {"engine": ENGINE, "query": query, "results": kept}
    if hidden:
        out["hidden"] = hidden
    return out


# ---- fetch ----------------------------------------------------------------

def fetch(url: str) -> dict:
    """One GET with the host's checks — and each redirect hop is another."""
    refuse_if_stopped(url, "адрес")
    current = url
    for _ in range(REDIRECT_HOPS):
        res = plugin.http("GET", current, headers=ACCEPT_HTML)
        status = res["status"]
        location = _header(res, "Location")
        if status in (301, 302, 303, 307, 308) and location:
            current = urljoin(current, location)
            refuse_if_stopped(current, "адрес")
            continue
        content_type = _header(res, "Content-Type")
        title, text = page_text(content_type, res.get("body", ""))
        out = {"url": url, "status": status, "content_type": media_type(content_type), "text": text}
        if current != url:
            out["final_url"] = current
        if title:
            out["title"] = title
        return out
    raise RuntimeError(f"{url}: цепочка перенаправлений длиннее {REDIRECT_HOPS} — похоже на петлю")


def _header(res: dict, name: str) -> str:
    for k, v in (res.get("headers") or {}).items():
        if k.lower() == name.lower():
            return v
    return ""


@plugin.tool(
    description=(
        "Read a public web page by address — documentation, an article, a README, a JSON answer. "
        "Use it the moment you have an address: from the task, from the conversation or from "
        "web_search. HTML comes back as text with headings, lists, tables and links kept; the whole "
        "page, never trimmed. An address the administrator's lists exclude, an internal address, or "
        "a content type outside the allowed ones is refused with the reason."),
    read_only=True,
    schema={"type": "object", "properties": {
        "url": {"type": "string", "description": "Full address, with http:// or https://"},
    }, "required": ["url"], "additionalProperties": False},
)
def web_fetch(url: str = ""):
    if not url.strip():
        raise RuntimeError("пустой адрес")
    try:
        return fetch(url.strip())
    except HostError as exc:
        raise RuntimeError(f"коннектор отказал: {exc}") from exc


# ---- check ----------------------------------------------------------------

@plugin.check
def check():
    try:
        results = run_search("buff.systems")
    except engines.Challenge as exc:
        return {"ok": False, "detail": str(exc)}
    except (HostError, RuntimeError) as exc:
        return {"ok": False, "detail": f"Поиск {ENGINE_NAMES.get(ENGINE, ENGINE)} не ответил: {exc}"}
    n = len(results)
    return {"ok": True, "detail": f"Поиск {ENGINE_NAMES.get(ENGINE, ENGINE)} работает: на пробный запрос — "
                                  f"{n} {plural(n, 'результат', 'результата', 'результатов')}."}


if __name__ == "__main__":
    plugin.run()
