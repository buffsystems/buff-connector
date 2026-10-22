"""Google Drive integration — documents, sheets and slides as the team's written
context.

Drive is a knowledge base, not a tracker: its value is what the team WROTE
DOWN — the specification in a document, the plan in a sheet, the decisions in
a deck. So this integration does two jobs, and the first one matters more:

1. It gives the model reading tools over the connection's area, so an answer
   can rest on what the team already wrote instead of on a guess.
2. It takes requests written as comments on a document, sheet or presentation
   and answers in the same discussion; a research request is answered with a
   new DOCUMENT holding the report in the same folder, and a reply carrying
   the link.

Four facts about Drive shape the code:

1. There is NO feed of comments across a drive, and Drive's push notifications
   need a verified HTTPS domain the connector does not have. Discovery is a
   sweep: the area's documents round-robin, a few per pass, asking each for
   the comments modified since reading started.
2. Who the bot IS depends on how the connection signed in. A service account
   is a separate identity people +mention by address; a person's own account
   (OAuth) means the bot writes AS that person — so the bot's own replies are
   told apart by their ids, never by their author.
3. A Google document is read as MARKDOWN through one export call; a sheet is
   read through the Sheets API as rows; a deck as text. No block walking.
4. Where a file is, is a chain of `parents`. The area is a list of roots, and a
   file belongs to it when a root is somewhere up that chain. Parents are looked
   up once and kept.
"""

from __future__ import annotations

import base64
import json
import random
import time
from urllib.parse import quote, urlencode

from buff_plugin import Event, Identity, Plugin, after, event_id, parse_command, source, visible

from content import (
    COMMENTABLE,
    DOC,
    EXPORT,
    FOLDER,
    SHEET,
    SHORTCUT,
    a_minute_before,
    author_text,
    bare_id,
    comment_url,
    commentable_q,
    excerpt,
    is_text,
    kind_of,
    match_score,
    multipart_related,
    q_string,
    search_q,
    sheet_markdown,
    split_key,
    thread_key,
    words_of,
)

plugin = Plugin("google-drive", host_api=4)

DRIVE = plugin.config.get("drive_api", "https://www.googleapis.com").rstrip("/")
SHEETS = plugin.config.get("sheets_api", "https://sheets.googleapis.com").rstrip("/")
ROOTS = {bare_id(r) for r in plugin.config.get("roots", []) if str(r).strip()}
ALIAS = plugin.config.get("mention_alias", "")

# How many documents one poll pass asks for comments. Drive allows twelve
# thousand requests a minute per user; twenty every five seconds is nowhere
# near that, and a bigger area is covered over several passes.
FILES_PER_PASS = 20
# How long the list of the area's documents is trusted before being listed
# again. Listing a whole drive is the expensive part of a pass — a live drive
# of a few thousand files took the first pass past the host's 30-second call
# limit and held a tool call behind it — so it is redone rarely; a document
# created in between is found on the next listing, and its comments are read
# from the floor like any other.
AREA_TTL_SEC = 600

FILE_FIELDS = "id,name,mimeType,parents,webViewLink,modifiedTime,size,shortcutDetails"
LIST_FIELDS = f"nextPageToken,files({FILE_FIELDS})"
AUTHOR = "author(displayName,emailAddress,me)"
COMMENT_FIELDS = (f"nextPageToken,comments(id,{AUTHOR},content,createdTime,modifiedTime,resolved,deleted,"
                  f"replies(id,{AUTHOR},content,createdTime,modifiedTime,deleted))")

me = {"name": "", "email": ""}

_meta: dict[str, dict] = {}
_area: dict = {"at": 0.0, "files": []}
# What a file says, by file: (modifiedTime it was read at, text). A search ranks
# by reading; a file is re-read only once it has been edited.
_bodies: dict[str, tuple[str, str]] = {}
_cursor = {"i": 0}
# Comments and replies already reported, plus the bot's own replies as they
# are written: the sweep reads documents in turns, so the platform's watermark
# cannot be the filter. The floor is where reading started; the set is what
# was already sent. The platform de-duplicates by id upstream as well.
_seen: set[str] = set()
_floor = {"at": ""}


# ---- transport ---------------------------------------------------------

class DriveError(RuntimeError):
    def __init__(self, status: int, path: str, body: str):
        super().__init__(f"Google ответил {status} на {path}: {body[:300]}")
        self.status = status


def _reason(body: str) -> str:
    try:
        err = json.loads(body).get("error") or {}
        reasons = ",".join(str(e.get("reason", "")) for e in err.get("errors") or [])
        return f"{err.get('message', '')} [{reasons}]" if err else body
    except (ValueError, AttributeError):
        return body


def api(method: str, url: str, body: bytes | str | None = None, *, content_type: str = "application/json",
        text: bool = False, attempts: int = 6):
    """One call, with Google's rate limits respected: on 429, on a 403 that
    names a rate limit and on a 5xx the call is retried after a growing,
    jittered pause — never dropped, never hammered."""
    headers = {"Content-Type": content_type} if body is not None else {}
    delay = 1.0
    for attempt in range(attempts):
        if isinstance(body, bytes):
            res = plugin.http(method, url, credential="api", headers=headers, body_bytes=body)
        else:
            res = plugin.http(method, url, credential="api", headers=headers, body=body or "")
        status = res["status"]
        raw = res.get("body", "")
        limited = status == 403 and "ateLimit" in raw
        if (status in (429, 500, 502, 503, 504) or limited) and attempt < attempts - 1:
            wait = float((res.get("headers") or {}).get("Retry-After") or delay)
            time.sleep(wait + random.uniform(0, 0.3))
            delay *= 2
            continue
        if status >= 400:
            raise DriveError(status, url.replace(DRIVE, "").replace(SHEETS, ""), _reason(raw))
        if text:
            return raw
        return json.loads(raw) if raw.strip() else {}
    raise DriveError(429, url, "лимит запросов не отпустил")


def drive(method: str, path: str, params: dict | None = None, body: dict | None = None, *, text: bool = False):
    """A Drive API call. Shared drives are always in view: a document the team
    keeps on one is as much theirs as one on somebody's own drive."""
    q = {"supportsAllDrives": "true", **(params or {})}
    return api(method, f"{DRIVE}/drive/v3{path}?{urlencode(q)}",
               json.dumps(body) if body is not None else None, text=text)


def paged(path: str, params: dict, key: str) -> list:
    """Every result of a paginated listing. Nothing is cut: a folder of a
    thousand files is a thousand files."""
    out: list = []
    token = None
    while True:
        page = drive("GET", path, {**params, **({"pageToken": token} if token else {})})
        out.extend(page.get(key) or [])
        token = page.get("nextPageToken")
        if not token:
            return out


def whoami() -> None:
    who = drive("GET", "/about", {"fields": "user(displayName,emailAddress)"}).get("user") or {}
    me["name"] = str(who.get("displayName") or "")
    me["email"] = str(who.get("emailAddress") or "")


def identity() -> Identity:
    # People address the bot by its display name, by its address (+ or @ is how
    # Docs spells a mention) or by the alias the operator chose.
    email = me["email"]
    return Identity(name=me["name"], key=email, display=me["name"], alias=ALIAS,
                    mentions=tuple(m for m in (f"+{email}", f"@{email}") if email))


def is_service_account() -> bool:
    return me["email"].endswith("gserviceaccount.com")


# ---- where things are ---------------------------------------------------

def meta_of(fid: str, *, fresh: bool = False) -> dict:
    """A file's metadata: read once and kept — a file does not move often, and
    the sweep asks every pass."""
    if fresh or fid not in _meta:
        m = drive("GET", f"/files/{quote(fid, safe='')}", {"fields": FILE_FIELDS})
        # A shortcut is read as what it points at.
        if m.get("mimeType") == SHORTCUT and (m.get("shortcutDetails") or {}).get("targetId"):
            target = str(m["shortcutDetails"]["targetId"])
            m = {**meta_of(target, fresh=fresh), "id": fid, "shortcut_to": target}
        _meta[fid] = m
    return _meta[fid]


def in_area(fid: str) -> bool:
    """Whether a root of the connection is somewhere up the chain of parents.
    No roots means the area is everything the account can see."""
    if not ROOTS:
        return True
    cur = fid
    for _ in range(64):  # a chain deeper than this is not a folder tree
        if cur in ROOTS:
            return True
        try:
            parents = meta_of(cur).get("parents") or []
        except DriveError:
            return False  # unreadable = not shared with the account = not ours
        if not parents:
            return False
        cur = str(parents[0])
    return False


def require_area(fid: str) -> None:
    if not in_area(fid):
        raise RuntimeError(f"{fid} вне области, разрешённой этому подключению")


def children_of(folder: str) -> list[dict]:
    found = paged("/files", {"q": f"{q_string(folder)} in parents and trashed = false",
                             "fields": LIST_FIELDS, "pageSize": 1000,
                             "includeItemsFromAllDrives": "true", "orderBy": "folder,name"}, "files")
    for f in found:
        _meta[f["id"]] = f
    return found


def area_files() -> list[dict]:
    """Every document, sheet and deck of the area — what the sweep walks."""
    if _area["files"] and time.time() - _area["at"] < AREA_TTL_SEC:
        return _area["files"]
    started = time.time()
    files: list[dict] = []
    if not ROOTS:
        files = paged("/files", {"q": f"trashed = false and {commentable_q()}", "fields": LIST_FIELDS,
                                 "pageSize": 1000, "includeItemsFromAllDrives": "true",
                                 "orderBy": "modifiedTime desc"}, "files")
        for f in files:
            _meta[f["id"]] = f
    else:
        queue = []
        for root in sorted(ROOTS):
            try:
                m = meta_of(root)
            except DriveError as exc:
                plugin.log(f"корень {root} не прочитан: {exc}")
                continue
            if m.get("mimeType") == FOLDER:
                queue.append(root)
            elif m.get("mimeType") in COMMENTABLE:
                files.append(m)
        seen_folders: set[str] = set()
        while queue:
            folder = queue.pop(0)
            if folder in seen_folders:
                continue
            seen_folders.add(folder)
            for f in children_of(folder):
                if f.get("mimeType") == FOLDER:
                    queue.append(f["id"])
                elif f.get("mimeType") in COMMENTABLE:
                    files.append(f)
    _area["at"], _area["files"] = time.time(), files
    plugin.log(f"область: {len(files)} файлов с обсуждениями, перечислено за {time.time() - started:.1f} с")
    return files


# ---- reading ------------------------------------------------------------

def export_text(fid: str, mime: str) -> str:
    return drive("GET", f"/files/{quote(fid, safe='')}/export", {"mimeType": mime}, text=True)


def sheets_get(sid: str, path: str, params: dict) -> dict:
    return api("GET", f"{SHEETS}/v4/spreadsheets/{quote(sid, safe='')}{path}?{urlencode(params, doseq=True)}")


def sheet_titles(sid: str) -> list[str]:
    book = sheets_get(sid, "", {"fields": "sheets.properties.title"})
    return [str((s.get("properties") or {}).get("title") or "") for s in book.get("sheets") or []]


def sheet_text(sid: str, ranges: list[str]) -> str:
    """The named ranges (a sheet title is a range) as markdown tables."""
    if not ranges:
        return "(в таблице нет листов)\n"
    got = sheets_get(sid, "/values:batchGet", {"ranges": ranges, "majorDimension": "ROWS"})
    parts = []
    for vr in got.get("valueRanges") or []:
        title = str(vr.get("range") or "").split("!")[0].strip("'")
        parts.append(sheet_markdown(title, vr.get("values") or []))
    return "\n".join(parts)


def file_text(m: dict) -> dict:
    """What a person reads of the file, by its kind."""
    fid, mime = str(m.get("id")), str(m.get("mimeType") or "")
    if mime in EXPORT:
        return {"markdown": export_text(fid, EXPORT[mime])}
    if mime == SHEET:
        return {"markdown": sheet_text(fid, sheet_titles(fid))}
    if mime == FOLDER:
        return {"children": [file_item(f) for f in children_of(fid)]}
    if is_text(mime):
        return {"markdown": drive("GET", f"/files/{quote(fid, safe='')}", {"alt": "media"}, text=True)}
    return {"note": f"{kind_of(mime)} ({mime}) — текст не читается; размер {m.get('size', '?')} байт"}


def body_of(m: dict) -> str:
    """The file's text for searching, read once per edit."""
    fid, edited = str(m.get("id")), str(m.get("modifiedTime") or "")
    hit = _bodies.get(fid)
    if hit and hit[0] == edited:
        return hit[1]
    got = file_text(m)
    text = str(got.get("markdown") or got.get("note") or "")
    _bodies[fid] = (edited, text)
    return text


def file_item(m: dict) -> dict:
    return {"key": m.get("id", ""), "kind": kind_of(str(m.get("mimeType") or "")), "title": m.get("name", ""),
            "url": m.get("webViewLink", ""), "updated": m.get("modifiedTime", "")}


# ---- requests in comments ------------------------------------------------

def comments_of(fid: str, since: str = "") -> list[dict]:
    params = {"fields": COMMENT_FIELDS, "pageSize": 100, "includeDeleted": "false"}
    if since:
        params["startModifiedTime"] = since
    return paged(f"/files/{quote(fid, safe='')}/comments", params, "comments")


def own_voice(item: dict) -> bool:
    """A service account's own comments are never requests. A person's own
    account is another matter: they ask AND the bot answers as them, so their
    comments are told apart by id, not by author."""
    return bool((item.get("author") or {}).get("me")) and is_service_account()


def comment_events(m: dict, c: dict, floor: str | None) -> list[Event]:
    """One discussion — the comment and its replies — turned into requests, or
    into nothing. Every path that produces an event goes through here, so the
    area check, the bot's own voice and the address are decided in one place."""
    fid = str(m.get("id"))
    if not in_area(fid):
        return []
    out: list[Event] = []
    for item in [c, *(c.get("replies") or [])]:
        cid = str(item.get("id") or "")
        if not cid or cid in _seen or item.get("deleted"):
            continue
        at = str(item.get("createdTime") or "")
        if floor and not after(at, floor):
            continue
        if own_voice(item):
            continue
        cmd = parse_command(str(item.get("content") or ""), identity())
        if not cmd:
            continue
        _seen.add(cid)
        out.append(Event(
            id=event_id("comment", cid, at), at=at, kind="comment",
            title=str(m.get("name") or ""),
            body=cmd.instruction,
            author=author_text(item.get("author")),
            # Who asked, for the connection's «who may ask» rules (#476). Drive
            # has no login; `emailAddress` is absent when the person keeps it
            # hidden from the reader, and then the e-mail is empty.
            author_email=str((item.get("author") or {}).get("emailAddress") or ""),
            url=comment_url(str(m.get("webViewLink") or ""), str(c.get("id") or "")),
            action=cmd.action,
            # Drive holds no code, so a request from it carries no repository and
            # no branch: the answer is computed on the project the connection
            # points at.
            extra=source(thread_key(fid, str(c.get("id") or "")), trigger="mention", context="issue"),
        ))
    return out


def comments_view(fid: str) -> list[dict]:
    out = []
    for c in comments_of(fid):
        out.append({"comment": c.get("id", ""), "author": author_text(c.get("author")),
                    "created": c.get("createdTime", ""), "resolved": bool(c.get("resolved")),
                    "text": c.get("content", ""),
                    "replies": [{"author": author_text(r.get("author")), "created": r.get("createdTime", ""),
                                 "text": r.get("content", "")} for r in c.get("replies") or []]})
    return out


# ---- writing -----------------------------------------------------------

def post_reply(fid: str, comment: str, content: str) -> dict:
    made = drive("POST", f"/files/{quote(fid, safe='')}/comments/{quote(comment, safe='')}/replies",
                 {"fields": "id"}, {"content": content})
    _seen.add(str(made.get("id") or ""))
    return made


def post_comment(fid: str, content: str) -> dict:
    made = drive("POST", f"/files/{quote(fid, safe='')}/comments", {"fields": "id"}, {"content": content})
    _seen.add(str(made.get("id") or ""))
    return made


def upload(folder: str, name: str, target_mime: str, media_type: str, media: bytes) -> dict:
    """A file created in one multipart call; a Google type as `mimeType` makes
    Drive IMPORT the bytes — markdown becomes a document."""
    meta: dict = {"name": name or "Без названия", "mimeType": target_mime}
    if folder:
        meta["parents"] = [folder]
    content_type, body = multipart_related(meta, media_type, media)
    q = urlencode({"uploadType": "multipart", "supportsAllDrives": "true", "fields": FILE_FIELDS})
    return api("POST", f"{DRIVE}/upload/drive/v3/files?{q}", body, content_type=content_type)


def create_doc(folder: str, title: str, markdown: str) -> dict:
    return upload(folder, title, DOC, "text/markdown", (markdown or "").encode())


def folder_of(fid: str) -> str:
    """Where a report goes: beside the file it was asked on; failing that, the
    first root; failing that, the account's own drive."""
    parents = meta_of(fid).get("parents") or []
    if parents:
        return str(parents[0])
    return sorted(ROOTS)[0] if ROOTS else ""


# ---- plugin surface ----------------------------------------------------

@plugin.check
def check():
    whoami()
    who = f"{me['name']} ({me['email']})" if me["name"] and me["email"] else (me["name"] or me["email"])
    how = "сервисный аккаунт" if is_service_account() else "учётная запись"
    if not ROOTS:
        return {"ok": True, "detail": f"Google Диск, {how} «{who}». Бот видит всё, что открыто этой учётной записи."}
    seen = 0
    for root in sorted(ROOTS):
        try:
            meta_of(root, fresh=True)
            seen += 1
        except DriveError:
            continue
    sees = visible(seen, len(ROOTS), one="указанную папку или файл", many="указанные папки и файлы",
                   many_of="указанных папок и файлов", fix="Доступ боту выдаётся в самом Google Диске.")
    return {"ok": True, "detail": f"Google Диск, {how} «{who}». {sees}"}


@plugin.poll
def poll(since):
    """A few documents of the area per pass, round-robin, every comment or
    reply newer than where reading started and not yet reported."""
    if not me["email"]:
        whoami()
    if not _floor["at"]:
        _floor["at"] = since or ""
    files = area_files()
    if not files:
        return []
    start = _cursor["i"] % len(files)
    batch = [files[(start + n) % len(files)] for n in range(min(FILES_PER_PASS, len(files)))]
    _cursor["i"] = (start + len(batch)) % len(files)
    events: list[Event] = []
    for m in batch:
        try:
            for c in comments_of(str(m["id"]), a_minute_before(_floor["at"]) if _floor["at"] else ""):
                events.extend(comment_events(m, c, _floor["at"]))
        except DriveError as exc:
            plugin.log(f"файл {m.get('id')} не прочитан: {exc}")
    return events


@plugin.reply
def reply(issue: str = "", text: str = "", link: dict | None = None,
          attachment: dict | None = None, document: dict | None = None, **_):
    fid, comment = split_key(issue)
    # The area bounds writes too, not only reads.
    require_area(fid)
    parts: list[str] = []
    if text and text.strip():
        parts.append(text.strip())
    if link and link.get("url"):
        parts.append(f"{link.get('title') or 'Открыть в Buff'}: {link['url']}")
    if document or attachment:
        # A finished report is a DOCUMENT of its own, in the folder of the one
        # it was asked on — the text itself, not a picture of it; the PDF goes
        # beside it for those who want one.
        doc = document or {}
        folder = folder_of(fid)
        title = str(doc.get("title") or (attachment or {}).get("filename") or "Исследование")
        made = create_doc(folder, title, str(doc.get("markdown") or ""))
        parts.append(f"Отчёт — в документе «{title}»: {made.get('webViewLink', '')}")
        if attachment:
            pdf = upload(folder, str(attachment.get("filename") or "report.pdf"), "application/pdf",
                         "application/pdf", base64.b64decode(attachment.get("base64", "")))
            parts.append(f"PDF: {pdf.get('webViewLink', '')}")
    if not parts:
        return {"ok": True}
    content = "\n\n".join(parts)
    if comment:
        post_reply(fid, comment, content)
    else:
        post_comment(fid, content)
    return {"ok": True}


@plugin.tool(description="Найти документы, таблицы, презентации и папки на Google Диске по словам — в "
                         "названии И в тексте файлов: Диск ищет по содержимому, интеграция ранжирует найденное, "
                         "читая текст (слова сравниваются по основе, так что падежи не мешают). Использовать "
                         "ПЕРВЫМ делом, когда вопрос касается требований, регламентов, планов или того, «как у нас "
                         "принято»; затем читать найденное get_file.",
             read_only=True,
             schema={"type": "object",
                     "properties": {"query": {"type": "string"}, "limit": {"type": "integer"}},
                     "required": ["query"], "additionalProperties": False})
def search_files(query: str = "", limit: int = 20):
    words = words_of(query)
    found = drive("GET", "/files", {
        "q": search_q(words), "fields": LIST_FIELDS, "pageSize": max(1, min(int(limit or 20), 100)),
        "includeItemsFromAllDrives": "true", "orderBy": "modifiedTime desc",
    }).get("files") or []
    hits: dict[str, dict] = {}
    for f in found:
        _meta[f["id"]] = f
        if in_area(f["id"]):
            hits[f["id"]] = f
    # Drive matched Drive's way — whole words, its own tokens. When that found
    # nothing of the area, the area's own documents are read and looked through
    # by stem: the question's form of a word is rarely the document's.
    if not hits and words:
        hits = {f["id"]: f for f in area_files()}
    scored = []
    for f in hits.values():
        text = ""
        if words and f.get("mimeType") != FOLDER:
            try:
                text = body_of(f)
            except DriveError as exc:
                plugin.log(f"файл {f.get('id')} не прочитан для поиска: {exc}")
        by_text = match_score(text, words)
        by_name = match_score(str(f.get("name") or ""), words)
        if words and not by_text and not by_name and f["id"] not in {x["id"] for x in found}:
            continue
        item = {**file_item(f), "matched": "текст" if by_text else "название"}
        if by_text:
            item["excerpt"] = excerpt(text, words)
        scored.append((by_text * 2 + by_name, item))
    scored.sort(key=lambda s: -s[0])
    out = [item for _, item in scored]
    return {"total": len(out), "results": out, "note": "Читайте найденное get_file; папки — list_folder."}


@plugin.tool(description="Прочитать файл Google Диска целиком: документ — как markdown, таблицу — все листы "
                         "таблицами, презентацию — текстом, папку — списком содержимого; вместе с обсуждением. "
                         "Использовать, когда поиск нашёл подходящий файл или когда в вопросе назвали его адрес.",
             read_only=True,
             schema={"type": "object", "properties": {"key": {"type": "string"}},
                     "required": ["key"], "additionalProperties": False})
def get_file(key: str = ""):
    fid, _ = split_key(key)
    require_area(fid)
    m = meta_of(fid, fresh=True)
    return {**file_item(m), "mime": m.get("mimeType", ""), "parent": (m.get("parents") or [""])[0],
            **file_text(m), "comments": comments_view(fid) if m.get("mimeType") in COMMENTABLE else []}


@plugin.tool(description="Перечислить содержимое папки Google Диска: вложенные папки и файлы с их видом. "
                         "Использовать, когда ответ собирается из нескольких файлов одного раздела.",
             read_only=True,
             schema={"type": "object", "properties": {"key": {"type": "string"}},
                     "required": ["key"], "additionalProperties": False})
def list_folder(key: str = ""):
    fid, _ = split_key(key)
    require_area(fid)
    kids = [file_item(f) for f in children_of(fid)]
    return {"total": len(kids), "children": kids}


@plugin.tool(description="Прочитать один лист или диапазон таблицы Google (например лист «Бюджет» или "
                         "диапазон A1:D20 на нём). Использовать, когда таблица большая и нужна её часть; "
                         "без листа и диапазона — вся таблица.",
             read_only=True,
             schema={"type": "object",
                     "properties": {"key": {"type": "string"}, "sheet": {"type": "string"},
                                    "range": {"type": "string"}},
                     "required": ["key"], "additionalProperties": False})
def get_sheet(key: str = "", sheet: str = "", range: str = ""):  # noqa: A002 — the API's own word
    sid, _ = split_key(key)
    require_area(sid)
    if sheet and range:
        ranges = [f"'{sheet}'!{range}"]
    elif sheet or range:
        ranges = [f"'{sheet}'" if sheet else range]
    else:
        ranges = sheet_titles(sid)
    return {"key": sid, "markdown": sheet_text(sid, ranges)}


@plugin.tool(description="Обсуждения файла Google Диска: ветки комментариев с ответами, кто и когда писал. "
                         "Использовать, когда важно, что уже обсуждали по этому файлу.",
             read_only=True,
             schema={"type": "object", "properties": {"key": {"type": "string"}},
                     "required": ["key"], "additionalProperties": False})
def get_comments(key: str = ""):
    fid, _ = split_key(key)
    require_area(fid)
    return {"key": fid, "comments": comments_view(fid)}


@plugin.tool(description="Написать комментарий к файлу Google Диска — или ответить в ветке, если ключ вида "
                         "ФАЙЛ#комментарий.",
             read_only=False,
             schema={"type": "object",
                     "properties": {"key": {"type": "string"}, "text": {"type": "string"}},
                     "required": ["key", "text"], "additionalProperties": False})
def add_comment(key: str = "", text: str = ""):
    reply(issue=key, text=text)
    return {"ok": True}


@plugin.tool(description="Создать документ Google из markdown в папке области подключения. Использовать, "
                         "когда результат нужно оставить на Диске документом, а не комментарием.",
             read_only=False,
             schema={"type": "object",
                     "properties": {"folder": {"type": "string"}, "title": {"type": "string"},
                                    "markdown": {"type": "string"}},
                     "required": ["folder", "title", "markdown"], "additionalProperties": False})
def create_document(folder: str = "", title: str = "", markdown: str = ""):
    fid = bare_id(folder)
    require_area(fid)
    made = create_doc(fid, title, markdown)
    return {"key": made.get("id", ""), "url": made.get("webViewLink", "")}


if __name__ == "__main__":
    plugin.run()
