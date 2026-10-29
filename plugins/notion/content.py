"""Notion's shapes, turned into what the model and the platform read.

Everything here is pure: ids out of links, rich text into text, a database row
into a flat record, a filter into Notion's own dialect. Kept apart from main.py
so it can be tested against the documented shapes without a plugin process.
"""

from __future__ import annotations

import json
import re

from buff_plugin import moment, say

# A Notion id is 32 hex digits, written with dashes as a UUID in the API and
# without them in a browser address — where it is glued to the page's title:
# https://www.notion.so/acme/Release-rules-2a3b4c5d6e7f8091a2b3c4d5e6f7a8b9?p=…
_HEX_RUN = re.compile(r"[0-9a-fA-F]{32,}")


def bare_id(value) -> str:
    """The 32-hex id of a page, database or data source — from an id with or
    without dashes, or from a Notion link.

    In a link the id is the last 32 hex digits of the last hex-looking run of
    the path: the title before it may itself look like hex («Cafe-Beef») and
    would otherwise be taken for part of the id. A `?p=` query names the page
    opened over a database view, and is the page a person is actually looking
    at, so it wins over the path.
    """
    text = str(value or "").strip()
    if not text:
        raise ValueError(say("пустая ссылка или идентификатор Notion", "an empty Notion link or id"))
    path, _, query = text.partition("?")
    for pair in query.split("&"):
        key, _, val = pair.partition("=")
        if key == "p" and val:
            return bare_id(val)
    runs = _HEX_RUN.findall(path.split("#")[0].replace("-", ""))
    if not runs:
        raise ValueError(say(f"{text} — не ссылка и не идентификатор Notion", f"{text} is neither a Notion link nor an id"))
    return runs[-1][-32:].lower()


def dashed(id32: str) -> str:
    """The same id the way the API writes it."""
    b = bare_id(id32)
    return f"{b[:8]}-{b[8:12]}-{b[12:16]}-{b[16:20]}-{b[20:]}"


def plain(rich: list | None) -> str:
    """Rich text as the text a person reads. Notion supplies `plain_text` on
    every item — a mention of a person arrives as «@Name» — so nothing is lost
    and nothing is invented."""
    out = []
    for item in rich or []:
        item = item or {}
        text = item.get("plain_text")
        if text is None:  # an item WE built for a request carries only text.content
            text = ((item.get("text") or {}).get("content")) or ""
        out.append(str(text))
    return "".join(out)


# A rich text item holds at most 2000 characters; a longer text is several
# items, never a shorter text.
TEXT_LIMIT = 2000


def text_chunks(text: str, limit: int = TEXT_LIMIT) -> list[dict]:
    text = str(text or "")
    if not text:
        return []
    return [{"type": "text", "text": {"content": text[i:i + limit]}}
            for i in range(0, len(text), limit)]


def link_item(title: str, url: str) -> dict:
    return {"type": "text", "text": {"content": title or url, "link": {"url": url}}}


def title_of(obj: dict | None) -> str:
    """A page's title lives in whichever property is of type `title`; a database
    or data source carries `title` at the top."""
    obj = obj or {}
    if obj.get("object") in ("database", "data_source") or "title" in obj and "properties" not in obj:
        return plain(obj.get("title") or [])
    for prop in (obj.get("properties") or {}).values():
        if (prop or {}).get("type") == "title":
            return plain(prop.get("title") or [])
    return ""


def _names(items) -> str:
    return ", ".join(str((i or {}).get("name") or "") for i in (items or []) if i)


def property_text(prop: dict | None) -> str:
    """One property value as text, by its type. Unknown kinds are shown as
    their JSON rather than dropped: a value the model cannot see is a fact the
    answer cannot rest on."""
    prop = prop or {}
    kind = prop.get("type", "")
    val = prop.get(kind)
    if kind in ("title", "rich_text"):
        return plain(val)
    if kind == "number":
        return "" if val is None else str(val)
    if kind in ("select", "status"):
        return str((val or {}).get("name") or "")
    if kind == "multi_select":
        return _names(val)
    if kind == "date":
        if not val:
            return ""
        start, end = str(val.get("start") or ""), val.get("end")
        return f"{start} → {end}" if end else start
    if kind == "checkbox":
        return say("да", "yes") if val else say("нет", "no")
    if kind in ("url", "email", "phone_number"):
        return str(val or "")
    if kind in ("people", "created_by", "last_edited_by"):
        people = val if isinstance(val, list) else [val]
        return ", ".join(str((p or {}).get("name") or (p or {}).get("id") or "") for p in people if p)
    if kind == "relation":
        return ", ".join(str((r or {}).get("id") or "") for r in (val or []))
    if kind in ("formula", "rollup"):
        # A computed value is a scalar of its own type, or an array of values.
        inner = val or {}
        itype = str(inner.get("type") or "")
        if itype == "string":
            return str(inner.get("string") or "")
        if itype == "boolean":
            return say("да", "yes") if inner.get("boolean") else say("нет", "no")
        if itype == "array":
            return "; ".join(property_text(v) for v in inner.get("array") or [])
        return property_text({"type": itype, itype: inner.get(itype)}) if itype else ""
    if kind in ("created_time", "last_edited_time"):
        return str(val or "")
    if kind == "files":
        return ", ".join(str((f or {}).get("name") or "") for f in (val or []))
    if kind == "unique_id":
        v = val or {}
        return f"{v.get('prefix')}-{v.get('number')}" if v.get("prefix") else str(v.get("number", ""))
    return json.dumps(val, ensure_ascii=False) if val is not None else ""


def row_of(page: dict) -> dict:
    """A database row as a flat record: its key, title, address and every
    property as text."""
    props = page.get("properties") or {}
    return {
        "key": bare_id(page.get("id", "")),
        "title": title_of(page),
        "url": page.get("url", ""),
        "updated": page.get("last_edited_time", ""),
        "properties": {name: property_text(p) for name, p in props.items()},
    }


# Which filter Notion accepts for a property depends on the property's type.
_CONTAINS = {"title", "rich_text", "url", "email", "phone_number", "multi_select"}
_EQUALS = {"select", "status"}


def query_filter(name: str, kind: str, contains: str) -> dict:
    """A `contains`-style filter on one property, in Notion's dialect. Types
    that have no such reading are refused with the type named, so the model
    reads without a filter instead of getting an empty answer."""
    if kind in _CONTAINS:
        return {"property": name, kind: {"contains": contains}}
    if kind in _EQUALS:
        return {"property": name, kind: {"equals": contains}}
    if kind == "number":
        try:
            return {"property": name, "number": {"equals": float(contains)}}
        except ValueError as exc:
            raise ValueError(say(f"свойство «{name}» числовое, а «{contains}» — не число",
                                 f"property “{name}” is numeric, and “{contains}” is not a number")) from exc
    if kind == "checkbox":
        return {"property": name, "checkbox": {"equals": contains.strip().lower() in ("да", "yes", "true", "1")}}
    raise ValueError(say(f"по свойству «{name}» типа {kind} фильтр не поддерживается — читайте без фильтра",
                         f"filtering by property “{name}” of type {kind} is not supported — read without a filter"))


def webhook_event(body: str) -> dict | None:
    """What a Notion callback says: the event type and the entity it is about.
    Anything that is not an event — the verification handshake, an empty body —
    is None."""
    try:
        doc = json.loads(body or "")
    except ValueError:
        return None
    if not isinstance(doc, dict) or not doc.get("type"):
        return None
    entity = doc.get("entity") or {}
    data = doc.get("data") or {}
    return {
        "type": str(doc.get("type")),
        "entity_id": str(entity.get("id") or ""),
        "entity_type": str(entity.get("type") or ""),
        "page_id": str(data.get("page_id") or ""),
    }


# ---- searching page text ourselves -----------------------------------------
#
# Notion's search matches TITLES only. A team's question is about what a page
# SAYS — «как у нас называется схема миграций» — and the page that says it is
# called «Регламент выпуска релизов». So the plugin reads the area's pages and
# looks for the words itself: crudely stemmed, because Russian inflects
# («миграций» / «миграции» / «миграция» are one word to a reader).

def words_of(query: str, min_len: int = 4) -> list[str]:
    """The words worth looking for: long enough to mean something on their own."""
    out = []
    for w in re.split(r"[^\w]+", str(query or "").lower()):
        if len(w) >= min_len and w not in out:
            out.append(w)
    return out


def stem(word: str) -> str:
    """A word without its last letters — enough to survive an inflection, not so
    much that unrelated words collide."""
    return word[: max(4, len(word) - 2)]


def match_score(text: str, words: list[str]) -> int:
    """How many of the words (stemmed) the text carries."""
    hay = str(text or "").lower()
    return sum(1 for w in words if stem(w) in hay)


def excerpt(text: str, words: list[str], width: int = 240) -> str:
    """The passage around the first matching word, on one line."""
    flat = re.sub(r"\s+", " ", str(text or ""))
    low = flat.lower()
    at = -1
    for w in words:
        at = low.find(stem(w))
        if at >= 0:
            break
    if at < 0:
        return flat[:width]
    start = max(0, at - width // 3)
    piece = flat[start:start + width]
    return ("…" if start > 0 else "") + piece + ("…" if start + width < len(flat) else "")


def not_before_minute(at: str, floor: str | None) -> bool:
    """Whether `at` is in the floor's minute or later.

    A LIVE Notion writes a comment's `created_time` to the MINUTE
    (`2026-09-16T07:52:00.000Z` for a comment written at 07:52:41), so a strict
    "after the floor" drops every request written in the same minute the floor
    was set — the one somebody wrote to try the connection out. The floor is
    therefore widened down to its minute; what was already reported is kept
    apart by id, here and upstream.
    """
    if not floor:
        return True
    a, f = moment(at), moment(floor)
    if a is None or f is None:
        return True
    return a >= f.replace(second=0, microsecond=0)
