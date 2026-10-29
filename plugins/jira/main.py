"""Jira Data Center and Server integration.

This is the connector's Jira support, moved out of the connector itself. Three
things in here exist because a live run against a real Jira contradicted what
the unit tests "proved", and they are the reason this file is not a
straightforward REST wrapper:

1. A real mention is Jira's own wiki markup, `[~account]` — that is what the
   autocomplete inserts and what renders as a chip. Plain `@name` works too, but
   only as text, so both forms are recognised.
2. Labels and assignment are read from the CHANGELOG, not from the issue's
   current fields. Reading current fields means switching the integration on
   fires it at every ticket that already carries the label.
3. Jira's changelog does NOT contain a login. `to` is an internal key
   (JIRAUSER10100) and `toString` is a display name; only a comment's author
   carries the login. So the bot recognises itself by any of the three.

The event id carries the moment as well as the tracker's id, because a
recreated Jira numbers comments from one again and the platform's de-duplication
would otherwise swallow real events as repeats.
"""

from __future__ import annotations

import base64
import json
import os
import re
from datetime import datetime, timedelta, timezone

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
    moment,
    parse_command as read_command,
    source,
)

plugin = Plugin("jira")

BASE = plugin.config.get("base_url", "").rstrip("/")
PROJECTS = plugin.config.get("project_keys", [])
ALIAS = plugin.config.get("mention_alias", "")

# The account the bot is logged in as, learned at startup.
me = {"name": "", "key": "", "display": ""}
scheme = {"kind": "bearer"}
# How a check names what it reached and how it signed in, in words.
EDITIONS = {"Server": "Server", "DataCenter": "Data Center"}
SIGN_IN = {"bearer": "по личному токену", "basic": "по логину и паролю"}
SIGN_IN_EN = {"bearer": "with a personal token", "basic": "with a login and password"}

# The marked projects ARE the access control — see Scope in the SDK for why the
# check lives in the function that produces events and binds writes as well.
scope = Scope(PROJECTS)

# Work is taken on a DIRECT ADDRESS and nothing else.
#
# A label used to start a run: add `buff-research` and the bot went to work. It
# was removed because a label is addressed at a PROCESS, not at anyone — triage
# rules, bulk JQL edits and the tracker's own automation all add labels, and each
# one became work somebody was billed for and nobody ordered.
#
# Assignment is different: choosing the bot's account is addressing it. But it
# also happens by routing rules, so it is off unless the operator says their
# assignments are made by a person on purpose.
ACCEPT_ASSIGNMENT = bool(plugin.config.get("accept_assignment"))


# ---- transport ---------------------------------------------------------

def api(method: str, path: str, body: dict | None = None) -> dict:
    res = plugin.http(method, BASE + path, credential="api",
                      headers={"Content-Type": "application/json"} if body else {},
                      body=json.dumps(body) if body else "")
    if res["status"] >= 400:
        raise RuntimeError(say(f"Jira ответила {res['status']} на {path}: {res['body'][:300]}",
                               f"Jira answered {res['status']} to {path}: {res['body'][:300]}"))
    return json.loads(res["body"]) if res["body"] else {}


def parse_version(s: str) -> tuple:
    nums = re.findall(r"\d+", s or "")
    return tuple(int(n) for n in nums[:3]) or (0,)


def detect_scheme() -> None:
    """Jira ≥ 8.14 takes a personal access token as a bearer; older Server only
    understands basic auth. Asking serverInfo is how we find out which."""
    info = api("GET", "/rest/api/2/serverInfo")
    version = parse_version(info.get("version", ""))
    scheme["kind"] = "bearer" if version >= (8, 14) else "basic"
    scheme["version"] = info.get("version", "")
    scheme["deployment"] = info.get("deploymentType", "")


def whoami() -> None:
    who = api("GET", "/rest/api/2/myself")
    me["name"] = who.get("name", "")
    me["key"] = who.get("key", "")
    me["display"] = who.get("displayName", "")


# ---- reading -----------------------------------------------------------

def identity() -> Identity:
    """Jira spells the same account three ways and the bot answers to all of
    them: `[~login]` is what the autocomplete inserts, `[~KEY]` is what the
    changelog carries, and a plain `@name` is what people type by hand."""
    return Identity(
        name=me["name"], key=me["key"], display=me["display"], alias=ALIAS,
        mentions=tuple(f"[~{n}]" for n in (me["name"], me["key"]) if n),
    )


def parse_command(text: str) -> tuple[str, str] | None:
    cmd = read_command(text, identity())
    return (cmd.action, cmd.instruction) if cmd else None


def is_me(*values: str) -> bool:
    return identity().is_me(*values)


def jql(since: str | None) -> str:
    projects = ", ".join(f'"{p}"' for p in PROJECTS)
    clause = f"project in ({projects})" if projects else "project is not EMPTY"
    if since:
        # Jira compares to the minute, so the window is deliberately wider than
        # the watermark; duplicates are removed upstream by event id.
        window = (moment(since) or datetime.now(timezone.utc)) - timedelta(minutes=2)
        clause += f' AND updated >= "{window.strftime("%Y-%m-%d %H:%M")}"'
    return clause + " ORDER BY updated ASC"


def search(since: str | None) -> list[dict]:
    body = {
        "jql": jql(since),
        "maxResults": 50,
        "fields": ["summary", "updated", "labels", "assignee", "comment"],
        "expand": ["changelog"],
    }
    return api("POST", "/rest/api/2/search", body).get("issues", [])


def project_of(key: str) -> str:
    return key.split("-", 1)[0]


def comment_visibility(comment: dict) -> str:
    """A comment restricted to a role or a group is a question only those people
    see (cloud-front#476), and the answer is restricted the same way. Kept as
    `role:<name>` / `group:<name>` so the reply can rebuild Jira's object."""
    v = comment.get("visibility") or {}
    kind, value = str(v.get("type") or ""), str(v.get("value") or "")
    return f"{kind}:{value}" if kind in ("role", "group") and value else ""


def restriction(visibility: str) -> dict | None:
    """Jira's `visibility` object back from comment_visibility's spelling."""
    kind, _, value = str(visibility or "").partition(":")
    return {"type": kind, "value": value} if kind in ("role", "group") and value else None


def events_from(issue: dict, since: str | None) -> list[Event]:
    out: list[Event] = []
    key = issue.get("key", "")
    if not scope.allows(project_of(key)):
        return out
    fields = issue.get("fields", {})
    summary = fields.get("summary", "")

    for c in (fields.get("comment", {}) or {}).get("comments", []):
        author = (c.get("author") or {})
        if is_me(author.get("name", ""), author.get("displayName", "")):
            continue  # the bot's own replies are not requests
        parsed = parse_command(c.get("body", "") or "")
        if not parsed:
            continue
        action, instruction = parsed
        at = c.get("created", "")
        # Without this a comment is re-reported on every poll for ever: the
        # search window is by ticket, and a ticket keeps matching long after the
        # comment in it was answered.
        if not after(at, since):
            continue
        out.append(Event(
            id=event_id("comment", c.get("id"), at), at=at, kind="comment", title=summary,
            body=instruction, author=author.get("displayName") or author.get("name", ""),
            url=f"{BASE}/browse/{key}", action=action,
            extra=source(key, trigger="mention"),
            author_login=str(author.get("name") or ""),
            author_email=str(author.get("emailAddress") or ""),
            visibility=comment_visibility(c),
        ))

    for history in (issue.get("changelog", {}) or {}).get("histories", []):
        at = history.get("created", "")
        if not after(at, since):
            continue
        author = (history.get("author") or {})
        for item in history.get("items", []):
            field = (item.get("field") or "").lower()
            if field != "assignee" or not ACCEPT_ASSIGNMENT:
                continue
            # No login here — only an internal key and a display name, so the
            # bot has to recognise all three of its own names.
            if is_me(item.get("to") or "", item.get("toString") or ""):
                out.append(Event(
                    id=event_id("assign", key, at), at=at, kind="assign", title=summary,
                    body=summary, author=author.get("displayName", ""),
                    url=f"{BASE}/browse/{key}", action="task",
                    extra=source(key, trigger="assign"),
                    author_login=str(author.get("name") or ""),
                    author_email=str(author.get("emailAddress") or ""),
                ))
    return out


# ---- plugin surface ----------------------------------------------------

@plugin.check
def check():
    detect_scheme()
    whoami()
    reached = " ".join(p for p in ("Jira", EDITIONS.get(scheme.get("deployment", ""), ""), scheme.get("version", "")) if p)
    n = len(scope.allowed)
    who = me['display'] or me['name']
    return {
        "ok": True,
        "detail": say(
            f"{reached}, вошли как {who} {SIGN_IN[scheme['kind']]}. "
            f"Бот работает в {n} {plural(n, 'проекте', 'проектах', 'проектах')}.",
            f"{reached}, signed in as {who} {SIGN_IN_EN[scheme['kind']]}. "
            f"The bot works in {n} {plural_en(n, 'project', 'projects')}."),
    }


@plugin.poll
def poll(since):
    if not me["name"]:
        detect_scheme()
        whoami()
    events: list[Event] = []
    for issue in search(since):
        events.extend(events_from(issue, since))
    return events


@plugin.webhook
def webhook(headers, body):
    """Jira's callback carries the issue, so the request can be turned into an
    event without going back to the API at all.

    Falls back to nothing when the payload is not one we recognise: the poll
    runs regardless and will pick the request up on its next pass. A callback is
    an accelerator, and an accelerator that guesses is worse than one that waits.
    """
    try:
        payload = json.loads(body or "{}")
    except ValueError:
        return []
    issue = payload.get("issue") or {}
    if not issue.get("key"):
        return []
    if not me["name"]:
        detect_scheme()
        whoami()

    comment = payload.get("comment")
    if comment:
        # Shape it like a search result so ONE parser produces every event: the
        # alternative is a second set of edge cases that drifts from the poller.
        issue = {**issue, "fields": {**issue.get("fields", {}),
                                     "comment": {"comments": [comment]}}}
    return events_from(issue, None)


@plugin.reply
def reply(issue: str = "", text: str = "", link: dict | None = None,
          attachment: dict | None = None, visibility: str = "", **_):
    # The marked projects bound writes too, not only reads. The ticket comes
    # from the platform, which derived it from our own event — but the connector
    # is the customer's trust boundary, and a boundary that only checks the
    # direction it expects trouble from is not one.
    scope.require(project_of(issue))
    restricted = restriction(visibility)
    if restricted:
        restricted_reply(issue, text, link, attachment, restricted)
        return {"ok": True}
    if text:
        api("POST", f"/rest/api/2/issue/{issue}/comment", {"body": text})
    if link:
        api("POST", f"/rest/api/2/issue/{issue}/remotelink", {
            "object": {"url": link.get("url", ""), "title": link.get("title", "")},
        })
    if attachment:
        # Attachments do not go through the JSON API: they need a multipart body
        # and Jira's own anti-CSRF header.
        boundary = "----buff" + base64.b16encode(os.urandom(8)).decode()
        name = attachment.get("filename", "report.pdf")
        content = base64.b64decode(attachment.get("base64", ""))
        body = (
            f"--{boundary}\r\n"
            f'Content-Disposition: form-data; name="file"; filename="{name}"\r\n'
            "Content-Type: application/octet-stream\r\n\r\n"
        ).encode() + content + f"\r\n--{boundary}--\r\n".encode()
        res = plugin.http("POST", f"{BASE}/rest/api/2/issue/{issue}/attachments",
                          credential="api",
                          headers={"X-Atlassian-Token": "no-check",
                                   "Content-Type": f"multipart/form-data; boundary={boundary}"},
                          body=body.decode("latin-1"))
        if res["status"] >= 400:
            raise RuntimeError(say(f"вложение не принято: {res['status']} {res['body'][:200]}",
                               f"the attachment was not accepted: {res['status']} {res['body'][:200]}"))
    return {"ok": True}


def restricted_reply(issue: str, text: str, link: dict | None, attachment: dict | None,
                     restricted: dict) -> None:
    """The answer to a restricted question, restricted the same way.

    Only a comment can be restricted: a remote link and an attachment are seen
    by everyone who sees the ticket. So the link goes into the comment, and the
    document stays in Buff behind that link rather than on the ticket."""
    parts = [text.strip()] if text and text.strip() else []
    if link and link.get("url"):
        parts.append(f"[{link.get('title') or say('Открыть в Buff', 'Open in Buff')}|{link['url']}]")
    if attachment:
        parts.append(say("Документ — в Buff по ссылке: вложения в Jira видны всем, кто видит задачу, "
                         "а вопрос был с ограниченным доступом.",
                         "The document is in Buff, by the link: attachments in Jira are seen by everyone who "
                         "sees the issue, and the question had restricted access."))
    if parts:
        api("POST", f"/rest/api/2/issue/{issue}/comment",
            {"body": "\n\n".join(parts), "visibility": restricted})


@plugin.tool(read_only=True,
             schema={"type": "object", "properties": {"jql": {"type": "string"},
                                                      "limit": {"type": "integer"}},
                     "required": ["jql"], "additionalProperties": False})
def search_issues(jql: str = "", limit: int = 20):
    body = {"jql": jql, "maxResults": limit, "fields": ["summary", "status", "assignee", "updated"]}
    data = api("POST", "/rest/api/2/search", body)
    return {"total": data.get("total", 0), "issues": [{
        "key": i.get("key"),
        "summary": (i.get("fields") or {}).get("summary"),
        "status": ((i.get("fields") or {}).get("status") or {}).get("name"),
    } for i in data.get("issues", [])]}


@plugin.tool(read_only=True,
             schema={"type": "object", "properties": {"key": {"type": "string"}},
                     "required": ["key"], "additionalProperties": False})
def get_issue(key: str = ""):
    data = api("GET", f"/rest/api/2/issue/{key}?fields=summary,description,status,comment")
    fields = data.get("fields", {})
    return {
        "key": data.get("key"),
        "summary": fields.get("summary"),
        "description": fields.get("description"),
        "status": (fields.get("status") or {}).get("name"),
        "comments": [{
            "author": ((c.get("author") or {}).get("displayName")),
            "created": c.get("created"),
            "body": c.get("body"),
        } for c in (fields.get("comment") or {}).get("comments", [])],
    }


@plugin.tool(read_only=False,
             schema={"type": "object", "properties": {"key": {"type": "string"},
                                                      "text": {"type": "string"}},
                     "required": ["key", "text"], "additionalProperties": False})
def add_comment(key: str = "", text: str = ""):
    api("POST", f"/rest/api/2/issue/{key}/comment", {"body": text})
    return {"ok": True}


if __name__ == "__main__":
    plugin.run()
