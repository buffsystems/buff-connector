"""Figma integration — the design as a source of knowledge.

A design file is the third thing a team writes down besides its tickets and its
code: what the screen is supposed to look like, which tokens it is built from,
how the layout behaves. This integration gives the model READING tools over the
marked files and takes requests written as comments on a design, the way the
tracker integrations do, answering in the same thread.

NOT EVERY MODEL CAN SEE. Most of the models people run this on are text-only, so
the integration is built parameters-first: every tool but one answers with the
numbers and names the inspector panel shows — bounds, auto-layout, paints as
hex, typography, the styles and variables bound to each field, the component an
instance came from. The one tool that answers with a PICTURE (`render_node`) is
marked `vision` and the platform hands it only to a model that reads images.
Nothing here depends on that tool existing.

What decides the code, from the REST API's own specification (figma/rest-api-spec):

1. Comments are per FILE and unfiltered: `GET /v1/files/:key/comments` returns
   every comment ever left, with `parent_id` on replies and `order_id` on the
   root of a thread. The thread IS the root comment, so a question asked in a
   reply is answered in the same thread; the poll filters by `created_at`.
   With no files and no projects listed the area is every file the account can
   open (#429), and since no endpoint lists those, the FILE_COMMENT callback is
   what names the file a question was asked in.
2. A node carries style IDS in `styles` and variable IDS in `boundVariables`; the
   NAMES live in the response's top-level `styles` map and in the Variables
   endpoint. Names are what an answer should say, so both are resolved here.
3. `GET /v1/files/:key/variables/local` is Enterprise-only (403 elsewhere). The
   tokens are then read from published styles and from `boundVariables` ids —
   what the plan gives, said plainly, rather than an empty list.
4. A published style names a NODE; the node, fetched, carries the value — fills
   for a colour, `style` for a text style, effects, layout grids.
5. Rendered images come from a URL on Figma's storage, not from api.figma.com,
   so the manifest names that host as a second egress rule; the bytes travel
   base64 (buff/http `binary`), because a PNG is not text.
"""

import json
import time
from urllib.parse import quote

from buff_plugin import (
    Event,
    Identity,
    Plugin,
    Scope,
    say,
    visible,
    after,
    event_id,
    parse_command,
    source,
)

from design import (
    Context,
    compress,
    matches,
    outline,
    style_value,
    variables_of,
    walk,
    words_of,
)

plugin = Plugin("figma")

BASE = plugin.config.get("base_url", "https://api.figma.com").rstrip("/")
IMAGES = plugin.config.get("images_base", "").rstrip("/")
FILES = [str(k).strip() for k in plugin.config.get("files", []) if str(k).strip()]
PROJECTS = [str(p).strip() for p in plugin.config.get("projects", []) if str(p).strip()]
ALIAS = plugin.config.get("mention_alias", "")
WEB = "https://www.figma.com"

# How long the list of a project's files is trusted before it is read again. A
# file added to a marked project must be seen without a restart; a request per
# project per poll would be wasteful for a list that changes weekly.
PROJECT_FILES_TTL_SEC = 300

me = {"id": "", "handle": "", "email": ""}
_project_files: dict = {"at": 0.0, "files": {}}   # key → {name, project}
_file_names: dict = {}
# Files where a callback found the bot mentioned. With no files and no projects
# listed, Figma cannot say which files exist, so these are what the poll reads
# to recover a callback that was lost.
_mentioned: set = set()


# ---- transport ---------------------------------------------------------

class FigmaError(RuntimeError):
    def __init__(self, status: int, path: str, body: str):
        super().__init__(say(f"Figma ответила {status} на {path}: {body[:300]}",
                             f"Figma answered {status} to {path}: {body[:300]}"))
        self.status = status


def api(method: str, path: str, body: dict | None = None):
    res = plugin.http(method, BASE + path, credential="api",
                      headers={"Content-Type": "application/json"} if body else {},
                      body=json.dumps(body) if body else "")
    if res["status"] >= 400:
        raise FigmaError(res["status"], path, res.get("body", ""))
    return json.loads(res["body"]) if res.get("body", "").strip() else {}


def whoami() -> None:
    who = api("GET", "/v1/me")
    me["id"] = str(who.get("id", ""))
    me["handle"] = str(who.get("handle", ""))
    me["email"] = str(who.get("email", ""))


def signed_in() -> str:
    return f"{me['handle']} ({me['email']})" if me["email"] else me["handle"]


def identity() -> Identity:
    return Identity(name=me["handle"], key=me["id"], display=me["handle"], alias=ALIAS)


# ---- scope -------------------------------------------------------------

def project_files(force: bool = False) -> dict:
    """Files of the marked projects, `key → {name, project}`, refreshed on a TTL."""
    if not force and _project_files["files"] and time.time() - _project_files["at"] < PROJECT_FILES_TTL_SEC:
        return _project_files["files"]
    found: dict = {}
    for pid in PROJECTS:
        listed = api("GET", f"/v1/projects/{quote(pid)}/files")
        for f in listed.get("files", []):
            found[str(f.get("key"))] = {"name": f.get("name", ""), "project": listed.get("name", pid),
                                        "updated": f.get("last_modified")}
    _project_files["at"] = time.time()
    _project_files["files"] = found
    return found


def scope_files() -> dict:
    """Every file the bot may read and write in: the listed ones plus the
    projects' — one dict, because a file named twice is one file."""
    out = dict(project_files()) if PROJECTS else {}
    for key in FILES:
        out.setdefault(key, {"name": _file_names.get(key, ""), "project": ""})
    return out


def unlisted() -> bool:
    """No files and no projects: the area is every file the bot's account can
    open (#429). Figma's own sharing already draws that line."""
    return not FILES and not PROJECTS


def scope() -> Scope:
    # The marked files ARE the access control — see Scope in the SDK — and the
    # same list bounds reads and writes. Unlike a tracker, an empty list is not
    # "nowhere": see `unlisted`.
    return Scope(list(scope_files().keys()))


def in_area(key: str) -> bool:
    return unlisted() or scope().allows(key)


def require_area(key: str) -> None:
    if not in_area(key):
        scope().require(key)


def watched_files() -> dict:
    """What the poll reads: the listed files, or — with nothing listed — the
    files where the bot has been mentioned."""
    if not unlisted():
        return scope_files()
    return {key: {"name": _file_names.get(key, ""), "project": ""} for key in sorted(_mentioned)}


def file_name(key: str) -> str:
    if key not in _file_names:
        try:
            meta = api("GET", f"/v1/files/{quote(key)}/meta").get("file") or {}
            _file_names[key] = str(meta.get("name", ""))
        except FigmaError:
            _file_names[key] = ""
    return _file_names[key]


# ---- threads -----------------------------------------------------------

def thread_key(file_key: str, root_id) -> str:
    """`Kb7Cabinet01#123456` — the file and the ROOT comment of the thread."""
    return f"{file_key}#{root_id}"


def split_key(key: str) -> tuple[str, str]:
    file_key, sep, root = str(key).rpartition("#")
    if not sep or not file_key or not root:
        raise RuntimeError(say(f"{key} — не адрес обсуждения Figma (нужно КЛЮЧ_ФАЙЛА#номер комментария)",
                               f"{key} is not a Figma discussion address (FILE_KEY#comment number is needed)"))
    return file_key, root


def pinned_node(comment: dict, root: dict | None = None) -> str:
    """The node a thread is pinned to. Only the ROOT comment carries the pin; a
    reply's own client_meta is empty, so the thread's pin comes from its root."""
    own = ((comment.get("client_meta") or {}).get("node_id") or "")
    return str(own or ((root or {}).get("client_meta") or {}).get("node_id") or "")


def comment_url(file_key: str, comment: dict, root: dict | None = None) -> str:
    node = pinned_node(comment, root)
    url = f"{WEB}/design/{file_key}"
    if node:
        url += f"?node-id={str(node).replace(':', '-')}"
    return url + f"#{comment.get('id', '')}"


def comment_event(file_key: str, c: dict, since: str | None, roots: dict | None = None) -> Event | None:
    """One comment, turned into a request — or into nothing.

    Every path that produces an event goes through here, which is why the scope
    check lives here rather than in the file list (#282). `roots` maps comment
    ids to root comments, so a reply is placed at its thread's pin.
    """
    if not in_area(file_key):
        return None
    user = c.get("user") or {}
    if identity().is_me(user.get("id"), user.get("handle")):
        return None  # the bot's own replies are not requests
    at = str(c.get("created_at", ""))
    if not after(at, since):
        return None
    cmd = parse_command(str(c.get("message", "")), identity())
    if not cmd:
        return None
    root = c.get("parent_id") or c.get("id")
    root_comment = (roots or {}).get(str(root))
    node = pinned_node(c, root_comment)
    name = file_name(file_key)
    return Event(
        id=event_id("comment", c.get("id"), at), at=at, kind="comment",
        title=f"{name}: {say('узел ', 'node ') + str(node) if node else say('обсуждение #', 'discussion #') + str(c.get('order_id') or root)}",
        body=cmd.instruction,
        author=str(user.get("handle", "")),
        # Who asked, for the connection's «who may ask» rules (#476). A handle is
        # a display name anyone may change, so it is no login; Figma documents a
        # user's `email` on /v1/me only, so it is passed on if a comment ever
        # carries one, and is empty otherwise.
        author_email=str(user.get("email") or ""),
        url=comment_url(file_key, c, root_comment),
        action=cmd.action,
        # A design holds no code, so a request from it carries no repository and
        # no branch: the answer is computed on the project the connection points at.
        extra=source(thread_key(file_key, root), trigger="mention", context="issue"),
    )


def comments_of(file_key: str) -> list[dict]:
    return sorted(api("GET", f"/v1/files/{quote(file_key)}/comments").get("comments", []),
                  key=lambda c: str(c.get("created_at", "")))


# ---- plugin surface ----------------------------------------------------

def variables_available(file_key: str) -> tuple[bool, str]:
    """Whether the Variables endpoint answers on this plan, said in the
    operator's terms: Enterprise-only is a fact about the plan, not a fault."""
    try:
        api("GET", f"/v1/files/{quote(file_key)}/variables/local")
        return True, say("Переменные доступны.", "Variables are available.")
    except FigmaError as e:
        if e.status in (401, 403):
            return False, say("На этом тарифе Figma переменные недоступны — цвета и размеры бот берёт из стилей.",
                              "Variables are not available on this Figma plan — the bot takes colours and sizes from the styles.")
        return False, say(f"Переменные не прочитались: {e}", f"The variables could not be read: {e}")


@plugin.check
def check():
    whoami()
    if unlisted():
        return {"ok": True, "detail": say(
            f"Figma, вошли как {signed_in()}. Бот видит все файлы, открытые учётной записи; "
            "вопросы из комментариев приходят через вебхук.",
            f"Figma, signed in as {signed_in()}. The bot sees every file open to the account; "
            "questions from comments arrive through the webhook.")}
    files = scope_files()
    seen = 0
    for key in files:
        try:
            _file_names[key] = str((api("GET", f"/v1/files/{quote(key)}/meta").get("file") or {}).get("name", ""))
            seen += 1
        except FigmaError:
            pass
    variables = say("Переменные не проверялись: файлов пока нет.", "Variables were not checked: there are no files yet.")
    if files:
        variables = variables_available(next(iter(files)))[1]
    sees = visible(seen, len(files), one="указанный файл", many="указанные файлы", many_of="указанных файлов",
                   fix="Откройте их учётной записи бота в самой Figma.",
                   en=dict(one="the listed file", many="listed files", fix="Share them with the bot's account in Figma itself."))
    return {"ok": True, "detail": say(f"Figma, вошли как {signed_in()}. {sees} {variables}",
                                      f"Figma, signed in as {signed_in()}. {sees} {variables}")}


@plugin.poll
def poll(since):
    if not me["id"]:
        whoami()
    events: list[Event] = []
    for key in watched_files():
        try:
            comments = comments_of(key)
        except FigmaError as e:
            # One file the token cannot read (moved, access revoked, deleted)
            # must not silence every other file in the scope.
            plugin.log(say(f"файл {key} не прочитался, пропущен в этом проходе: {e}",
                           f"file {key} could not be read, skipped in this pass: {e}"))
            continue
        roots = {str(c.get("id")): c for c in comments if not c.get("parent_id")}
        for c in comments:
            e = comment_event(key, c, since, roots)
            if e:
                events.append(e)
    return events


@plugin.webhook
def webhook(headers: dict, body: str):
    """A FILE_COMMENT callback names the file and the comment; the comment is
    read from the API, because the payload carries no parent_id and the thread
    to answer in is its ROOT. With nothing listed this is how a question in any
    file is found at all."""
    try:
        ev = json.loads(body or "{}")
    except ValueError:
        return []
    key = str(ev.get("file_key") or "").strip()
    if ev.get("event_type") != "FILE_COMMENT" or not key or not in_area(key):
        return []
    if not me["id"]:
        whoami()
    comments = comments_of(key)
    roots = {str(c.get("id")): c for c in comments if not c.get("parent_id")}
    wanted = str(ev.get("comment_id") or "")
    found = [e for c in comments if str(c.get("id")) == wanted
             for e in [comment_event(key, c, None, roots)] if e]
    if found:
        _mentioned.add(key)
    return found


def comment_body(text: str, link: dict | None, attachment: dict | None) -> str:
    parts = [text.strip()] if text and text.strip() else []
    if link and link.get("url"):
        parts.append(f"{link.get('title') or say('Открыть в Buff', 'Open in Buff')}: {link['url']}")
    if attachment:
        # A Figma comment takes no files. The document is not lost — it is behind
        # the link above — and saying so beats a silently missing attachment.
        name = attachment.get('filename', 'report.pdf')
        parts.append(say(f"Документ «{name}» к комментарию Figma приложить нельзя — он доступен по ссылке выше.",
                         f"The document “{name}” cannot be attached to a Figma comment — it is available by the "
                         "link above."))
    return "\n\n".join(parts)


@plugin.reply
def reply(issue: str = "", text: str = "", link: dict | None = None,
          attachment: dict | None = None, **_):
    file_key, root = split_key(issue)
    # The marked files bound writes too, not only reads.
    require_area(file_key)
    body = comment_body(text, link, attachment)
    if not body:
        return {"ok": True}
    api("POST", f"/v1/files/{quote(file_key)}/comments", {"message": body, "comment_id": str(root)})
    return {"ok": True}


# ---- reading tools -------------------------------------------------------

def context_of(payload: dict, file_key: str) -> Context:
    """Names for the ids in a file/nodes response, plus variable names when the
    plan lets them be read (cached per file — a network call otherwise)."""
    return Context(styles=payload.get("styles") or {}, components=payload.get("components") or {},
                   variables=variable_names(file_key))


_variable_names: dict = {}


def variable_names(file_key: str) -> dict:
    if file_key not in _variable_names:
        try:
            meta = api("GET", f"/v1/files/{quote(file_key)}/variables/local").get("meta") or {}
            _variable_names[file_key] = variables_of(meta)[1]
        except FigmaError:
            _variable_names[file_key] = {}
    return _variable_names[file_key]


def _require(file_key: str) -> None:
    require_area(file_key)


@plugin.tool(read_only=True,
             schema={"type": "object", "properties": {}, "additionalProperties": False})
def list_files():
    out = []
    for key, meta in watched_files().items():
        out.append({"key": key, "name": meta.get("name") or file_name(key), "project": meta.get("project", ""),
                    "updated": meta.get("updated"), "url": f"{WEB}/design/{key}"})
    area = (say("все файлы, открытые учётной записи бота; в списке — те, где бота уже упоминали, "
                "любой другой открывается по ключу из ссылки figma.com/design/КЛЮЧ/…",
                "all the files open to the bot's account; the list holds those where the bot was already "
                "mentioned, any other opens by the key from a figma.com/design/KEY/… link") if unlisted()
            else say("перечисленные файлы и файлы перечисленных проектов",
                     "the listed files and the files of the listed projects"))
    return {"area": area, "total": len(out), "files": out}


@plugin.tool(read_only=True,
             schema={"type": "object",
                     "properties": {"key": {"type": "string"}, "depth": {"type": "integer"}},
                     "required": ["key"], "additionalProperties": False})
def get_file(key: str = "", depth: int = 2):
    _require(key)
    payload = api("GET", f"/v1/files/{quote(key)}?depth={int(depth or 2)}")
    _file_names[key] = str(payload.get("name", ""))
    document = payload.get("document") or {}
    styles = payload.get("styles") or {}
    components = payload.get("components") or {}
    return {
        "key": key, "name": payload.get("name"), "updated": payload.get("lastModified"),
        "version": payload.get("version"), "url": f"{WEB}/design/{key}",
        "pages": [outline(p) for p in document.get("children", [])],
        "styles": [{"name": s.get("name"), "type": str(s.get("styleType", "")).lower(), "remote": bool(s.get("remote"))}
                   for s in styles.values()],
        "components": [{"id": cid, "name": c.get("name"), "description": c.get("description", "")}
                       for cid, c in components.items()],
    }


@plugin.tool(read_only=True,
             schema={"type": "object",
                     "properties": {"key": {"type": "string"}, "node_id": {"type": "string"},
                                    "depth": {"type": "integer"}},
                     "required": ["key", "node_id"], "additionalProperties": False})
def get_node(key: str = "", node_id: str = "", depth: int | None = None):
    _require(key)
    path = f"/v1/files/{quote(key)}/nodes?ids={quote(str(node_id))}"
    if depth:
        path += f"&depth={int(depth)}"
    payload = api("GET", path)
    entry = (payload.get("nodes") or {}).get(str(node_id))
    if not entry:
        raise RuntimeError(say(f"узла {node_id} в файле {key} нет", f"there is no node {node_id} in file {key}"))
    return {"key": key, "file": payload.get("name"), "url": f"{WEB}/design/{key}?node-id={str(node_id).replace(':', '-')}",
            "node": compress(entry.get("document") or {}, context_of(entry, key))}


@plugin.tool(read_only=True,
             schema={"type": "object", "properties": {"key": {"type": "string"}},
                     "required": ["key"], "additionalProperties": False})
def get_styles(key: str = ""):
    _require(key)
    published = (api("GET", f"/v1/files/{quote(key)}/styles").get("meta") or {}).get("styles", [])
    values: dict = {}
    if published:
        ids = ",".join(quote(str(s.get("node_id"))) for s in published if s.get("node_id"))
        nodes = api("GET", f"/v1/files/{quote(key)}/nodes?ids={ids}").get("nodes") or {}
        for nid, entry in nodes.items():
            values[nid] = entry
    ctx = Context(variables=variable_names(key))
    styles = []
    for s in published:
        entry = values.get(str(s.get("node_id"))) or {}
        styles.append({"name": s.get("name"), "type": str(s.get("style_type", "")).lower(),
                       "description": s.get("description", ""),
                       "value": style_value(s.get("style_type", ""), entry.get("document"), ctx),
                       "updated": s.get("updated_at")})
    used = api("GET", f"/v1/files/{quote(key)}?depth=1").get("styles") or {}
    available, reason = variables_available(key)
    variables: dict = {"available": available, "note": reason}
    if available:
        meta = api("GET", f"/v1/files/{quote(key)}/variables/local").get("meta") or {}
        collections, names = variables_of(meta)
        _variable_names[key] = names
        variables["collections"] = collections
    return {"key": key, "published": styles,
            "used": [{"name": s.get("name"), "type": str(s.get("styleType", "")).lower(), "remote": bool(s.get("remote"))}
                     for s in used.values()],
            "variables": variables}


@plugin.tool(read_only=True,
             schema={"type": "object",
                     "properties": {"key": {"type": "string"}, "query": {"type": "string"},
                                    "types": {"type": "array", "items": {"type": "string"}}},
                     "required": ["key", "query"], "additionalProperties": False})
def search_nodes(key: str = "", query: str = "", types: list | None = None):
    _require(key)
    words = words_of(query)
    kinds = [str(t).lower() for t in (types or [])]
    payload = api("GET", f"/v1/files/{quote(key)}")
    _file_names[key] = str(payload.get("name", ""))
    found = []
    for page in (payload.get("document") or {}).get("children", []):
        for node, path in walk(page):
            if node is page or not matches(node, words, kinds):
                continue
            hit = {"id": node.get("id"), "name": node.get("name"), "type": str(node.get("type", "")).lower(),
                   "page": page.get("name"), "path": " / ".join(p for p in path[1:] if p)}
            if node.get("type") == "TEXT":
                hit["text"] = node.get("characters", "")
            found.append(hit)
    return {"total": len(found), "nodes": found}


@plugin.tool(read_only=True,
             schema={"type": "object", "properties": {"key": {"type": "string"}},
                     "required": ["key"], "additionalProperties": False})
def get_comments(key: str = ""):
    _require(key)
    threads: dict = {}
    for c in comments_of(key):
        item = {"id": c.get("id"), "author": (c.get("user") or {}).get("handle", ""),
                "at": c.get("created_at"), "message": c.get("message", "")}
        root = c.get("parent_id")
        if root:
            threads.setdefault(str(root), {"id": str(root), "replies": []})["replies"].append(item)
        else:
            entry = threads.setdefault(str(c.get("id")), {"id": str(c.get("id")), "replies": []})
            entry.update(item)
            entry["order"] = c.get("order_id")
            entry["node"] = (c.get("client_meta") or {}).get("node_id")
            entry["resolved"] = bool(c.get("resolved_at"))
            entry["url"] = comment_url(key, c)
    return {"key": key, "total": len(threads), "threads": list(threads.values())}


@plugin.tool(read_only=True, vision=True,
             schema={"type": "object",
                     "properties": {"key": {"type": "string"}, "node_id": {"type": "string"},
                                    "scale": {"type": "number"}},
                     "required": ["key", "node_id"], "additionalProperties": False})
def render_node(key: str = "", node_id: str = "", scale: float = 1):
    _require(key)
    factor = min(4.0, max(0.01, float(scale or 1)))
    rendered = api("GET", f"/v1/images/{quote(key)}?ids={quote(str(node_id))}&format=png&scale={factor:g}")
    url = (rendered.get("images") or {}).get(str(node_id))
    if not url:
        why = rendered.get('err') or say('узла нет или он пустой', 'there is no such node, or it is empty')
        raise RuntimeError(say(f"Figma не отрисовала узел {node_id}: {why}", f"Figma did not render node {node_id}: {why}"))
    res = plugin.http("GET", url, credential="", headers={}, body="", binary=True)
    if res["status"] >= 400 or not res.get("body_base64"):
        raise RuntimeError(say(f"картинка не скачалась: {res['status']}", f"the picture did not download: {res['status']}"))
    return {"content": [
        {"type": "image", "data": res["body_base64"], "mimeType": "image/png"},
        {"type": "text", "text": say(f"Узел {node_id} файла {key}, масштаб {factor:g}: ",
                                     f"Node {node_id} of file {key}, scale {factor:g}: ")
         + f"{WEB}/design/{key}?node-id={str(node_id).replace(':', '-')}"},
    ]}


@plugin.tool(read_only=False,
             schema={"type": "object",
                     "properties": {"key": {"type": "string"}, "text": {"type": "string"}},
                     "required": ["key", "text"], "additionalProperties": False})
def add_comment(key: str = "", text: str = ""):
    reply(issue=key, text=text)
    return {"ok": True}


if __name__ == "__main__":
    plugin.run()
