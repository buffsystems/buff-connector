"""Jira Cloud integration.

Separate from the Jira Data Center plugin because the API is separate, not just
the address. Three differences decide this file, and all three were read off a
live Cloud instance rather than off the documentation:

1. A comment's body is an ADF DOCUMENT — a tree of nodes — not wiki markup. It
   has to be walked to get the text out, and written back as a tree.
2. `POST /rest/api/3/search` is GONE on Cloud: it answers 410. Searching is
   `GET /rest/api/3/search/jql`, which pages with a `nextPageToken` instead of a
   start index.
3. A person is an `accountId` and a `displayName`. There is no login anywhere,
   so the bot recognises itself by its account id and its display name, and a
   mention is an ADF node carrying that id rather than text.
"""

from __future__ import annotations

import json
from urllib.parse import quote

from buff_plugin import (
    plural,
    plural_en,
    say,
    Event,
    Identity,
    Plugin,
    Scope,
    after,
    event_id,
    parse_command,
    source,
)

plugin = Plugin("jira-cloud")

BASE = plugin.config.get("base_url", "").rstrip("/")
PROJECTS = plugin.config.get("project_keys", [])
ALIAS = plugin.config.get("mention_alias", "")

# The marked projects ARE the access control — see Scope in the SDK.
scope = Scope(PROJECTS)

# Work is taken on a DIRECT ADDRESS. Assignment is addressed at the bot too, but
# it also happens by routing rules, so it is off unless the operator says their
# assignments are deliberate (#282).
ACCEPT_ASSIGNMENT = bool(plugin.config.get("accept_assignment"))

me = {"account_id": "", "display": ""}


# ---- transport ---------------------------------------------------------

def api(method: str, path: str, body: dict | None = None):
    res = plugin.http(method, BASE + path, credential="api",
                      headers={"Content-Type": "application/json",
                               "Accept": "application/json"} if body else
                              {"Accept": "application/json"},
                      body=json.dumps(body) if body else "")
    if res["status"] >= 400:
        raise RuntimeError(say(f"Jira ответила {res['status']} на {path}: {res['body'][:300]}",
                               f"Jira answered {res['status']} to {path}: {res['body'][:300]}"))
    return json.loads(res["body"]) if res["body"].strip() else {}


def whoami() -> None:
    who = api("GET", "/rest/api/3/myself")
    me["account_id"] = who.get("accountId", "")
    me["display"] = who.get("displayName", "")


def identity() -> Identity:
    # A mention typed by hand reads `@Имя`; one inserted by the editor is a node,
    # handled in `text_of` below, which rewrites it to the same shape.
    return Identity(name=me["display"], display=me["display"], alias=ALIAS)


# ---- reading -----------------------------------------------------------

def text_of(doc) -> str:
    """The words in an ADF document.

    A comment on Cloud is a tree: paragraphs of text nodes, with mentions as
    nodes of their own. Walking it is the only way to read what a person wrote —
    and a mention has to come back as `@Имя`, because that is what the command
    parser looks for.
    """
    if isinstance(doc, str):
        return doc
    out: list[str] = []

    def walk(node) -> None:
        if isinstance(node, list):
            for n in node:
                walk(n)
            return
        if not isinstance(node, dict):
            return
        kind = node.get("type")
        if kind == "text":
            out.append(str(node.get("text", "")))
        elif kind == "mention":
            attrs = node.get("attrs") or {}
            name = str(attrs.get("text") or "")
            out.append(name if name.startswith("@") else f"@{name}")
        elif kind in ("hardBreak", "paragraph"):
            out.append("\n")
        walk(node.get("content"))

    walk(doc)
    return "".join(out).strip()


def to_adf(text: str) -> dict:
    """A comment, as Cloud takes it: one paragraph per line of the answer."""
    lines = [ln for ln in str(text).split("\n")]
    return {
        "type": "doc", "version": 1,
        "content": [{"type": "paragraph",
                     "content": ([{"type": "text", "text": ln}] if ln else [])}
                    for ln in lines],
    }


def mentions_me(comment: dict) -> bool:
    """True when the bot is mentioned as a NODE — the editor's own way of
    addressing somebody, which carries an account id and no text the parser
    would recognise on its own."""
    found = False

    def walk(node) -> None:
        nonlocal found
        if isinstance(node, list):
            for n in node:
                walk(n)
            return
        if not isinstance(node, dict):
            return
        if node.get("type") == "mention":
            if str((node.get("attrs") or {}).get("id", "")) == me["account_id"]:
                found = True
        walk(node.get("content"))

    walk(comment.get("body"))
    return found


def project_of(key: str) -> str:
    return str(key).split("-", 1)[0]


def comment_visibility(comment: dict) -> str:
    """How private a question was (cloud-front#476); the answer is posted the same way.

    A comment restricted to a role is `role:<name>`; to a group, `group-id:<id>`
    — Cloud names a group by its id, the name being optional — or `group:<name>`
    where only the name came. Jira Service Management marks a comment the
    customer does not see with `jsdPublic: false`: that is `internal`."""
    v = comment.get("visibility") or {}
    kind = str(v.get("type") or "")
    if kind == "role" and (v.get("value") or v.get("identifier")):
        return f"role:{v.get('value') or v.get('identifier')}"
    if kind == "group" and v.get("identifier"):
        return f"group-id:{v['identifier']}"
    if kind == "group" and v.get("value"):
        return f"group:{v['value']}"
    return "internal" if comment.get("jsdPublic") is False else ""


def visibility_fields(visibility: str) -> dict:
    """The comment fields that restrict an answer the way comment_visibility read
    the question: Jira's `visibility` object, or the Service Management property
    that keeps a comment from the customer."""
    kind, _, value = str(visibility or "").partition(":")
    if kind == "role" and value:
        return {"visibility": {"type": "role", "value": value}}
    if kind == "group-id" and value:
        return {"visibility": {"type": "group", "identifier": value}}
    if kind == "group" and value:
        return {"visibility": {"type": "group", "value": value}}
    if kind == "internal":
        return {"properties": [{"key": "sd.public.comment", "value": {"internal": True}}]}
    return {}


def event_from_comment(issue: dict, comment: dict, since: str | None) -> Event | None:
    """One comment on an issue, turned into a request — or into nothing.

    Every path that produces an event goes through here, which is why the scope
    check lives here rather than in the query (#282).
    """
    key = str(issue.get("key", ""))
    if not scope.allows(project_of(key)):
        return None
    author = comment.get("author") or {}
    if author.get("accountId") == me["account_id"]:
        return None  # the bot's own replies are not requests
    if identity().is_me(author.get("displayName")):
        return None
    at = str(comment.get("created", ""))
    if not after(at, since):
        return None
    text = text_of(comment.get("body"))
    cmd = parse_command(text, identity())
    if not cmd and mentions_me(comment):
        # Addressed by a mention NODE with nothing the parser recognises around
        # it: still a request, and the default action is to answer the question.
        stripped = text.replace(f"@{me['display']}", "").strip()
        if stripped:
            from buff_plugin import Command

            cmd = Command("ask", stripped)
    if not cmd:
        return None
    return Event(
        id=event_id("comment", comment.get("id"), at), at=at, kind="comment",
        title=(issue.get("fields") or {}).get("summary", ""), body=cmd.instruction,
        author=author.get("displayName", ""), url=f"{BASE}/browse/{key}",
        action=cmd.action, extra=source(key, trigger="mention"),
        # Cloud has no login, only an account id — which no person would write
        # into a list of who may ask. The address is there unless its owner hid it.
        author_email=str(author.get("emailAddress") or ""),
        visibility=comment_visibility(comment),
    )


def search_jql(jql: str, fields: str = "summary,updated", limit: int = 50) -> list[dict]:
    """Cloud's search. `POST /search` is GONE — it answers 410 — and the one
    that works pages with a token rather than a start index."""
    path = (f"/rest/api/3/search/jql?jql={quote(jql)}&maxResults={limit}"
            f"&fields={quote(fields)}")
    return (api("GET", path) or {}).get("issues", [])


# ---- plugin surface ----------------------------------------------------

@plugin.check
def check():
    info = api("GET", "/rest/api/3/serverInfo")
    whoami()
    n = len(scope.allowed)
    who = me['display'] or '?'
    return {"ok": True, "detail": say(
        f"Jira Cloud, вошли как {who}. Бот работает в {n} {plural(n, 'проекте', 'проектах', 'проектах')}.",
        f"Jira Cloud, signed in as {who}. The bot works in {n} {plural_en(n, 'project', 'projects')}.")}


@plugin.poll
def poll(since):
    if not me["account_id"]:
        whoami()
    events: list[Event] = []
    projects = ", ".join(f'"{p}"' for p in PROJECTS)
    clause = f"project in ({projects})" if projects else "project is not EMPTY"
    if since:
        # Jira compares to the minute, so the window is deliberately wider than
        # the watermark; the rest is filtered by moment below.
        from buff_plugin import moment
        from datetime import timedelta

        at = moment(since)
        if at:
            clause += f' AND updated >= "{(at - timedelta(minutes=2)).strftime("%Y-%m-%d %H:%M")}"'
    for issue in search_jql(clause + " ORDER BY updated ASC"):
        key = issue.get("key", "")
        comments = (api("GET", f"/rest/api/3/issue/{key}/comment?maxResults=50&orderBy=-created")
                    or {}).get("comments", [])
        for c in comments:
            e = event_from_comment(issue, c, since)
            if e:
                events.append(e)
    return events


@plugin.webhook
def webhook(headers, body):
    """Jira's callback carries the issue and the comment, so a request becomes an
    event without going back for them. Anything else produces nothing, and the
    poll picks up whatever a callback missed."""
    try:
        payload = json.loads(body or "{}")
    except ValueError:
        return []
    issue = payload.get("issue") or {}
    comment = payload.get("comment")
    if not issue.get("key") or not comment:
        return []
    if not me["account_id"]:
        whoami()
    e = event_from_comment(issue, comment, None)
    return [e] if e else []


@plugin.reply
def reply(issue: str = "", text: str = "", link: dict | None = None,
          attachment: dict | None = None, visibility: str = "", **_):
    # The marked projects bound writes too, not only reads.
    scope.require(project_of(issue))
    restricted = visibility_fields(visibility)
    if restricted:
        restricted_reply(issue, text, link, attachment, restricted)
        return {"ok": True}
    if text:
        api("POST", f"/rest/api/3/issue/{issue}/comment", {"body": to_adf(text)})
    if link and link.get("url"):
        api("POST", f"/rest/api/3/issue/{issue}/remotelink",
            {"object": {"url": link.get("url", ""), "title": link.get("title", "")}})
    if attachment:
        upload(issue, attachment)
    return {"ok": True}


def restricted_reply(issue: str, text: str, link: dict | None, attachment: dict | None,
                     restricted: dict) -> None:
    """The answer to a restricted question, restricted the same way.

    Only a comment can be restricted: a remote link and an attachment are seen
    by everyone who sees the ticket. So the link goes into the comment, and the
    document stays in Buff behind that link rather than on the ticket."""
    body = to_adf(text.strip()) if text and text.strip() else {"type": "doc", "version": 1, "content": []}
    if link and link.get("url"):
        body["content"].append({"type": "paragraph", "content": [{
            "type": "text", "text": link.get("title") or say("Открыть в Buff", "Open in Buff"),
            "marks": [{"type": "link", "attrs": {"href": link["url"]}}]}]})
    if attachment:
        body["content"].append({"type": "paragraph", "content": [{"type": "text", "text": say(
            "Документ — в Buff по ссылке: вложения в Jira видны всем, кто видит задачу, "
            "а вопрос был с ограниченным доступом.",
            "The document is in Buff, by the link: attachments in Jira are seen by everyone who sees "
            "the issue, and the question had restricted access.")}]})
    if body["content"]:
        api("POST", f"/rest/api/3/issue/{issue}/comment", {"body": body, **restricted})


def upload(issue: str, attachment: dict) -> None:
    """Attachments do not go through the JSON API: they need a multipart body
    and Jira's own anti-CSRF header."""
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
    res = plugin.http("POST", f"{BASE}/rest/api/3/issue/{issue}/attachments",
                      credential="api",
                      headers={"X-Atlassian-Token": "no-check",
                               "Content-Type": f"multipart/form-data; boundary={boundary}"},
                      body=body.decode("latin-1"))
    if res["status"] >= 400:
        raise RuntimeError(say(f"вложение не принято: {res['status']} {res['body'][:200]}",
                               f"the attachment was not accepted: {res['status']} {res['body'][:200]}"))


@plugin.tool(read_only=True,
             schema={"type": "object",
                     "properties": {"jql": {"type": "string"}, "limit": {"type": "integer"}},
                     "required": ["jql"], "additionalProperties": False})
def search_issues(jql: str = "", limit: int = 20):
    issues = search_jql(jql, fields="summary,status", limit=limit)
    return {"total": len(issues), "issues": [{
        "key": i.get("key"),
        "summary": (i.get("fields") or {}).get("summary"),
        "status": ((i.get("fields") or {}).get("status") or {}).get("name"),
    } for i in issues]}


@plugin.tool(read_only=True,
             schema={"type": "object", "properties": {"key": {"type": "string"}},
                     "required": ["key"], "additionalProperties": False})
def get_issue(key: str = ""):
    scope.require(project_of(key))
    data = api("GET", f"/rest/api/3/issue/{key}?fields=summary,description,status,comment")
    fields = data.get("fields", {})
    return {
        "key": data.get("key"),
        "summary": fields.get("summary"),
        # Description is an ADF document on Cloud, like a comment.
        "description": text_of(fields.get("description")),
        "status": (fields.get("status") or {}).get("name"),
        "comments": [{
            "author": ((c.get("author") or {}).get("displayName")),
            "created": c.get("created"),
            "body": text_of(c.get("body")),
        } for c in (fields.get("comment") or {}).get("comments", [])],
    }


@plugin.tool(read_only=False,
             schema={"type": "object",
                     "properties": {"key": {"type": "string"}, "text": {"type": "string"}},
                     "required": ["key", "text"], "additionalProperties": False})
def add_comment(key: str = "", text: str = ""):
    scope.require(project_of(key))
    api("POST", f"/rest/api/3/issue/{key}/comment", {"body": to_adf(text)})
    return {"ok": True}


if __name__ == "__main__":
    plugin.run()
