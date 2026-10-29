"""Redmine integration.

Questions and answers where the team already works: a note in an issue reaches
the bot, and the answer comes back as a note in the same issue.

Redmine has no pull requests and no repository of its own to speak of, so this
is an issue tracker and nothing else — the page says so rather than leaving a
customer to discover it.

Two shapes decide most of what is here, and both came off a live server:

1. There is no "every note in this project since a moment" endpoint. Issues
   updated since the watermark are listed in one call, and each is then read
   WITH its journals — a request per touched issue, which is the smallest set
   there is.
2. A journal entry names its author by DISPLAY NAME only — no login anywhere.
   The bot therefore has to know what it is called as well as who it is, or it
   answers its own replies for ever.
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
    at_second,
    event_id,
    parse_command,
    source,
)

from textile import to_textile

plugin = Plugin("redmine")

BASE = plugin.config.get("base_url", "").rstrip("/")
PROJECTS = plugin.config.get("projects", [])
ALIAS = plugin.config.get("mention_alias", "")

# The marked projects ARE the access control — see Scope in the SDK.
scope = Scope(PROJECTS, fold_case=True)

# Work is taken on a DIRECT ADDRESS. Assignment is addressed at the bot too, but
# it also happens by routing rules, so it is off unless the operator says their
# assignments are deliberate (#282).
ACCEPT_ASSIGNMENT = bool(plugin.config.get("accept_assignment"))
# How the server formats text — Redmine's own setting, which its API does not show.
# The answer comes as Markdown; on a Textile server it is converted (cloud-front#478).
TEXTILE = plugin.config.get("text_formatting", "markdown") == "textile"

me = {"login": "", "name": ""}


# ---- transport ---------------------------------------------------------

def api(method: str, path: str, body: dict | None = None):
    res = plugin.http(method, BASE + path, credential="api",
                      headers={"Content-Type": "application/json"} if body else {},
                      body=json.dumps(body) if body else "")
    if res["status"] >= 400:
        raise RuntimeError(say(f"Redmine ответила {res['status']} на {path}: {res['body'][:300]}",
                               f"Redmine answered {res['status']} to {path}: {res['body'][:300]}"))
    return json.loads(res["body"]) if res["body"].strip() else {}


def whoami() -> None:
    who = (api("GET", "/users/current.json") or {}).get("user", {})
    me["login"] = who.get("login", "")
    me["name"] = " ".join(p for p in (who.get("firstname"), who.get("lastname")) if p).strip()


def identity() -> Identity:
    return Identity(name=me["login"], display=me["name"], alias=ALIAS)


# Accounts already read, by user id: one request per author per run, not per note.
users: dict = {}


def read_user(uid) -> dict | None:
    """One account as Redmine shows it to this token; None when it could not be
    read just now. `login` and `mail` are in it only where Redmine lets the token
    see them — an admin's, or an address its owner did not hide."""
    res = plugin.http("GET", f"{BASE}/users/{uid}.json", credential="api")
    if res["status"] in (403, 404):
        return {}  # not this token's to see, or the account is gone: that will not change
    if res["status"] >= 400:
        return None
    return (json.loads(res["body"]) if res["body"].strip() else {}).get("user") or {}


def account(user: dict) -> dict:
    """Who wrote a journal entry, beyond the display name it carries
    (cloud-front#476): the connection's list of who may ask matches on the login
    and the address. A failed read is not remembered, so the next note tries
    again; what Redmine will not show stays empty, never guessed."""
    uid = user.get("id")
    if not uid:
        return {}
    if uid not in users:
        found = read_user(uid)
        if found is None:
            return {}
        users[uid] = found
    return users[uid]


# ---- reading -----------------------------------------------------------

def since_filter(since: str | None) -> str:
    """Redmine's `updated_on` window, in the only spelling it accepts.

    A watermark carries fractional seconds; Redmine answers a timestamp that has
    them with `422 Updated is invalid` — the poll then fails on every pass and
    the integration is deaf while looking healthy. Whole seconds in UTC is what
    it takes."""
    at = at_second(since)
    return "&updated_on=%3E%3D" + quote(at) if at else ""


def issue_key(project: str, ident) -> str:
    """`catalog#12`. The project travels with the number because an issue number
    alone means nothing once a second project is connected."""
    return f"{project}#{ident}"


def split_key(key: str) -> tuple[str, int]:
    project, _, ident = str(key).strip().rpartition("#")
    if not project or not ident.isdigit():
        raise RuntimeError(say(f"{key} — не адрес задачи в Redmine (нужно проект#номер)",
                               f"{key} is not a Redmine issue address (project#number is needed)"))
    return project, int(ident)


def event_from_journal(project: str, issue: dict, journal: dict, since: str | None) -> Event | None:
    """One journal entry, turned into a request — or into nothing at all.

    Every path that produces an event goes through here, which is why the scope
    check lives here rather than in the query (#282).
    """
    if not scope.allows(project):
        return None
    user = journal.get("user") or {}
    # A journal names its author by display name only, so that is what the bot
    # recognises itself by. It knows its own login too, which costs nothing to
    # check and covers a Redmine that starts sending one.
    if identity().is_me(user.get("name"), user.get("login")):
        return None
    at = str(journal.get("created_on", ""))
    if not after(at, since):
        return None
    cmd = parse_command(journal.get("notes", "") or "", identity())
    if not cmd:
        return None
    ident = issue.get("id")
    if not ident:
        return None
    key = issue_key(project, ident)
    who = account(user)
    return Event(
        id=event_id("journal", journal.get("id"), at), at=at, kind="comment",
        title=issue.get("subject", ""), body=cmd.instruction,
        author=user.get("name", ""), url=f"{BASE}/issues/{ident}",
        action=cmd.action, extra=source(key, trigger="mention", repo=project),
        author_login=str(who.get("login") or ""), author_email=str(who.get("mail") or ""),
        # A private note is seen only by those allowed to; so is the answer to it.
        visibility="private" if journal.get("private_notes") else "",
    )


def assignment_event(project: str, issue: dict, journal: dict, since: str | None) -> Event | None:
    """Assignment of an issue to the bot, read from the journal's DETAILS.

    From the journal and not from the issue's current assignee: reading the
    current field means switching the integration on fires it at every issue
    that already names the bot.
    """
    if not ACCEPT_ASSIGNMENT or not scope.allows(project):
        return None
    at = str(journal.get("created_on", ""))
    if not after(at, since):
        return None
    for d in journal.get("details") or []:
        if d.get("name") != "assigned_to_id" or not d.get("new_value"):
            continue
        if str(d.get("new_value")) != str(me.get("id", "")):
            continue
        key = issue_key(project, issue.get("id"))
        user = journal.get("user") or {}
        who = account(user)
        return Event(
            id=event_id("assign", key, at), at=at, kind="assign",
            title=issue.get("subject", ""), body=issue.get("subject", ""),
            author=user.get("name", ""),
            url=f"{BASE}/issues/{issue.get('id')}", action="task",
            extra=source(key, trigger="assign", repo=project),
            author_login=str(who.get("login") or ""), author_email=str(who.get("mail") or ""),
        )
    return None


# ---- writing -----------------------------------------------------------

def note_text(text: str, link: dict | None, attachment_note: str) -> str:
    """What the note says. Redmine has no remote links, so a link belongs in the
    text; an attachment is uploaded with the same note and named in it. The link
    is written in the server's markup, and so is the answer: one of the two in the
    other markup always showed raw (cloud-front#478)."""
    body = (to_textile(text) if TEXTILE else text).strip() if text else ""
    parts = [body] if body else []
    if link and link.get("url"):
        title = link.get("title") or say("Открыть в Buff", "Open in Buff")
        parts.append(f'"{title}":{link["url"]}' if TEXTILE else f"[{title}]({link['url']})")
    if attachment_note:
        parts.append(attachment_note)
    return "\n\n".join(parts)


def upload(attachment: dict) -> dict | None:
    """Redmine takes a file in two steps: the bytes go to /uploads.json, which
    answers with a token, and the token is attached to the issue update."""
    import base64

    name = attachment.get("filename", "report.pdf")
    content = base64.b64decode(attachment.get("base64", ""))
    res = plugin.http("POST", f"{BASE}/uploads.json?filename={quote(name)}",
                      credential="api",
                      headers={"Content-Type": "application/octet-stream"},
                      body=content.decode("latin-1"))
    if res["status"] >= 400:
        raise RuntimeError(say(f"вложение не принято: {res['status']} {res['body'][:200]}",
                               f"the attachment was not accepted: {res['status']} {res['body'][:200]}"))
    token = (json.loads(res["body"]).get("upload") or {}).get("token", "")
    if not token:
        return None
    return {"token": token, "filename": name, "content_type": "application/octet-stream"}


# ---- plugin surface ----------------------------------------------------

@plugin.check
def check():
    whoami()
    # Redmine publishes no version over its API, so the check says what it can
    # prove: which account it reached and how many projects it may act in.
    n = len(scope.allowed)
    who = me['name'] or me['login']
    return {"ok": True, "detail": say(
        f"Redmine, вошли как {who}. Бот работает в {n} {plural(n, 'проекте', 'проектах', 'проектах')}.",
        f"Redmine, signed in as {who}. The bot works in {n} {plural_en(n, 'project', 'projects')}.")}


@plugin.poll
def poll(since):
    if not me["login"]:
        whoami()
    events: list[Event] = []
    for project in PROJECTS:
        # The window is one-sided and wider than the watermark (whole seconds);
        # the rest is filtered by moment in event_from_journal.
        query = (f"?project_id={quote(project)}&status_id=*&sort=updated_on:desc&limit=50"
                 + since_filter(since))
        for stub in (api("GET", "/issues.json" + query) or {}).get("issues", []):
            issue = (api("GET", f"/issues/{stub['id']}.json?include=journals") or {}).get("issue", {})
            for journal in issue.get("journals", []):
                e = event_from_journal(project, issue, journal, since)
                if e:
                    events.append(e)
                assigned = assignment_event(project, issue, journal, since)
                if assigned:
                    events.append(assigned)
    return events


@plugin.reply
def reply(issue: str = "", text: str = "", link: dict | None = None,
          attachment: dict | None = None, visibility: str = "", **_):
    project, ident = split_key(issue)
    # The marked projects bound writes too, not only reads.
    scope.require(project)
    private = visibility == "private"
    # Only the note can be private: a file uploaded with it is attached to the
    # issue, where everyone who sees the issue sees it. The answer to a private
    # question keeps its document in Buff, behind the link.
    upload_spec = upload(attachment) if attachment and not private else None
    if upload_spec:
        doc = say(f"Документ приложен: {upload_spec['filename']}", f"The document is attached: {upload_spec['filename']}")
    elif attachment:
        doc = say("Документ — в Buff по ссылке: вопрос был закрытой заметкой, "
                  "а вложение к задаче увидели бы все.",
                  "The document is in Buff, by the link: the question was a private note, "
                  "and everyone would see an attachment to the issue.")
    else:
        doc = ""
    note = note_text(text, link, doc)
    body: dict = {"issue": {"notes": note}}
    if upload_spec:
        body["issue"]["uploads"] = [upload_spec]
    if private:
        body["issue"]["private_notes"] = True
    if note or upload_spec:
        api("PUT", f"/issues/{ident}.json", body)
    return {"ok": True}


@plugin.tool(read_only=True,
             schema={"type": "object",
                     "properties": {"query": {"type": "string"}, "limit": {"type": "integer"}},
                     "required": ["query"], "additionalProperties": False})
def search_issues(query: str = "", limit: int = 20):
    found = []
    for project in PROJECTS:
        data = api("GET", f"/search.json?q={quote(query)}&issues=1&limit={limit}"
                          f"&project_id={quote(project)}") or {}
        for r in data.get("results", []):
            found.append({"key": issue_key(project, r.get("id")), "title": r.get("title"),
                          "url": r.get("url")})
    return {"total": len(found), "issues": found}


@plugin.tool(read_only=True,
             schema={"type": "object", "properties": {"key": {"type": "string"}},
                     "required": ["key"], "additionalProperties": False})
def get_issue(key: str = ""):
    project, ident = split_key(key)
    scope.require(project)
    issue = (api("GET", f"/issues/{ident}.json?include=journals") or {}).get("issue", {})
    return {
        "key": key,
        "title": issue.get("subject"),
        "status": (issue.get("status") or {}).get("name"),
        "body": issue.get("description"),
        "comments": [{"author": (j.get("user") or {}).get("name"),
                      "created": j.get("created_on"), "body": j.get("notes")}
                     for j in issue.get("journals", []) if j.get("notes")],
    }


@plugin.tool(read_only=False,
             schema={"type": "object",
                     "properties": {"key": {"type": "string"}, "text": {"type": "string"}},
                     "required": ["key", "text"], "additionalProperties": False})
def add_comment(key: str = "", text: str = ""):
    project, ident = split_key(key)
    scope.require(project)
    api("PUT", f"/issues/{ident}.json", {"issue": {"notes": text}})
    return {"ok": True}


if __name__ == "__main__":
    plugin.run()
