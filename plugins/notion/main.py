"""Notion integration — pages and databases as the team's written context.

Notion is a knowledge base, not a tracker: its value is what the team WROTE
DOWN — requirements, decisions, runbooks, the tables they keep. So this
integration does two jobs, and the first one matters more:

1. It gives the model reading tools over the connection's area, so an answer
   can rest on what the team already wrote instead of on a guess.
2. It takes requests written as comments on a page and answers in the same
   discussion; a research request is answered with a child PAGE holding the
   report, and a comment carrying the link.

Four facts about Notion's API shape the code:

1. There is NO feed of comments across a workspace, and a new comment does not
   touch the page's `last_edited_time`. Discovery is therefore the webhook —
   `comment.created` names the comment, which is then read by id — and the poll
   is a safety net that walks the area's pages round-robin, a few per pass.
2. An integration is not a person: nobody can @-mention it. People address the
   bot by WRITING its name — `@buff ask …` — so the alias in the settings is what
   they type, and the bot's own name from `/v1/users/me` is the second spelling.
3. Since API version 2026-03-11 a page is read and written as MARKDOWN in one
   call (`/v1/pages/{id}/markdown`, `markdown` on create). No block walking, no
   home-grown renderer: the model sees the page the way the API writes it.
4. Where a page is, is a chain of parents — page → page → data source → database
   → page … → workspace. The area is a list of roots, and a page belongs to it
   when a root is somewhere up that chain. Parents are looked up once and kept.
"""

from __future__ import annotations

import json
import random
import time
from urllib.parse import quote

from buff_plugin import Event, Identity, Plugin, event_id, parse_command, say, source, visible

from content import (
    bare_id,
    dashed,
    excerpt,
    link_item,
    match_score,
    not_before_minute,
    plain,
    property_text,
    query_filter,
    row_of,
    text_chunks,
    title_of,
    webhook_event,
    words_of,
)

plugin = Plugin("notion", host_api=3)

BASE = plugin.config.get("base_url", "https://api.notion.com").rstrip("/")
ROOTS = {bare_id(r) for r in plugin.config.get("roots", []) if str(r).strip()}
ALIAS = plugin.config.get("mention_alias", "")
API_VERSION = "2026-03-11"

# How many pages one poll pass reads comments for. Notion's ordinary plan
# allows three requests a second; a pass every five seconds with ten pages plus
# the search stays inside that, and a bigger area is covered over several
# passes rather than by exceeding it.
PAGES_PER_PASS = 10
# How long the list of the area's pages is trusted before being searched again.
AREA_TTL_SEC = 30

me = {"id": "", "name": "", "workspace": ""}

# Where each page or database sits: id → its parent {type, id}. Looked up once.
_parents: dict[str, dict] = {}
_page_meta: dict[str, dict] = {}
_area: dict = {"at": 0.0, "pages": []}
# Page text, by page: (last_edited_time it was read at, markdown). Notion's
# search matches titles only, so a search by words reads the area's pages and
# looks itself; a page is re-read only once it has been edited.
_bodies: dict[str, tuple[str, str]] = {}
_cursor = {"i": 0}
# Comments already reported. The sweep reads pages in turns, so the platform's
# watermark cannot be the filter — a comment on a page read later would be
# older than the mark and lost. The floor is where reading started; the set is
# what was already sent. The platform de-duplicates by id upstream as well.
_seen: set[str] = set()
_floor = {"at": ""}
# People who asked, by user id: read once, for the name and the e-mail (#476).
_users: dict[str, dict] = {}


# ---- transport ---------------------------------------------------------

class NotionError(RuntimeError):
    def __init__(self, status: int, path: str, body: str):
        super().__init__(say(f"Notion ответил {status} на {path}: {body[:300]}",
                             f"Notion answered {status} to {path}: {body[:300]}"))
        self.status = status


def api(method: str, path: str, body: dict | None = None, *, attempts: int = 6):
    """One call, with Notion's own rate limit respected: on 429 and 529 the
    answer names how long to wait, and the call is retried after that with a
    growing, jittered pause — never dropped, never hammered."""
    headers = {"Notion-Version": API_VERSION}
    if body is not None:
        headers["Content-Type"] = "application/json"
    delay = 1.0
    for attempt in range(attempts):
        res = plugin.http(method, BASE + path, credential="api", headers=headers,
                          body=json.dumps(body) if body is not None else "")
        status = res["status"]
        if status in (429, 529) and attempt < attempts - 1:
            wait = float((res.get("headers") or {}).get("Retry-After") or delay)
            time.sleep(wait + random.uniform(0, 0.3))
            delay *= 2
            continue
        if status >= 400:
            raise NotionError(status, path, res.get("body", ""))
        return json.loads(res["body"]) if res.get("body", "").strip() else {}
    raise NotionError(429, path, say("лимит запросов не отпустил", "the rate limit did not let go"))


def paged(method: str, path: str, body: dict | None = None, *, key: str = "results") -> list:
    """Every result of a paginated call. Nothing is cut: a list of a hundred
    pages is a hundred pages."""
    out: list = []
    cursor = None
    while True:
        if method == "GET":
            sep = "&" if "?" in path else "?"
            page = api("GET", path + (f"{sep}start_cursor={quote(cursor)}" if cursor else ""))
        else:
            page = api(method, path, {**(body or {}), **({"start_cursor": cursor} if cursor else {})})
        out.extend(page.get(key) or [])
        if not page.get("has_more") or not page.get("next_cursor"):
            return out
        cursor = page["next_cursor"]


def whoami() -> None:
    who = api("GET", "/v1/users/me")
    me["id"] = bare_id(who.get("id", "")) if who.get("id") else ""
    me["name"] = str(who.get("name") or "")
    me["workspace"] = str(((who.get("bot") or {}).get("workspace_name")) or "")


def identity() -> Identity:
    # People cannot @-mention an integration, so the alias is what they type;
    # the bot's own name is the second spelling it answers to.
    return Identity(name=me["name"], key=me["id"], display=me["name"], alias=ALIAS)


# ---- where things are ---------------------------------------------------

def parent_of(kind: str, id32: str) -> dict:
    """The parent of a page, block, data source or database: {type, id}. Read
    once and kept — a page does not move often, and the sweep asks every pass."""
    if id32 in _parents:
        return _parents[id32]
    path = {"page": "/v1/pages/", "block": "/v1/blocks/", "data_source": "/v1/data_sources/",
            "database": "/v1/databases/"}[kind]
    obj = api("GET", path + dashed(id32))
    if kind == "page":
        _page_meta[id32] = obj
    parent = obj.get("parent") or {}
    ptype = str(parent.get("type") or "workspace")
    pid = parent.get(ptype) if ptype != "workspace" else ""
    found = {"type": ptype, "id": bare_id(pid) if isinstance(pid, str) and pid else ""}
    _parents[id32] = found
    return found


def in_area(kind: str, id32: str) -> bool:
    """Whether a root of the connection is somewhere up the chain of parents.
    No roots means the area is everything the integration was given."""
    if not ROOTS:
        return True
    cur_kind, cur = kind, bare_id(id32)
    for _ in range(64):  # a chain deeper than this is not a document tree
        if cur in ROOTS:
            return True
        try:
            parent = parent_of(cur_kind, cur)
        except NotionError:
            return False  # unreadable = not shared with the bot = not ours
        if parent["type"] == "workspace" or not parent["id"]:
            return False
        cur_kind = {"page_id": "page", "block_id": "block", "data_source_id": "data_source",
                    "database_id": "database"}.get(parent["type"], "page")
        cur = parent["id"]
    return False


def _kind(page: bool) -> str:
    """What a search result or a child is, in the reader's words."""
    return say("страница", "page") if page else say("база данных", "database")


def require_area(kind: str, id32: str) -> None:
    if not in_area(kind, id32):
        raise RuntimeError(say(f"{id32} вне области, разрешённой этому подключению",
                               f"{id32} is outside the area this connection is allowed"))


def page_of_block(block_id: str) -> str:
    """The page a block sits on: blocks nest, so the chain is followed up to
    the first page."""
    cur_kind, cur = "block", bare_id(block_id)
    for _ in range(64):
        parent = parent_of(cur_kind, cur)
        if parent["type"] == "page_id":
            return parent["id"]
        if parent["type"] != "block_id" or not parent["id"]:
            return ""
        cur = parent["id"]
    return ""


def page_meta(id32: str) -> dict:
    if id32 not in _page_meta:
        _page_meta[id32] = api("GET", "/v1/pages/" + dashed(id32))
    return _page_meta[id32]


def area_pages() -> list[str]:
    """Every page of the area, newest edit first — what the sweep walks."""
    if _area["pages"] and time.time() - _area["at"] < AREA_TTL_SEC:
        return _area["pages"]
    found = paged("POST", "/v1/search", {
        "filter": {"property": "object", "value": "page"},
        "sort": {"direction": "descending", "timestamp": "last_edited_time"},
        "page_size": 100,
    })
    pages = []
    for p in found:
        pid = bare_id(p.get("id", ""))
        _page_meta[pid] = p
        if in_area("page", pid):
            pages.append(pid)
    _area["at"], _area["pages"] = time.time(), pages
    return pages


def page_markdown(page: str) -> str:
    """The page as markdown, read once per edit."""
    edited = str(page_meta(page).get("last_edited_time") or "")
    hit = _bodies.get(page)
    if hit and hit[0] == edited:
        return hit[1]
    md = str(api("GET", f"/v1/pages/{dashed(page)}/markdown").get("markdown") or "")
    _bodies[page] = (edited, md)
    return md


# ---- requests in comments ------------------------------------------------

def thread_key(page: str, discussion: str) -> str:
    """`PAGE#DISCUSSION` — the discussion IS the thread: the answer goes back
    into the same one, and a second question there continues the talk."""
    return f"{bare_id(page)}#{bare_id(discussion)}" if discussion else bare_id(page)


def split_key(key: str) -> tuple[str, str]:
    page, _, discussion = str(key).partition("#")
    try:
        return bare_id(page), (bare_id(discussion) if discussion else "")
    except ValueError as exc:
        raise RuntimeError(say(f"{key} — не адрес страницы Notion (нужно СТРАНИЦА или СТРАНИЦА#обсуждение)",
                               f"{key} is not a Notion page address (PAGE or PAGE#discussion is needed)")) from exc


def page_of_comment(c: dict) -> str:
    parent = c.get("parent") or {}
    if parent.get("type") == "page_id":
        return bare_id(parent["page_id"])
    if parent.get("type") == "block_id":
        return page_of_block(parent["block_id"])
    return ""


def user_of(uid: str) -> dict:
    """The user behind an id, read once. Reading users is a capability the
    operator may not have granted; then there is nothing, and the next request
    asks again rather than remembering the refusal."""
    if not uid:
        return {}
    if uid not in _users:
        try:
            _users[uid] = api("GET", "/v1/users/" + dashed(uid))
        except (NotionError, ValueError):
            return {}
    return _users[uid]


def author_of(c: dict) -> str:
    """Who wrote it, in words: Notion resolves the name beside the comment.
    Failing that, the user is read by id, and failing that the id is what
    there is."""
    display = (c.get("display_name") or {}).get("resolved_name")
    if display:
        return str(display)
    uid = ((c.get("created_by") or {}).get("id") or "")
    return str(user_of(uid).get("name") or uid)


def author_email_of(c: dict) -> str:
    """The asker's e-mail, for the connection's «who may ask» rules (#476).
    Notion names it only on the user, and only with the «user information
    including email addresses» capability; without it the e-mail is empty.
    Notion has no login, so the e-mail is all the rules can match."""
    uid = ((c.get("created_by") or {}).get("id") or "")
    return str((user_of(uid).get("person") or {}).get("email") or "")


def comment_event(c: dict, floor: str | None) -> Event | None:
    """One comment, turned into a request — or into nothing.

    Every path that produces an event goes through here: the sweep and the
    callback alike, so the area check, the bot's own voice and the address are
    decided in one place (#282).
    """
    cid = bare_id(c.get("id", "")) if c.get("id") else ""
    if not cid or cid in _seen:
        return None
    author_id = ((c.get("created_by") or {}).get("id") or "")
    if author_id and identity().is_me(bare_id(author_id), author_id):
        return None  # the bot's own replies are not requests
    at = str(c.get("created_time") or "")
    # Notion stamps a comment to the minute, so the floor is widened to its
    # minute; ids keep what was already reported apart.
    if not not_before_minute(at, floor):
        return None
    text = plain(c.get("rich_text"))
    cmd = parse_command(text, identity())
    if not cmd:
        return None
    page = page_of_comment(c)
    if not page or not in_area("page", page):
        return None
    meta = page_meta(page)
    discussion = str(c.get("discussion_id") or "")
    _seen.add(cid)
    return Event(
        id=event_id("comment", cid, at), at=at, kind="comment",
        title=title_of(meta),
        body=cmd.instruction,
        author=author_of(c),
        author_email=author_email_of(c),
        url=f"{meta.get('url', '')}?d={bare_id(discussion)}" if discussion else str(meta.get("url", "")),
        action=cmd.action,
        # Notion holds no code, so a request from it carries no repository and
        # no branch: the answer is computed on the project the connection
        # points at.
        extra=source(thread_key(page, discussion), trigger="mention", context="issue"),
    )


def comments_of(page: str) -> list[dict]:
    return paged("GET", f"/v1/comments?block_id={dashed(page)}&page_size=100")


# ---- writing -----------------------------------------------------------

def post_comment(page: str, discussion: str, rich: list[dict]) -> dict:
    body: dict = {"rich_text": rich}
    if discussion:
        body["discussion_id"] = dashed(discussion)
    else:
        body["parent"] = {"page_id": dashed(page)}
    return api("POST", "/v1/comments", body)


def comment_rich(text: str, link: dict | None, notes: list[str]) -> list[dict]:
    rich: list[dict] = []
    if text and text.strip():
        rich.extend(text_chunks(text.strip()))
    if link and link.get("url"):
        rich.extend(text_chunks("\n\n" if rich else ""))
        rich.append(link_item(str(link.get("title") or say("Открыть в Buff", "Open in Buff")), str(link["url"])))
    for note in notes:
        rich.extend(text_chunks(("\n\n" if rich else "") + note))
    return rich


def create_child_page(parent: str, title: str, markdown: str) -> dict:
    return api("POST", "/v1/pages", {
        "parent": {"page_id": dashed(parent)},
        "properties": {"title": {"title": text_chunks(title or say("Без названия", "Untitled"))}},
        "markdown": markdown or "",
    })


def attach_pdf(page: str, attachment: dict) -> None:
    """The document as a file on the page: uploaded byte for byte, then placed
    as a PDF block."""
    import base64

    name = str(attachment.get("filename") or "report.pdf")
    content = base64.b64decode(attachment.get("base64", ""))
    upload = api("POST", "/v1/file_uploads", {"filename": name, "content_type": "application/pdf"})
    boundary = "----buff" + base64.b16encode(random.randbytes(8)).decode()
    body = (
        f"--{boundary}\r\n"
        f'Content-Disposition: form-data; name="file"; filename="{name}"\r\n'
        "Content-Type: application/pdf\r\n\r\n"
    ).encode() + content + f"\r\n--{boundary}--\r\n".encode()
    res = plugin.http("POST", f"{BASE}/v1/file_uploads/{upload['id']}/send", credential="api",
                      headers={"Notion-Version": API_VERSION,
                               "Content-Type": f"multipart/form-data; boundary={boundary}"},
                      body_bytes=body)
    if res["status"] >= 400:
        raise NotionError(res["status"], "/v1/file_uploads/…/send", res.get("body", ""))
    api("PATCH", f"/v1/blocks/{dashed(page)}/children", {
        "children": [{"type": "pdf", "pdf": {"type": "file_upload", "file_upload": {"id": upload["id"]}}}],
    })


# ---- plugin surface ----------------------------------------------------

@plugin.check
def check():
    whoami()
    where = (say(f"Notion, рабочее пространство «{me['workspace']}»", f"Notion, workspace “{me['workspace']}”")
             if me["workspace"] else "Notion")
    integration = say(f"интеграция «{me['name']}»", f"integration “{me['name']}”")
    if not ROOTS:
        return {"ok": True, "detail": f"{where}, {integration}. " + say(
            "Бот видит всё, что открыто этой интеграции.", "The bot sees everything open to this integration.")}
    seen = 0
    for root in sorted(ROOTS):
        for kind in ("page", "database", "data_source"):
            try:
                parent_of(kind, root)
                seen += 1
                break
            except NotionError:
                continue
    sees = visible(seen, len(ROOTS), one="указанную страницу или базу", many="указанные страницы и базы",
                   many_of="указанных страниц и баз", fix="Подключите их к интеграции в самом Notion.",
                   en=dict(one="the listed page or database", many="listed pages and databases",
                           fix="Connect them to the integration in Notion itself."))
    return {"ok": True, "detail": f"{where}, {integration}. {sees}"}


@plugin.poll
def poll(since):
    """The safety net: a few pages of the area per pass, round-robin, every
    comment newer than where reading started and not yet reported."""
    if not me["id"]:
        whoami()
    if not _floor["at"]:
        _floor["at"] = since or ""
    pages = area_pages()
    if not pages:
        return []
    start = _cursor["i"] % len(pages)
    batch = [pages[(start + n) % len(pages)] for n in range(min(PAGES_PER_PASS, len(pages)))]
    _cursor["i"] = (start + len(batch)) % len(pages)
    events: list[Event] = []
    for page in batch:
        try:
            for c in comments_of(page):
                e = comment_event(c, _floor["at"])
                if e:
                    events.append(e)
        except NotionError as exc:
            plugin.log(say(f"страница {page} не прочитана: {exc}", f"page {page} could not be read: {exc}"))
    return events


@plugin.webhook
def webhook(headers: dict, body: str):
    """A callback names the comment; the comment is read by id. Anything else
    Notion sends about — a page edited, a schema changed — is not a request."""
    if not me["id"]:
        whoami()
    ev = webhook_event(body)
    if not ev or ev["type"] != "comment.created" or not ev["entity_id"]:
        return []
    c = api("GET", "/v1/comments/" + dashed(ev["entity_id"]))
    e = comment_event(c, None)
    return [e] if e else []


@plugin.reply
def reply(issue: str = "", text: str = "", link: dict | None = None,
          attachment: dict | None = None, document: dict | None = None, **_):
    page, discussion = split_key(issue)
    # The area bounds writes too, not only reads.
    require_area("page", page)
    notes: list[str] = []
    if document or attachment:
        # A finished report is a PAGE of its own, under the one where it was
        # asked for — the text itself, not a picture of it; the PDF goes on the
        # page as a file for those who want one.
        doc = document or {}
        title = str(doc.get("title") or (attachment or {}).get("filename") or say("Исследование", "Research"))
        made = create_child_page(page, title, str(doc.get("markdown") or ""))
        if attachment:
            attach_pdf(bare_id(made["id"]), attachment)
        notes.append(say(f"Отчёт — на странице «{title}»: {made.get('url', '')}",
                         f"The report is on the page “{title}”: {made.get('url', '')}"))
    rich = comment_rich(text, link, notes)
    if not rich:
        return {"ok": True}
    post_comment(page, discussion, rich)
    return {"ok": True}


@plugin.tool(read_only=True,
             schema={"type": "object",
                     "properties": {"query": {"type": "string"}, "limit": {"type": "integer"}},
                     "required": ["query"], "additionalProperties": False})
def search_pages(query: str = "", limit: int = 20):
    found = api("POST", "/v1/search", {"query": query, "page_size": max(1, min(int(limit or 20), 100)),
                                         "sort": {"direction": "descending", "timestamp": "last_edited_time"}})
    out = []
    seen: set[str] = set()
    for r in found.get("results") or []:
        kind = str(r.get("object") or "")
        rid = bare_id(r.get("id", ""))
        if kind == "page":
            _page_meta[rid] = r
        if not in_area("page" if kind == "page" else "data_source", rid):
            continue
        seen.add(rid)
        out.append({"key": rid, "kind": _kind(kind == "page"),
                    "title": title_of(r), "url": r.get("url", ""), "updated": r.get("last_edited_time", ""),
                    "matched": say("заголовок", "title")})
    # Then the text of every page of the area that the title search did not
    # already find — read once per edit, so the second question is cheap.
    words = words_of(query)
    scored = []
    for page in area_pages():
        if page in seen or not words:
            continue
        try:
            text = page_markdown(page)
        except NotionError as exc:
            plugin.log(say(f"страница {page} не прочитана для поиска: {exc}",
                           f"page {page} could not be read for search: {exc}"))
            continue
        score = match_score(text, words)
        if score:
            meta = page_meta(page)
            scored.append((score, {"key": page, "kind": _kind(True), "title": title_of(meta),
                                   "url": meta.get("url", ""), "updated": meta.get("last_edited_time", ""),
                                   "matched": say("текст", "text"), "excerpt": excerpt(text, words)}))
    scored.sort(key=lambda s: -s[0])
    out.extend(item for _, item in scored)
    return {"total": len(out), "results": out,
            "note": say("Совпадения по заголовку — от Notion, по тексту — из прочитанных страниц области; "
                        "читайте найденное get_page.",
                        "Title matches come from Notion, text matches from the area's pages read; "
                        "read what was found with get_page.")}


@plugin.tool(read_only=True,
             schema={"type": "object", "properties": {"key": {"type": "string"}},
                     "required": ["key"], "additionalProperties": False})
def get_page(key: str = ""):
    page, _ = split_key(key)
    require_area("page", page)
    meta = api("GET", "/v1/pages/" + dashed(page))
    _page_meta[page] = meta
    md = api("GET", f"/v1/pages/{dashed(page)}/markdown")
    parent = meta.get("parent") or {}
    return {
        "key": page,
        "title": title_of(meta),
        "url": meta.get("url", ""),
        "parent": {"type": parent.get("type", ""), "key": bare_id(parent.get(parent.get("type", ""), ""))
                   if parent.get("type") not in (None, "workspace") else ""},
        "updated": meta.get("last_edited_time", ""),
        "properties": {name: property_text(p) for name, p in (meta.get("properties") or {}).items()
                       if (p or {}).get("type") != "title"},
        "markdown": md.get("markdown", ""),
        "truncated": bool(md.get("truncated")),
        "comments": comments_view(page),
    }


def comments_view(page: str) -> list[dict]:
    out = []
    for c in comments_of(page):
        out.append({"discussion": bare_id(c.get("discussion_id", "")) if c.get("discussion_id") else "",
                    "author": author_of(c), "created": c.get("created_time", ""),
                    "text": plain(c.get("rich_text"))})
    return out


@plugin.tool(read_only=True,
             schema={"type": "object", "properties": {"key": {"type": "string"}},
                     "required": ["key"], "additionalProperties": False})
def get_page_children(key: str = ""):
    page, _ = split_key(key)
    require_area("page", page)
    kids = []
    for b in paged("GET", f"/v1/blocks/{dashed(page)}/children?page_size=100"):
        kind = b.get("type")
        if kind in ("child_page", "child_database"):
            kids.append({"key": bare_id(b.get("id", "")),
                         "kind": _kind(kind == "child_page"),
                         "title": str((b.get(kind) or {}).get("title") or "")})
    return {"total": len(kids), "children": kids}


@plugin.tool(read_only=True,
             schema={"type": "object", "properties": {"key": {"type": "string"}},
                     "required": ["key"], "additionalProperties": False})
def get_database(key: str = ""):
    db_id = bare_id(key)
    db = api("GET", "/v1/databases/" + dashed(db_id))
    require_area("database", db_id)
    sources = []
    for ds in db.get("data_sources") or []:
        full = api("GET", "/v1/data_sources/" + dashed(ds["id"]))
        sources.append({"key": bare_id(ds["id"]), "name": ds.get("name", ""),
                        "properties": [{"name": n, "type": (p or {}).get("type", "")}
                                       for n, p in (full.get("properties") or {}).items()]})
    return {"key": db_id, "title": title_of(db), "url": db.get("url", ""), "data_sources": sources}


@plugin.tool(read_only=True,
             schema={"type": "object",
                     "properties": {"key": {"type": "string"}, "property": {"type": "string"},
                                    "contains": {"type": "string"}, "limit": {"type": "integer"}},
                     "required": ["key"], "additionalProperties": False})
def query_database(key: str = "", property: str = "", contains: str = "", limit: int = 100):
    given = bare_id(key)
    # A database is a container of data sources; the rows live in a source.
    try:
        db = api("GET", "/v1/databases/" + dashed(given))
        require_area("database", given)
        sources = [bare_id(ds["id"]) for ds in db.get("data_sources") or []]
    except NotionError:
        require_area("data_source", given)
        sources = [given]
    rows: list[dict] = []
    for ds in sources:
        body: dict = {"page_size": max(1, min(int(limit or 100), 100))}
        if property and contains:
            schema = api("GET", "/v1/data_sources/" + dashed(ds)).get("properties") or {}
            kind = str((schema.get(property) or {}).get("type") or "")
            if not kind:
                raise RuntimeError(say(f"в базе нет свойства «{property}»; есть: {', '.join(schema)}",
                                       f"the database has no property “{property}”; it has: {', '.join(schema)}"))
            body["filter"] = query_filter(property, kind, contains)
        res = api("POST", f"/v1/data_sources/{dashed(ds)}/query", body)
        rows.extend(row_of(p) for p in res.get("results") or [] if p.get("object") == "page")
    return {"total": len(rows), "rows": rows}


@plugin.tool(read_only=True,
             schema={"type": "object", "properties": {"key": {"type": "string"}},
                     "required": ["key"], "additionalProperties": False})
def get_comments(key: str = ""):
    page, _ = split_key(key)
    require_area("page", page)
    return {"key": page, "comments": comments_view(page)}


@plugin.tool(read_only=False,
             schema={"type": "object",
                     "properties": {"key": {"type": "string"}, "text": {"type": "string"}},
                     "required": ["key", "text"], "additionalProperties": False})
def add_comment(key: str = "", text: str = ""):
    reply(issue=key, text=text)
    return {"ok": True}


@plugin.tool(read_only=False,
             schema={"type": "object",
                     "properties": {"parent": {"type": "string"}, "title": {"type": "string"},
                                    "markdown": {"type": "string"}},
                     "required": ["parent", "title", "markdown"], "additionalProperties": False})
def create_page(parent: str = "", title: str = "", markdown: str = ""):
    page = bare_id(parent)
    require_area("page", page)
    made = create_child_page(page, title, markdown)
    return {"key": bare_id(made.get("id", "")), "url": made.get("url", "")}


if __name__ == "__main__":
    plugin.run()
