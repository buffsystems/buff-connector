"""GitLab integration — gitlab.com and self-hosted alike.

One REST API (v4) serves both; what differs is the address. So this is one
plugin, and the operator points it at their own server or at gitlab.com.

The poller is GitLab's EVENTS feed (`/projects/:id/events?action=commented`).
GitLab has no "every note in this project since a moment" endpoint, and walking
issues and merge requests separately would mean a request per thread; the events
feed is one call per project and covers issues, merge requests AND commits at
once — which is exactly the set of places a person can address the bot.

A question asked in a merge request is about the code ON that merge request, and
one asked under a commit is about that commit, so the event carries the
repository and the branch (or the commit itself) and the platform answers there
instead of on the default branch (#287).

One limit of the feed, found by reading a live one: a comment on a COMMIT
arrives with nothing that identifies the commit — no id, no sha, only the commit
message as a title. Those requests are therefore served by the callback, which
does carry the sha; the poller cannot route them and does not guess.
"""

from __future__ import annotations

import json
import re
from datetime import timedelta
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
    moment,
    parse_command,
    source,
)

plugin = Plugin("gitlab")

BASE = plugin.config.get("base_url", "https://gitlab.com").rstrip("/")
PROJECTS = plugin.config.get("projects", [])
ALIAS = plugin.config.get("mention_alias", "")

# The marked projects ARE the access control — see Scope in the SDK.
scope = Scope(PROJECTS, fold_case=True)

# Work is taken on a DIRECT ADDRESS. Assignment is addressed at the bot too, but
# it also happens by routing rules, so it is off unless the operator says their
# assignments are deliberate (#282).
ACCEPT_ASSIGNMENT = bool(plugin.config.get("accept_assignment"))

me = {"username": "", "name": ""}

# What a note can be attached to, and how each is addressed afterwards. GitLab's
# own notation is the plainest thing to reuse: #7 is an issue, !7 a merge
# request, @<sha> a commit.
NOTEABLE = {"Issue": "#", "MergeRequest": "!", "Commit": "@"}


# ---- transport ---------------------------------------------------------

def api(method: str, path: str, body: dict | None = None):
    res = plugin.http(method, BASE + "/api/v4" + path, credential="api",
                      headers={"Content-Type": "application/json"} if body else {},
                      body=json.dumps(body) if body else "")
    if res["status"] >= 400:
        raise RuntimeError(say(f"GitLab ответила {res['status']} на {path}: {res['body'][:300]}",
                               f"GitLab answered {res['status']} to {path}: {res['body'][:300]}"))
    return json.loads(res["body"]) if res["body"] else {}


def enc(project: str) -> str:
    """A project is addressed by its URL-encoded path — `acme%2Fapi`."""
    return quote(str(project), safe="")


def whoami() -> None:
    who = api("GET", "/user")
    me["username"] = who.get("username", "")
    me["name"] = who.get("name") or who.get("username", "")


def identity() -> Identity:
    return Identity(name=me["username"], display=me["name"], alias=ALIAS)


# Addresses already read, by user id: one request per author per run, not per note.
emails: dict = {}


def author_email(author: dict) -> str:
    """The author's address, where GitLab shows one (cloud-front#476).

    The events feed carries none, and the Note Hook's `email` is "[REDACTED]"
    on current GitLab, so it is read off the user once — `public_email` for
    anyone, `email` when the token is an admin's. Unreadable is empty, never a
    guess; a failed read is not remembered, so the next note tries again."""
    given = str(author.get("email") or "")
    if "@" in given:
        return given
    uid = author.get("id")
    if not uid:
        return ""
    if uid not in emails:
        try:
            user = api("GET", f"/users/{uid}")
        except RuntimeError:
            return ""
        emails[uid] = str(user.get("public_email") or user.get("email") or "")
    return emails[uid]


def note_visibility(note: dict) -> str:
    """An internal note is seen by the team only, and so is the answer to it.
    `confidential` is the older name of the same flag."""
    return "internal" if note.get("internal") or note.get("confidential") else ""


# ---- reading -----------------------------------------------------------

def issue_key(project: str, kind: str, ident) -> str:
    return f"{project}{NOTEABLE.get(kind, '#')}{ident}"


def split_key(key: str) -> tuple[str, str, str]:
    """Reads a key back into the project, what it points at, and which one."""
    m = re.fullmatch(r"(?P<project>.+?)(?P<mark>[#!@])(?P<ident>[^#!@]+)", str(key).strip())
    if not m:
        raise RuntimeError(say(f"{key} — не адрес обсуждения в GitLab (нужно группа/проект#номер)",
                               f"{key} is not a GitLab discussion address (group/project#number is needed)"))
    kinds = {v: k for k, v in NOTEABLE.items()}
    return m.group("project"), kinds[m.group("mark")], m.group("ident")


def source_branch(project: str, iid) -> str:
    """The branch a merge request is built from. Best effort: an answer on the
    default branch beats losing the request, and the reply says which branch it
    was computed on either way."""
    try:
        mr = api("GET", f"/projects/{enc(project)}/merge_requests/{iid}")
    except RuntimeError:
        return ""
    return str(mr.get("source_branch", ""))


def event_from(project: str, ev: dict, since: str | None, branch_of=source_branch) -> Event | None:
    """One event from GitLab's feed, turned into a request — or into nothing.

    Every path that produces an event goes through here, which is why the scope
    check lives here rather than in the query (#282).
    """
    if not scope.allows(project):
        return None
    note = ev.get("note") or {}
    kind = str(note.get("noteable_type", ""))
    if not note or kind not in NOTEABLE:
        return None
    if note.get("system"):
        return None  # GitLab's own bookkeeping notes are not requests
    author = ev.get("author") or note.get("author") or {}
    if identity().is_me(author.get("username"), author.get("name")):
        return None  # the bot's own replies are not requests
    at = str(note.get("created_at") or ev.get("created_at", ""))
    if not after(at, since):
        return None
    cmd = parse_command(note.get("body", "") or "", identity())
    if not cmd:
        return None

    # NOT `target_iid`: in the events feed that is the NOTE's own id, not the
    # number of the thing it hangs on.
    ident = note.get("commit_id") if kind == "Commit" else note.get("noteable_iid")
    if not ident:
        # A comment on a COMMIT arrives in the feed with nothing that identifies
        # the commit — no id, no sha, only the commit's message as a title. It
        # cannot be routed, so it is not guessed at: the callback carries the sha
        # and handles this case, which is why the page says commit comments need
        # the receiver switched on.
        return None

    key = issue_key(project, kind, ident)
    if kind == "MergeRequest":
        extra = source(key, trigger="mention", context="pull", repo=project,
                       ref=branch_of(project, ident))
    elif kind == "Commit":
        # The commit IS the ref: an answer about a commit has to be computed on
        # that commit and on nothing else.
        extra = source(key, trigger="mention", context="commit", repo=project, ref=str(ident))
    else:
        extra = source(key, trigger="mention", context="issue", repo=project)

    return Event(
        id=event_id("note", note.get("id"), at), at=at, kind="comment", title=key,
        body=cmd.instruction,
        author=author.get("name") or author.get("username", ""),
        url=note_url(project, kind, ident), action=cmd.action, extra=extra,
        author_login=str(author.get("username") or ""),
        author_email=author_email(author),
        visibility=note_visibility(note),
    )


def note_url(project: str, kind: str, ident) -> str:
    part = {"Issue": "issues", "MergeRequest": "merge_requests", "Commit": "commit"}[kind]
    return f"{BASE}/{project}/-/{part}/{ident}"


def assignment_events(project: str, issue: dict, since: str | None) -> list[Event]:
    """Assignment of an issue to the bot, read from its resource events.

    From the events and not from the issue's current assignee: reading the
    current field means switching the integration on fires it at every issue
    that already names the bot.
    """
    out: list[Event] = []
    if not ACCEPT_ASSIGNMENT or not scope.allows(project):
        return out
    iid = issue.get("iid")
    events = api("GET", f"/projects/{enc(project)}/issues/{iid}/resource_state_events") or []
    for e in events + (issue.get("assignment_events") or []):
        assignee = e.get("assignee") or {}
        if not identity().is_me(assignee.get("username")):
            continue
        at = str(e.get("created_at", ""))
        if not after(at, since):
            continue
        key = issue_key(project, "Issue", iid)
        user = e.get("user") or {}
        out.append(Event(
            id=event_id("assign", key, at), at=at, kind="assign",
            title=issue.get("title", ""), body=issue.get("title", ""),
            author=user.get("name", ""),
            url=note_url(project, "Issue", iid), action="task",
            extra=source(key, trigger="assign", repo=project),
            author_login=str(user.get("username") or ""),
            author_email=author_email(user),
        ))
    return out


# ---- writing -----------------------------------------------------------

def note_body(text: str, link: dict | None, attachment_markdown: str) -> str:
    """What the reply says. GitLab has no remote links, and an upload is a piece
    of markdown, so both belong in the text — otherwise the person who asked sees
    an answer mentioning a document nobody can find."""
    parts = [text.strip()] if text and text.strip() else []
    if link and link.get("url"):
        parts.append(f"[{link.get('title') or say('Открыть в Buff', 'Open in Buff')}]({link['url']})")
    if attachment_markdown:
        parts.append(say(f"Документ: {attachment_markdown}", f"Document: {attachment_markdown}"))
    return "\n\n".join(parts)


def upload(project: str, attachment: dict) -> str:
    """Upload a file to the project and return the markdown that renders it."""
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
    res = plugin.http("POST", f"{BASE}/api/v4/projects/{enc(project)}/uploads",
                      credential="api",
                      headers={"Content-Type": f"multipart/form-data; boundary={boundary}"},
                      body=body.decode("latin-1"))
    if res["status"] >= 400:
        raise RuntimeError(say(f"вложение не принято: {res['status']} {res['body'][:200]}",
                               f"the attachment was not accepted: {res['status']} {res['body'][:200]}"))
    return str(json.loads(res["body"]).get("markdown", ""))


def notes_path(project: str, kind: str, ident) -> str:
    if kind == "MergeRequest":
        return f"/projects/{enc(project)}/merge_requests/{ident}/notes"
    if kind == "Commit":
        return f"/projects/{enc(project)}/repository/commits/{ident}/comments"
    return f"/projects/{enc(project)}/issues/{ident}/notes"


def notes_field(kind: str) -> str:
    """What GitLab calls the text of a comment — and it is not one word.

    A note on an issue or a merge request is posted as `body`; a comment on a
    commit is posted as `note`. Sending the wrong one is answered with
    "body is missing", which a live run said and the documentation did not."""
    return "note" if kind == "Commit" else "body"


# ---- plugin surface ----------------------------------------------------

@plugin.check
def check():
    version = api("GET", "/version").get("version", "?")
    whoami()
    n = len(scope.allowed)
    who = me['name'] or me['username']
    return {"ok": True, "detail": say(
        f"GitLab {version}, вошли как {who}. Бот работает в {n} {plural(n, 'проекте', 'проектах', 'проектах')}.",
        f"GitLab {version}, signed in as {who}. The bot works in {n} {plural_en(n, 'project', 'projects')}.")}


def events_query(since: str | None) -> str:
    """The feed's query for one poll.

    GitLab filters events by DATE and EXCLUDES that date: `after=<today>` returns
    nothing that happened today, so a watermark passed straight through means a
    poller that quietly sees no events at all. The window therefore starts the
    day BEFORE the watermark, and the extra day is filtered out here by moment.
    """
    q = "?action=commented&per_page=50"
    if since:
        at = moment(since)
        if at:
            q += f"&after={(at - timedelta(days=1)).strftime('%Y-%m-%d')}"
    return q


@plugin.poll
def poll(since):
    if not me["username"]:
        whoami()
    events: list[Event] = []
    for project in PROJECTS:
        params = events_query(since)
        for ev in api("GET", f"/projects/{enc(project)}/events{params}") or []:
            e = event_from(project, ev, since)
            if e:
                events.append(e)
        if ACCEPT_ASSIGNMENT:
            issues = api("GET", f"/projects/{enc(project)}/issues?per_page=50"
                                + (f"&updated_after={since}" if since else "")) or []
            for issue in issues:
                events.extend(assignment_events(project, issue, since))
    return events


@plugin.webhook
def webhook(headers, body):
    """GitLab's Note Hook carries the note and what it hangs on, so a request can
    be turned into an event without going back for either.

    Anything else — a push, a pipeline, an action we do not act on — produces
    nothing, and the poll picks up whatever a callback missed.
    """
    try:
        payload = json.loads(body or "{}")
    except ValueError:
        return []
    if payload.get("object_kind") != "note":
        return []
    attrs = payload.get("object_attributes") or {}
    project = ((payload.get("project") or {}).get("path_with_namespace") or "").strip()
    if not project or not attrs:
        return []
    if not me["username"]:
        whoami()
    kind = str(attrs.get("noteable_type", ""))
    ident = ""
    if kind == "MergeRequest":
        ident = (payload.get("merge_request") or {}).get("iid", "")
    elif kind == "Issue":
        ident = (payload.get("issue") or {}).get("iid", "")
    elif kind == "Commit":
        ident = (payload.get("commit") or {}).get("id", "")
    # The branch is in the payload for a merge request, so a callback about one
    # costs no extra request at all.
    branch = (payload.get("merge_request") or {}).get("source_branch", "")
    ev = {
        "author": payload.get("user") or {},
        "note": {"id": attrs.get("id"), "body": attrs.get("note", ""),
                 "created_at": attrs.get("created_at", ""), "noteable_type": kind,
                 "noteable_iid": ident, "commit_id": ident if kind == "Commit" else "",
                 "internal": attrs.get("internal"), "confidential": attrs.get("confidential")},
    }
    e = event_from(project, ev, None, lambda *_: branch or source_branch(project, ident))
    return [e] if e else []


@plugin.reply
def reply(issue: str = "", text: str = "", link: dict | None = None,
          attachment: dict | None = None, visibility: str = "", **_):
    project, kind, ident = split_key(issue)
    # The marked projects bound writes too, not only reads.
    scope.require(project)
    markdown = upload(project, attachment) if attachment else ""
    body = note_body(text, link, markdown)
    if body:
        api("POST", notes_path(project, kind, ident), note_payload(kind, body, visibility))
    return {"ok": True}


def note_payload(kind: str, body: str, visibility: str) -> dict:
    """The answer to an internal note is internal too. A commit comment has no
    such flag — and so no internal question to answer."""
    payload = {notes_field(kind): body}
    if visibility == "internal" and kind != "Commit":
        payload["internal"] = True
    return payload


@plugin.tool(read_only=True,
             schema={"type": "object",
                     "properties": {"query": {"type": "string"}, "limit": {"type": "integer"}},
                     "required": ["query"], "additionalProperties": False})
def search_issues(query: str = "", limit: int = 20):
    found = []
    for project in PROJECTS:
        for kind, mark in (("issues", "Issue"), ("merge_requests", "MergeRequest")):
            for i in api("GET", f"/projects/{enc(project)}/{kind}"
                                f"?search={quote(query)}&per_page={limit}") or []:
                found.append({"key": issue_key(project, mark, i.get("iid")),
                              "title": i.get("title"), "state": i.get("state"),
                              "kind": "pull" if mark == "MergeRequest" else "issue"})
    return {"total": len(found), "issues": found}


@plugin.tool(read_only=True,
             schema={"type": "object", "properties": {"key": {"type": "string"}},
                     "required": ["key"], "additionalProperties": False})
def get_issue(key: str = ""):
    project, kind, ident = split_key(key)
    scope.require(project)
    part = "merge_requests" if kind == "MergeRequest" else "issues"
    item = api("GET", f"/projects/{enc(project)}/{part}/{ident}")
    notes = api("GET", f"/projects/{enc(project)}/{part}/{ident}/notes?per_page=100") or []
    return {
        "key": key,
        "title": item.get("title"),
        "state": item.get("state"),
        "body": item.get("description"),
        "kind": "pull" if kind == "MergeRequest" else "issue",
        "comments": [{"author": (n.get("author") or {}).get("username"),
                      "created": n.get("created_at"), "body": n.get("body")}
                     for n in notes if not n.get("system")],
    }


@plugin.tool(read_only=True,
             schema={"type": "object", "properties": {"key": {"type": "string"}},
                     "required": ["key"], "additionalProperties": False})
def get_merge_diff(key: str = ""):
    project, kind, ident = split_key(key)
    scope.require(project)
    if kind != "MergeRequest":
        raise RuntimeError(say(f"{key} — не merge request", f"{key} is not a merge request"))
    data = api("GET", f"/projects/{enc(project)}/merge_requests/{ident}/changes")
    return {"key": key, "changes": [{"path": c.get("new_path"), "diff": c.get("diff")}
                                    for c in data.get("changes", [])]}


@plugin.tool(read_only=False,
             schema={"type": "object",
                     "properties": {"key": {"type": "string"}, "text": {"type": "string"}},
                     "required": ["key", "text"], "additionalProperties": False})
def add_comment(key: str = "", text: str = ""):
    project, kind, ident = split_key(key)
    scope.require(project)
    api("POST", notes_path(project, kind, ident), {notes_field(kind): text})
    return {"ok": True}


if __name__ == "__main__":
    plugin.run()
