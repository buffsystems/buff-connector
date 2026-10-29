"""Gitea (and Forgejo) integration.

Questions and answers where the team already works: a comment in an issue or in
a pull request reaches the bot, and the answer comes back as a comment in the
same thread.

Two shapes of Gitea's API decide most of what is here, and both were taken from
a live server rather than from the documentation:

1. One call per repository returns EVERY comment since a moment
   (`/issues/comments?since=`), and it covers pull requests as well — Gitea keeps
   both in one place. That is the whole poller.
2. A comment says it belongs to a pull request by carrying a NON-EMPTY
   `pull_request_url`; the issue it belongs to is only in `issue_url`, whose tail
   is the number.

A question asked in a pull request is about the code ON that pull request, so
the event carries the repository and the branch (#287) and the platform answers
there instead of on the default branch.
"""

from __future__ import annotations

import json
import re
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

plugin = Plugin("gitea")

BASE = plugin.config.get("base_url", "").rstrip("/")
REPOS = plugin.config.get("repos", [])
ALIAS = plugin.config.get("mention_alias", "")

# The marked repositories ARE the access control — see Scope in the SDK. Gitea
# folds case in repository names, so the comparison does too.
scope = Scope(REPOS, fold_case=True)

# Work is taken on a DIRECT ADDRESS. Assignment is addressed at the bot too, but
# it also happens by routing rules and bulk edits, so it is off unless the
# operator says their assignments are made by a person on purpose (#282).
ACCEPT_ASSIGNMENT = bool(plugin.config.get("accept_assignment"))

# The account the bot is logged in as, learned at startup.
me = {"login": "", "display": ""}


# ---- transport ---------------------------------------------------------

def api(method: str, path: str, body: dict | None = None):
    res = plugin.http(method, BASE + "/api/v1" + path, credential="api",
                      headers={"Content-Type": "application/json"} if body else {},
                      body=json.dumps(body) if body else "")
    if res["status"] >= 400:
        raise RuntimeError(say(f"Gitea ответила {res['status']} на {path}: {res['body'][:300]}",
                               f"Gitea answered {res['status']} to {path}: {res['body'][:300]}"))
    return json.loads(res["body"]) if res["body"] else {}


def whoami() -> None:
    who = api("GET", "/user")
    me["login"] = who.get("login", "")
    me["display"] = who.get("full_name") or who.get("login", "")


def identity() -> Identity:
    return Identity(name=me["login"], display=me["display"], alias=ALIAS)


# ---- reading -----------------------------------------------------------

def issue_key(repo: str, index: int) -> str:
    """How a thread is named everywhere outside this plugin. It has to carry the
    repository: an issue number alone means nothing across repositories."""
    return f"{repo}#{index}"


def split_key(key: str) -> tuple[str, int]:
    m = re.fullmatch(r"(?P<repo>[^#\s]+/[^#\s]+)#(?P<index>\d+)", str(key).strip())
    if not m:
        raise RuntimeError(say(f"{key} — не адрес обсуждения в Gitea (нужно owner/repo#номер)",
                               f"{key} is not a Gitea discussion address (owner/repo#number is needed)"))
    return m.group("repo"), int(m.group("index"))


def issue_index(comment: dict) -> int:
    """The number of the issue or pull request a comment belongs to.

    Gitea puts it in `issue_url` for an issue and in `pull_request_url` for a
    pull request — and leaves the OTHER one an empty string, so reading only
    `issue_url` finds nothing for every comment written under a pull request.
    That is not what the field names suggest, and a live run is what said so.
    """
    for field in ("issue_url", "pull_request_url"):
        tail = str(comment.get(field) or "").rstrip("/").rsplit("/", 1)[-1]
        if tail.isdigit():
            return int(tail)
    return 0


def is_pull(comment: dict) -> bool:
    """Gitea marks a pull request's comment with a non-empty `pull_request_url`.
    It is present and EMPTY on an issue's comment, so its presence proves
    nothing — only its content does."""
    return bool(str(comment.get("pull_request_url", "")).strip())


def head_ref(repo: str, index: int) -> str:
    """The branch a pull request is built from. Best effort: an answer on the
    default branch beats losing the request, and the reply says which branch it
    was computed on either way."""
    try:
        pull = api("GET", f"/repos/{repo}/pulls/{index}")
    except RuntimeError:
        return ""
    return str((pull.get("head") or {}).get("ref", ""))


def real_email(user: dict) -> str:
    """The author's e-mail as Gitea shows it — but not the stand-in Gitea puts
    there for a person who keeps theirs private (`<login>@noreply.<host>`): the
    connection's "who may ask" rules match on a real address or on none (#476)."""
    email = str(user.get("email") or "").strip()
    domain = email.rpartition("@")[2].lower()
    if not domain or "noreply" in domain.split("."):
        return ""
    return email


# Addresses read off profiles, by login: one request per author per run (#476).
emails: dict[str, str] = {}


def author_email(user: dict) -> str:
    """The author's real address, for the connection's "who may ask" rules.

    A comment's poster always comes with the stand-in — seen on Gitea 1.27 even
    for a person who shows their address — so the profile is read once, where
    Gitea shows the real one unless its owner hides it (an admin's token sees it
    regardless). A read that fails is not remembered and costs the request nothing."""
    email = real_email(user)
    login = str(user.get("login") or "")
    if email or not login:
        return email
    if login not in emails:
        try:
            emails[login] = real_email(api("GET", f"/users/{quote(login)}"))
        except (RuntimeError, ValueError):
            return ""
    return emails[login]


def event_from_comment(repo: str, comment: dict, since: str | None, ref_of=head_ref) -> Event | None:
    """One comment, turned into a request — or into nothing at all.

    Every path that produces an event goes through here, which is why the scope
    check lives here rather than in the query: a webhook is configured on the
    server side, and a wrong filter there would otherwise deliver comments from
    repositories the operator never approved (#282).
    """
    if not scope.allows(repo):
        return None
    author = comment.get("user") or {}
    if identity().is_me(author.get("login"), author.get("full_name")):
        return None  # the bot's own replies are not requests
    at = str(comment.get("created_at", ""))
    # An edit is not a new request, so the moment that counts is when the comment
    # was written. Gitea's `since` filters on the update, so this is also what
    # keeps an edited old comment from being answered a second time.
    if not after(at, since):
        return None
    cmd = parse_command(comment.get("body", "") or "", identity())
    if not cmd:
        return None
    index = issue_index(comment)
    if not index:
        return None
    pull = is_pull(comment)
    # The repository travels with EVERY request, not only with a pull request's.
    # A question asked in an issue is about the code of that repository, and
    # without its name the answer is computed at the top of the dev project —
    # which, for a project with one module, means describing the wrapper around
    # the customer's code instead of the code.
    extra = source(issue_key(repo, index), trigger="mention",
                   context="pull" if pull else "issue",
                   repo=repo,
                   ref=ref_of(repo, index) if pull else "")
    return Event(
        id=event_id("comment", comment.get("id"), at), at=at, kind="comment",
        title=extra["issue"], body=cmd.instruction,
        author=author.get("full_name") or author.get("login", ""),
        author_login=author.get("login", ""), author_email=author_email(author),
        url=str(comment.get("html_url", "")), action=cmd.action, extra=extra,
    )


def events_from_timeline(repo: str, index: int, title: str, entries: list,
                         since: str | None) -> list[Event]:
    """Assignment of the issue to the bot, read from the timeline.

    From the timeline and not from the issue's current assignee: reading the
    current field means switching the integration on fires it at every ticket
    that already names the bot. Entries are matched by WHO was named rather than
    by Gitea's label for the entry, which has changed between releases.
    """
    out: list[Event] = []
    if not ACCEPT_ASSIGNMENT or not scope.allows(repo):
        return out
    for e in entries or []:
        assignee = e.get("assignee") or {}
        # By LOGIN only. Gitea always carries one here, and a display name is not
        # unique — matching on it would let anyone whose full name happens to be
        # the bot's start paid work by being assigned a ticket.
        if not identity().is_me(assignee.get("login")):
            continue
        at = str(e.get("created_at", ""))
        if not after(at, since):
            continue
        author = e.get("user") or {}
        out.append(Event(
            id=event_id("assign", issue_key(repo, index), at), at=at, kind="assign",
            title=title, body=title,
            author=author.get("full_name") or author.get("login", ""),
            author_login=author.get("login", ""), author_email=author_email(author),
            url=f"{BASE}/{repo}/issues/{index}", action="task",
            extra=source(issue_key(repo, index), trigger="assign"),
        ))
    return out


# ---- writing -----------------------------------------------------------

def comment_body(text: str, link: dict | None, attachment_url: str) -> str:
    """What the reply actually says.

    Gitea has no remote links and keeps attachments beside the issue rather than
    inside a comment, so both have to be named in the text — otherwise the person
    who asked sees an answer that mentions a document nobody can find.
    """
    parts = [text.strip()] if text and text.strip() else []
    if link and link.get("url"):
        parts.append(f"[{link.get('title') or say('Открыть в Buff', 'Open in Buff')}]({link['url']})")
    if attachment_url:
        parts.append(say(f"Документ: {attachment_url}", f"Document: {attachment_url}"))
    return "\n\n".join(parts)


def upload(repo: str, index: int, attachment: dict) -> str:
    """Attach a file to the issue and return where it can be downloaded."""
    import base64
    import os as _os

    name = attachment.get("filename", "report.pdf")
    content = base64.b64decode(attachment.get("base64", ""))
    boundary = "----buff" + base64.b16encode(_os.urandom(8)).decode()
    body = (
        f"--{boundary}\r\n"
        f'Content-Disposition: form-data; name="attachment"; filename="{name}"\r\n'
        "Content-Type: application/octet-stream\r\n\r\n"
    ).encode() + content + f"\r\n--{boundary}--\r\n".encode()
    res = plugin.http("POST", f"{BASE}/api/v1/repos/{repo}/issues/{index}/assets?name={name}",
                      credential="api",
                      headers={"Content-Type": f"multipart/form-data; boundary={boundary}"},
                      body=body.decode("latin-1"))
    if res["status"] >= 400:
        raise RuntimeError(say(f"вложение не принято: {res['status']} {res['body'][:200]}",
                               f"the attachment was not accepted: {res['status']} {res['body'][:200]}"))
    return str(json.loads(res["body"]).get("browser_download_url", ""))


# ---- plugin surface ----------------------------------------------------

@plugin.check
def check():
    version = api("GET", "/version").get("version", "?")
    whoami()
    n = len(scope.allowed)
    who = me['display'] or me['login']
    return {"ok": True, "detail": say(
        f"Gitea {version}, вошли как {who}. Бот работает в {n} {plural(n, 'репозитории', 'репозиториях', 'репозиториях')}.",
        f"Gitea {version}, signed in as {who}. The bot works in {n} {plural_en(n, 'repository', 'repositories')}.")}


@plugin.poll
def poll(since):
    if not me["login"]:
        whoami()
    events: list[Event] = []
    for repo in REPOS:
        params = f"?limit=50{'&since=' + since if since else ''}"
        for c in api("GET", f"/repos/{repo}/issues/comments{params}") or []:
            e = event_from_comment(repo, c, since)
            if e:
                events.append(e)
        if ACCEPT_ASSIGNMENT:
            events.extend(assignments(repo, since))
    return events


def assignments(repo: str, since: str | None) -> list[Event]:
    """Issues touched since the watermark, checked for the bot being assigned."""
    out: list[Event] = []
    params = f"?state=all&limit=50{'&since=' + since if since else ''}"
    for issue in api("GET", f"/repos/{repo}/issues{params}") or []:
        index = int(issue.get("number") or 0)
        if not index:
            continue
        timeline = api("GET", f"/repos/{repo}/issues/{index}/timeline?limit=50")
        out.extend(events_from_timeline(repo, index, issue.get("title", ""), timeline, since))
    return out


@plugin.webhook
def webhook(headers, body):
    """Gitea's callback carries everything the poller would have gone back for.

    The comment inside it is shaped exactly like the one the API returns — the
    same pair of URLs, one of them empty — so ONE parser produces every event and
    there is no second set of edge cases to drift from the poller. The branch is
    in the payload too, so a callback about a pull request costs no extra request
    at all.

    Anything else — a push, a release, an action we do not act on — produces
    nothing, and the poll picks up whatever a callback missed. Note that Gitea
    sends comments on pull requests only to a hook subscribed to
    `pull_request_comment` as well as `issue_comment`.
    """
    try:
        payload = json.loads(body or "{}")
    except ValueError:
        return []
    comment = payload.get("comment")
    repo = ((payload.get("repository") or {}).get("full_name") or "").strip()
    if not comment or not repo or payload.get("action") not in (None, "created"):
        return []
    if not me["login"]:
        whoami()
    branch = ((payload.get("pull_request") or {}).get("head") or {}).get("ref", "")
    e = event_from_comment(repo, comment, None, lambda *_: branch or head_ref(repo, issue_index(comment)))
    return [e] if e else []


@plugin.reply
def reply(issue: str = "", text: str = "", link: dict | None = None,
          attachment: dict | None = None, **_):
    repo, index = split_key(issue)
    # The marked repositories bound writes too, not only reads.
    scope.require(repo)
    url = upload(repo, index, attachment) if attachment else ""
    body = comment_body(text, link, url)
    if body:
        api("POST", f"/repos/{repo}/issues/{index}/comments", {"body": body})
    return {"ok": True}


@plugin.tool(read_only=True,
             schema={"type": "object",
                     "properties": {"query": {"type": "string"}, "limit": {"type": "integer"}},
                     "required": ["query"], "additionalProperties": False})
def search_issues(query: str = "", limit: int = 20):
    found = []
    for repo in REPOS:
        for i in api("GET", f"/repos/{repo}/issues?q={query}&state=all&limit={limit}") or []:
            found.append({"key": issue_key(repo, i.get("number")), "title": i.get("title"),
                          "state": i.get("state"),
                          "kind": "pull" if i.get("pull_request") else "issue"})
    return {"total": len(found), "issues": found}


@plugin.tool(read_only=True,
             schema={"type": "object", "properties": {"key": {"type": "string"}},
                     "required": ["key"], "additionalProperties": False})
def get_issue(key: str = ""):
    repo, index = split_key(key)
    scope.require(repo)
    issue = api("GET", f"/repos/{repo}/issues/{index}")
    comments = api("GET", f"/repos/{repo}/issues/{index}/comments?limit=100") or []
    return {
        "key": key,
        "title": issue.get("title"),
        "state": issue.get("state"),
        "body": issue.get("body"),
        "kind": "pull" if issue.get("pull_request") else "issue",
        "comments": [{"author": (c.get("user") or {}).get("login"),
                      "created": c.get("created_at"), "body": c.get("body")} for c in comments],
    }


@plugin.tool(read_only=True,
             schema={"type": "object", "properties": {"key": {"type": "string"}},
                     "required": ["key"], "additionalProperties": False})
def get_pull_diff(key: str = ""):
    repo, index = split_key(key)
    scope.require(repo)
    res = plugin.http("GET", f"{BASE}/api/v1/repos/{repo}/pulls/{index}.diff",
                      credential="api", headers={}, body="")
    if res["status"] >= 400:
        raise RuntimeError(say(f"Gitea ответила {res['status']} на diff {key}", f"Gitea answered {res['status']} to the diff of {key}"))
    return {"key": key, "diff": res["body"]}


@plugin.tool(read_only=False,
             schema={"type": "object",
                     "properties": {"key": {"type": "string"}, "text": {"type": "string"}},
                     "required": ["key", "text"], "additionalProperties": False})
def add_comment(key: str = "", text: str = ""):
    repo, index = split_key(key)
    scope.require(repo)
    api("POST", f"/repos/{repo}/issues/{index}/comments", {"body": text})
    return {"ok": True}


if __name__ == "__main__":
    plugin.run()
