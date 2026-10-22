"""Confluence integration — Data Center, Server and Cloud.

ONE integration for both, and that is a finding rather than a shortcut. Jira
needed splitting because Cloud's API had genuinely moved (search answers 410, a
comment is a document); Confluence's has not: the CQL comment feed, the space
key on the container and the storage body were verified live on BOTH a Data
Center (confluence.atlassian.com) and a Cloud site (openmrs.atlassian.net) and
are identical. What differs is how an account is named — Data Center has a login
and a `userKey`, Cloud has only an `accountId` and a display name — and that is
a line of code, not a second integration.

Confluence is not a tracker: its value is the WRITTEN CONTEXT a team keeps —
requirements, decisions, runbooks, the reason a thing is the way it is. So this
integration does two jobs, and the first one matters more:

1. It gives the model reading tools over the marked spaces, so an answer can
   rest on what the team already wrote down instead of on a guess.
2. It takes requests written as comments on a page, the way the tracker
   integrations do, and answers in the same discussion.

Five shapes off LIVE servers decide the rest:

1. `type=comment` in CQL is a feed of comments across a space — one query, in
   `lastmodified` order, with the page in `container`. No walking pages.
2. A comment's browser address arrives ready-made in `_links.webui`, pointing at
   the comment itself (`?focusedCommentId=…`).
3. The body is storage-format XHTML with Confluence's own elements in it, and it
   goes to the model through the SDK's `html_to_text` — a page read raw is
   plumbing, not prose.
4. `/rest/api/search` marks the words it matched with `@@@hl@@@…@@@endhl@@@`.
   Left in, those markers reach the model as noise.
5. On Cloud an author has no login at all: `accountId` plus `displayName` /
   `publicName`. Comparing only what Data Center returns made the bot answer its
   own comments there — caught by a fixture taken from the live Cloud site.
"""

import json
import re
from urllib.parse import quote

from buff_plugin import (
    Event,
    Identity,
    Plugin,
    Scope,
    visible,
    after,
    event_id,
    html_to_text,
    moment,
    parse_command,
    source,
)

plugin = Plugin("confluence")

BASE = plugin.config.get("base_url", "").rstrip("/")
SPACES = plugin.config.get("spaces", [])
ALIAS = plugin.config.get("mention_alias", "")

# The marked spaces ARE the access control — see Scope in the SDK — with one
# difference from a tracker: an EMPTY list is every space the bot's account can
# open (#429). Confluence's own permissions already draw that line.
scope = Scope(SPACES, fold_case=True)


def in_area(space: str) -> bool:
    return not scope.allowed or scope.allows(space)


def require_area(space: str) -> None:
    if not in_area(space):
        scope.require(space)


me = {"username": "", "key": "", "display": ""}

# What the search endpoint wraps around a match.
_HL = re.compile(r"@@@(?:hl|endhl)@@@")


# ---- transport ---------------------------------------------------------

def api(method: str, path: str, body: dict | None = None):
    res = plugin.http(method, BASE + path, credential="api",
                      headers={"Content-Type": "application/json"} if body else {},
                      body=json.dumps(body) if body else "")
    if res["status"] >= 400:
        raise RuntimeError(f"Confluence ответил {res['status']} на {path}: {res['body'][:300]}")
    return json.loads(res["body"]) if res["body"].strip() else {}


def whoami() -> None:
    who = api("GET", "/rest/api/user/current")
    me["username"] = who.get("username", "")
    # Data Center identifies an account by `userKey`, Cloud by `accountId`, and
    # Cloud has no login at all — so the id is the only thing both have.
    me["key"] = who.get("userKey") or who.get("accountId", "")
    me["display"] = who.get("displayName") or who.get("publicName", "")


def identity() -> Identity:
    return Identity(name=me["username"], key=me["key"], display=me["display"], alias=ALIAS)


# ---- reading -----------------------------------------------------------

def page_key(space: str, page_id) -> str:
    """`DOC#51872380` — the space and the page. The thread is the PAGE: a second
    question on the same page continues the same conversation."""
    return f"{space}#{page_id}"


def split_key(key: str) -> tuple[str, str]:
    space, sep, page = str(key).rpartition("#")
    if not sep or not space or not page:
        raise RuntimeError(f"{key} — не адрес страницы Confluence (нужно ПРОСТРАНСТВО#номер)")
    return space, page


def clean(text: str) -> str:
    """Search results carry the highlighting markers of the endpoint that made
    them; nothing downstream wants those."""
    return _HL.sub("", text or "")


def cql(clause: str) -> str:
    spaces = ", ".join(f'"{s}"' for s in SPACES)
    scoped = f"space in ({spaces})" if spaces else "space is not empty"
    return f"{scoped} and {clause}"


def search(query: str, limit: int = 50, expand: str = "") -> list[dict]:
    path = f"/rest/api/content/search?cql={quote(query)}&limit={limit}"
    if expand:
        path += f"&expand={quote(expand)}"
    return (api("GET", path) or {}).get("results", [])


def since_clause(since: str | None) -> str:
    """Confluence compares `lastmodified` to the minute, so the window is
    deliberately wider than the watermark; `after` decides each comment exactly."""
    at = moment(since) if since else None
    if at is None:
        return ""
    return f' and lastmodified >= "{at.strftime("%Y/%m/%d %H:%M")}"'


def comment_event(c: dict, since: str | None) -> Event | None:
    """One comment on a page, turned into a request — or into nothing.

    Every path that produces an event goes through here, which is why the scope
    check lives here rather than in the query (#282).
    """
    container = c.get("container") or {}
    space = ((container.get("space") or {}).get("key") or "").strip()
    if not in_area(space):
        return None
    version = c.get("version") or {}
    author = version.get("by") or {}
    if identity().is_me(author.get("username"), author.get("displayName"),
                        author.get("publicName"), author.get("userKey"),
                        author.get("accountId")):
        return None  # the bot's own replies are not requests
    at = str(version.get("when", ""))
    if not after(at, since):
        return None
    text = html_to_text(((c.get("body") or {}).get("storage") or {}).get("value", ""))
    cmd = parse_command(text, identity())
    if not cmd:
        return None
    key = page_key(space, container.get("id"))
    return Event(
        id=event_id("comment", c.get("id"), at), at=at, kind="comment",
        title=str(container.get("title", "")),
        body=cmd.instruction,
        author=author.get("displayName") or author.get("publicName")
               or author.get("username", ""),
        # Who asked, for the connection's «who may ask» rules (#476). Data Center
        # names a login and exposes no e-mail in its user object at all; Cloud has
        # no login (an account id is not one) and fills `email` only when the
        # person's profile lets it be seen.
        author_login=str(author.get("username") or ""),
        author_email=str(author.get("email") or ""),
        url=BASE + str((c.get("_links") or {}).get("webui", "")),
        action=cmd.action,
        # Confluence holds no code, so a request from it carries no repository
        # and no branch: the answer is computed on the project the connection
        # points at.
        extra=source(key, trigger="mention", context="issue"),
    )


def page_text(page: dict) -> str:
    return html_to_text(((page.get("body") or {}).get("storage") or {}).get("value", ""))


def comments_of(page_id: str) -> list[dict]:
    path = (f"/rest/api/content/{page_id}/child/comment"
            f"?expand=body.storage,version&limit=100")
    return (api("GET", path) or {}).get("results", [])


# ---- writing -----------------------------------------------------------

def as_storage(text: str) -> str:
    """A comment, as Confluence stores it: paragraphs of escaped text."""
    out = []
    for line in str(text).split("\n"):
        safe = line.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")
        out.append(f"<p>{safe}</p>" if safe.strip() else "<p />")
    return "".join(out)


def comment_body(text: str, link: dict | None, attachment_note: str) -> str:
    parts = [text.strip()] if text and text.strip() else []
    if link and link.get("url"):
        parts.append(f"{link.get('title') or 'Открыть в Buff'}: {link['url']}")
    if attachment_note:
        parts.append(attachment_note)
    return "\n\n".join(parts)


def upload(page_id: str, attachment: dict) -> None:
    """Attachments need a multipart body and Confluence's anti-CSRF header."""
    import base64
    import os as _os

    name = attachment.get("filename", "report.pdf")
    content = base64.b64decode(attachment.get("base64", ""))
    boundary = "----buff" + base64.b16encode(_os.urandom(8)).decode()
    body = (
        f"--{boundary}\r\n"
        f'Content-Disposition: form-data; name="file"; filename="{name}"\r\n'
        "Content-Type: application/octet-stream\r\n\r\n"
    ).encode() + content + f"\r\n--{boundary}--\r\n".encode()
    res = plugin.http("POST", f"{BASE}/rest/api/content/{page_id}/child/attachment",
                      credential="api",
                      headers={"X-Atlassian-Token": "no-check",
                               "Content-Type": f"multipart/form-data; boundary={boundary}"},
                      body=body.decode("latin-1"))
    if res["status"] >= 400:
        raise RuntimeError(f"вложение не принято: {res['status']} {res['body'][:200]}")


# ---- plugin surface ----------------------------------------------------

def edition(system: dict, manifest_xml: str) -> str:
    """Which Confluence this is, in the operator's terms.

    Told by what each edition actually answers, checked on a live server of
    both kinds: Cloud answers `/rest/api/settings/systemInfo` with a cloudId and
    an edition, and reports a synthetic version (`1000.0.0-<hash>`) that means
    nothing to anybody; Data Center answers 404 there and names a real version
    in its application-links manifest.
    """
    if system.get("cloudId"):
        kind = str(system.get("edition") or "").strip()
        return f"Confluence Cloud ({kind})" if kind else "Confluence Cloud"
    found = re.search(r"<version>([^<]+)</version>", manifest_xml or "")
    return f"Confluence {found.group(1)}" if found else "Confluence"


@plugin.check
def check():
    whoami()
    system: dict = {}
    try:
        system = api("GET", "/rest/api/settings/systemInfo")
    except RuntimeError:
        pass  # Data Center has no such endpoint, and that ABSENCE is the answer
    manifest_xml = ""
    try:
        # The version lives in the application-links manifest, which is XML —
        # the one place a Data Center reports it without administrator rights.
        res = plugin.http("GET", f"{BASE}/rest/applinks/1.0/manifest", credential="api",
                          headers={}, body="")
        manifest_xml = res.get("body", "")
    except RuntimeError:
        pass
    where = edition(system, manifest_xml)
    seen = 0
    for space in SPACES:
        try:
            api("GET", f"/rest/api/space/{quote(space)}")
            seen += 1
        except RuntimeError:
            pass
    area = (visible(seen, len(scope.allowed), one="указанное пространство", many="указанные пространства",
                    many_of="указанных пространств", fix="Права боту выдаются в самом Confluence.")
            if scope.allowed else "Бот видит все пространства, открытые учётной записи.")
    return {"ok": True, "detail": f"{where}, вошли как {identity().signed_in()}. {area}"}


@plugin.poll
def poll(since):
    if not me["username"]:
        whoami()
    query = cql("type=comment" + since_clause(since)) + " order by lastmodified asc"
    events: list[Event] = []
    for c in search(query, 50, "body.storage,version,container.space"):
        e = comment_event(c, since)
        if e:
            events.append(e)
    return events


@plugin.reply
def reply(issue: str = "", text: str = "", link: dict | None = None,
          attachment: dict | None = None, **_):
    space, page_id = split_key(issue)
    # The marked spaces bound writes too, not only reads.
    require_area(space)
    note = ""
    if attachment:
        upload(page_id, attachment)
        note = f"Документ приложен к странице: {attachment.get('filename', 'report.pdf')}"
    body = comment_body(text, link, note)
    if not body:
        return {"ok": True}
    api("POST", "/rest/api/content", {
        "type": "comment",
        "container": {"id": str(page_id), "type": "page"},
        "body": {"storage": {"value": as_storage(body), "representation": "storage"}},
    })
    return {"ok": True}


@plugin.tool(description="Найти страницы в подключённых пространствах по тексту. Использовать "
                         "ПЕРВЫМ делом, когда вопрос касается требований, регламентов, "
                         "архитектурных решений или того, «как у нас принято».",
             read_only=True,
             schema={"type": "object",
                     "properties": {"query": {"type": "string"}, "limit": {"type": "integer"}},
                     "required": ["query"], "additionalProperties": False})
def search_pages(query: str = "", limit: int = 10):
    text = (query or "").replace('"', " ").strip()
    path = ("/rest/api/search?limit=" + str(limit) + "&cql="
            + quote(cql(f'type=page and text ~ "{text}"')))
    found = (api("GET", path) or {}).get("results", [])
    pages = []
    for r in found:
        content = r.get("content") or {}
        space = ((r.get("resultGlobalContainer") or {}).get("title") or "")
        pages.append({
            "key": page_key(_space_of(r), content.get("id")),
            "title": clean(r.get("title", "")),
            "space": space,
            "excerpt": clean(r.get("excerpt", "")),
            "url": BASE + str(r.get("url", "")),
            "updated": r.get("lastModified"),
        })
    return {"total": len(pages), "pages": pages}


def _space_of(result: dict) -> str:
    """The space key of a search result. The search endpoint names the space by
    its TITLE, not its key, so the key comes from the address."""
    url = str(result.get("url") or "")
    found = re.search(r"/spaces/([^/]+)/", url)
    return found.group(1) if found else ""


@plugin.tool(description="Прочитать страницу целиком, вместе с обсуждением. Использовать, когда "
                         "поиск нашёл подходящую страницу или когда в вопросе назвали её адрес.",
             read_only=True,
             schema={"type": "object", "properties": {"key": {"type": "string"}},
                     "required": ["key"], "additionalProperties": False})
def get_page(key: str = ""):
    space, page_id = split_key(key)
    require_area(space)
    page = api("GET", f"/rest/api/content/{page_id}"
                      "?expand=body.storage,version,space,ancestors")
    return {
        "key": key,
        "title": page.get("title"),
        "space": ((page.get("space") or {}).get("key")),
        "path": [a.get("title") for a in page.get("ancestors", [])],
        "updated": ((page.get("version") or {}).get("when")),
        "updated_by": (((page.get("version") or {}).get("by") or {}).get("displayName")),
        "url": BASE + str((page.get("_links") or {}).get("webui", "")),
        "text": page_text(page),
        "comments": [{"author": (((c.get("version") or {}).get("by") or {}).get("displayName")),
                      "created": ((c.get("version") or {}).get("when")),
                      "text": html_to_text(((c.get("body") or {}).get("storage") or {}).get("value", ""))}
                     for c in comments_of(page_id)],
    }


@plugin.tool(description="Перечислить дочерние страницы раздела. Использовать, когда ответ "
                         "собирается из нескольких страниц одного раздела.",
             read_only=True,
             schema={"type": "object", "properties": {"key": {"type": "string"}},
                     "required": ["key"], "additionalProperties": False})
def get_page_children(key: str = ""):
    space, page_id = split_key(key)
    require_area(space)
    found = (api("GET", f"/rest/api/content/{page_id}/child/page?limit=100&expand=version")
             or {}).get("results", [])
    return {"total": len(found),
            "pages": [{"key": page_key(space, p.get("id")), "title": p.get("title"),
                       "url": BASE + str((p.get("_links") or {}).get("webui", ""))}
                      for p in found]}


@plugin.tool(description="Написать комментарий на странице.",
             read_only=False,
             schema={"type": "object",
                     "properties": {"key": {"type": "string"}, "text": {"type": "string"}},
                     "required": ["key", "text"], "additionalProperties": False})
def add_comment(key: str = "", text: str = ""):
    reply(issue=key, text=text)
    return {"ok": True}


if __name__ == "__main__":
    plugin.run()
