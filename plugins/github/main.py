"""GitHub integration — github.com and GitHub Enterprise Server.

One REST API serves both; what differs is the address (`api.github.com` against
`https://<host>/api/v3`). So this is one plugin, pointed at whichever the
customer runs.

Three shapes off the live API decide most of what is here:

1. One call per repository returns EVERY comment since a moment
   (`/issues/comments?since=`), and it covers pull requests too — GitHub keeps
   both in one place, because a pull request IS an issue to it.
2. Which of the two a comment belongs to is told by its `html_url`: the API's
   `issue_url` says `/issues/N` for both, and only the browser address carries
   `/pull/N`.
3. Comments on COMMITS live in their own list (`/comments?since=`), and each
   carries `commit_id` — so unlike some trackers, a commit comment can be routed
   from polling alone.

A question asked in a pull request is about the code ON that pull request, and
one under a commit about that commit, so the event carries the repository and
the branch (or the commit) and the platform answers there (#287).
"""

from __future__ import annotations

import json
from urllib.parse import quote

from buff_plugin import (
    plural,
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

plugin = Plugin("github")

BASE = plugin.config.get("base_url", "https://api.github.com").rstrip("/")
REPOS = plugin.config.get("repos", [])
ALIAS = plugin.config.get("mention_alias", "")

# The marked repositories ARE the access control — see Scope in the SDK.
scope = Scope(REPOS, fold_case=True)

# Work is taken on a DIRECT ADDRESS. Assignment is addressed at the bot too, but
# it also happens by routing rules, so it is off unless the operator says their
# assignments are deliberate (#282).
ACCEPT_ASSIGNMENT = bool(plugin.config.get("accept_assignment"))

me = {"login": "", "name": ""}

# The public e-mail of each author seen, by login: a comment carries none, and the
# connection's "who may ask" rules match on it (#476). One lookup per person.
emails: dict[str, str] = {}


# ---- transport ---------------------------------------------------------

def api(method: str, path: str, body: dict | None = None, accept: str = ""):
    headers = {"Accept": accept or "application/vnd.github+json",
               "X-GitHub-Api-Version": "2022-11-28"}
    if body:
        headers["Content-Type"] = "application/json"
    res = plugin.http(method, BASE + path, credential="api", headers=headers,
                      body=json.dumps(body) if body else "")
    if res["status"] >= 400:
        raise RuntimeError(f"GitHub ответил {res['status']} на {path}: {res['body'][:300]}")
    if accept and "json" not in accept:
        return res["body"]
    return json.loads(res["body"]) if res["body"].strip() else {}


def whoami() -> None:
    who = api("GET", "/user")
    me["login"] = who.get("login", "")
    me["name"] = who.get("name") or who.get("login", "")


def public_email(login: str) -> str:
    """The e-mail the account shows publicly, or "" — GitHub has no other one to
    give, and a lookup that fails must not cost the request."""
    if not login:
        return ""
    if login not in emails:
        try:
            emails[login] = str(api("GET", f"/users/{quote(login)}").get("email") or "")
        except (RuntimeError, ValueError):
            return ""
    return emails[login]


def identity() -> Identity:
    return Identity(name=me["login"], display=me["name"], alias=ALIAS)


# ---- reading -----------------------------------------------------------

def issue_key(repo: str, kind: str, ident) -> str:
    """`acme/api#7` for an issue or pull request, `acme/api@<sha>` for a commit.
    GitHub numbers issues and pull requests in one sequence, so one mark serves
    both — the context says which it was."""
    return f"{repo}{'@' if kind == 'commit' else '#'}{ident}"


def split_key(key: str) -> tuple[str, str, str]:
    for mark, kind in (("@", "commit"), ("#", "issue")):
        repo, sep, ident = key.rpartition(mark)
        if sep and repo and ident:
            return repo, kind, ident
    raise RuntimeError(f"{key} — не адрес обсуждения в GitHub (нужно владелец/репозиторий#номер)")


def is_pull(comment: dict) -> bool:
    """A pull request's comment is told apart by the BROWSER address: the API's
    `issue_url` says `/issues/N` for a pull request too, because to GitHub a
    pull request is an issue with a branch attached."""
    return "/pull/" in str(comment.get("html_url", ""))


def issue_number(comment: dict) -> int:
    """The number of the thread a comment belongs to.

    A comment left on a LINE of a pull request carries no `issue_url` at all —
    only `pull_request_url`. Read without it, such a comment looks like a commit
    comment (it does have a `commit_id`), and the answer lands on the commit
    instead of the review it was asked in.
    """
    for field in ("issue_url", "pull_request_url"):
        tail = str(comment.get(field, "")).rstrip("/").rsplit("/", 1)[-1]
        if tail.isdigit():
            return int(tail)
    return 0


def head_ref(repo: str, number) -> str:
    """The branch a pull request is built from. Best effort: an answer on the
    default branch beats losing the request, and the reply says which branch it
    was computed on either way."""
    try:
        pull = api("GET", f"/repos/{repo}/pulls/{number}")
    except RuntimeError:
        return ""
    return str((pull.get("head") or {}).get("ref", ""))


def event_from_comment(repo: str, comment: dict, since: str | None, ref_of=head_ref) -> Event | None:
    """One comment on an issue or a pull request, turned into a request.

    Every path that produces an event goes through here, which is why the scope
    check lives here rather than in the query (#282).
    """
    if not scope.allows(repo):
        return None
    author = comment.get("user") or {}
    if identity().is_me(author.get("login"), author.get("name")):
        return None  # the bot's own replies are not requests
    at = str(comment.get("created_at", ""))
    if not after(at, since):
        return None
    cmd = parse_command(comment.get("body", "") or "", identity())
    if not cmd:
        return None
    number = issue_number(comment)
    if not number:
        return None
    pull = is_pull(comment)
    key = issue_key(repo, "issue", number)
    extra = source(key, trigger="mention", context="pull" if pull else "issue",
                   repo=repo, ref=ref_of(repo, number) if pull else "")
    return Event(
        id=event_id("comment", comment.get("id"), at), at=at, kind="comment",
        title=key, body=cmd.instruction,
        author=author.get("login", ""), author_login=author.get("login", ""),
        author_email=public_email(author.get("login", "")),
        url=str(comment.get("html_url", "")), action=cmd.action, extra=extra,
    )


def event_from_commit_comment(repo: str, comment: dict, since: str | None) -> Event | None:
    """A comment on a commit. It carries `commit_id`, so it can be routed — and
    the commit IS the ref: an answer about a commit is computed on it and on
    nothing else."""
    if not scope.allows(repo):
        return None
    author = comment.get("user") or {}
    if identity().is_me(author.get("login"), author.get("name")):
        return None
    at = str(comment.get("created_at", ""))
    if not after(at, since):
        return None
    cmd = parse_command(comment.get("body", "") or "", identity())
    if not cmd:
        return None
    sha = str(comment.get("commit_id", ""))
    if not sha:
        return None
    key = issue_key(repo, "commit", sha)
    return Event(
        id=event_id("commit-comment", comment.get("id"), at), at=at, kind="comment",
        title=key, body=cmd.instruction, author=author.get("login", ""),
        author_login=author.get("login", ""), author_email=public_email(author.get("login", "")),
        url=str(comment.get("html_url", "")), action=cmd.action,
        extra=source(key, trigger="mention", context="commit", repo=repo, ref=sha),
    )


def assignment_events(repo: str, issue: dict, since: str | None) -> list[Event]:
    """Assignment of an issue to the bot, read from its timeline.

    From the timeline and not from the current assignee: reading the current
    field means switching the integration on fires it at every issue that
    already names the bot.
    """
    out: list[Event] = []
    if not ACCEPT_ASSIGNMENT or not scope.allows(repo):
        return out
    number = issue.get("number")
    events = api("GET", f"/repos/{repo}/issues/{number}/timeline?per_page=50") or []
    for e in events:
        if e.get("event") != "assigned":
            continue
        if not identity().is_me((e.get("assignee") or {}).get("login")):
            continue
        at = str(e.get("created_at", ""))
        if not after(at, since):
            continue
        key = issue_key(repo, "issue", number)
        actor = str((e.get("actor") or {}).get("login", ""))
        out.append(Event(
            id=event_id("assign", key, at), at=at, kind="assign",
            title=issue.get("title", ""), body=issue.get("title", ""),
            author=actor, author_login=actor, author_email=public_email(actor),
            url=str(issue.get("html_url", "")), action="task",
            extra=source(key, trigger="assign", repo=repo),
        ))
    return out


# ---- writing -----------------------------------------------------------

def comment_body(text: str, link: dict | None, attachment_note: str) -> str:
    """What the reply says. GitHub takes no attachment through its API — only
    the browser can upload one — so a document is named by its link or not at
    all, and saying so beats promising a file nobody can open."""
    parts = [text.strip()] if text and text.strip() else []
    if link and link.get("url"):
        parts.append(f"[{link.get('title') or 'Открыть в Buff'}]({link['url']})")
    if attachment_note:
        parts.append(attachment_note)
    return "\n\n".join(parts)


# ---- plugin surface ----------------------------------------------------

@plugin.check
def check():
    whoami()
    # GitHub Enterprise Server names its version; github.com answers 404 to the
    # same call, and that ABSENCE is how the two are told apart.
    where = "github.com"
    try:
        version = api("GET", "/meta").get("installed_version")
        if version:
            where = f"Enterprise Server {version}"
    except RuntimeError:
        pass
    return {"ok": True,
            "detail": f"GitHub ({where}), вошли как {identity().signed_in()}. "
                      f"Бот работает в {len(scope.allowed)} {plural(len(scope.allowed), 'репозитории', 'репозиториях', 'репозиториях')}."}


@plugin.poll
def poll(since):
    if not me["login"]:
        whoami()
    events: list[Event] = []
    # Whole seconds, in UTC: the watermark carries fractions, and the window is
    # a QUERY — what is actually new is still decided by `after` below.
    window = f"&since={quote(at_second(since))}" if since else ""
    for repo in REPOS:
        for c in api("GET", f"/repos/{repo}/issues/comments?per_page=50&sort=created{window}") or []:
            e = event_from_comment(repo, c, since)
            if e:
                events.append(e)
        for c in api("GET", f"/repos/{repo}/comments?per_page=50{window}") or []:
            e = event_from_commit_comment(repo, c, since)
            if e:
                events.append(e)
        if ACCEPT_ASSIGNMENT:
            issues = api("GET", f"/repos/{repo}/issues?state=all&per_page=50{window}") or []
            for issue in issues:
                events.extend(assignment_events(repo, issue, since))
    return events


@plugin.webhook
def webhook(headers, body):
    """GitHub's callback carries the comment and what it hangs on, so a request
    becomes an event without going back for either. A pull request's branch is
    in the payload too, so a callback about one costs no extra request.

    Anything else — a push, a release, an action we do not act on — produces
    nothing, and the poll picks up whatever a callback missed.
    """
    try:
        payload = json.loads(body or "{}")
    except ValueError:
        return []
    if payload.get("action") not in (None, "created"):
        return []
    comment = payload.get("comment")
    repo = ((payload.get("repository") or {}).get("full_name") or "").strip()
    if not comment or not repo:
        return []
    if not me["login"]:
        whoami()
    # An issue, a pull request, or a line inside one: all three are a thread with
    # a number, and the payload names the branch for the last two.
    if payload.get("issue") or comment.get("pull_request_url"):
        branch = ((payload.get("pull_request") or {}).get("head") or {}).get("ref", "")
        return [e] if (e := event_from_comment(repo, comment, None, lambda *_: branch
                                               or head_ref(repo, issue_number(comment)))) else []
    if comment.get("commit_id"):
        return [e] if (e := event_from_commit_comment(repo, comment, None)) else []
    return []


@plugin.reply
def reply(issue: str = "", text: str = "", link: dict | None = None,
          attachment: dict | None = None, **_):
    repo, kind, ident = split_key(issue)
    # The marked repositories bound writes too, not only reads.
    scope.require(repo)
    note = ("Документ приложен в обсуждении в Buff — GitHub не принимает файлы через API."
            if attachment else "")
    body = comment_body(text, link, note)
    if not body:
        return {"ok": True}
    if kind == "commit":
        api("POST", f"/repos/{repo}/commits/{ident}/comments", {"body": body})
    else:
        api("POST", f"/repos/{repo}/issues/{ident}/comments", {"body": body})
    return {"ok": True}


@plugin.tool(description="Найти задачи и pull request в подключённых репозиториях по тексту. "
                         "Использовать, когда упомянут номер или нужен контекст из трекера клиента.",
             read_only=True,
             schema={"type": "object",
                     "properties": {"query": {"type": "string"}, "limit": {"type": "integer"}},
                     "required": ["query"], "additionalProperties": False})
def search_issues(query: str = "", limit: int = 20):
    found = []
    for repo in REPOS:
        q = quote(f"repo:{repo} {query}")
        data = api("GET", f"/search/issues?q={q}&per_page={limit}")
        for i in data.get("items", []):
            found.append({"key": issue_key(repo, "issue", i.get("number")),
                          "title": i.get("title"), "state": i.get("state"),
                          "kind": "pull" if i.get("pull_request") else "issue"})
    return {"total": len(found), "issues": found}


@plugin.tool(description="Прочитать задачу или pull request целиком, вместе с обсуждением. "
                         "Использовать, когда нужны требования, критерии приёмки или ход обсуждения.",
             read_only=True,
             schema={"type": "object", "properties": {"key": {"type": "string"}},
                     "required": ["key"], "additionalProperties": False})
def get_issue(key: str = ""):
    repo, kind, ident = split_key(key)
    scope.require(repo)
    issue = api("GET", f"/repos/{repo}/issues/{ident}")
    comments = api("GET", f"/repos/{repo}/issues/{ident}/comments?per_page=100") or []
    return {
        "key": key,
        "title": issue.get("title"),
        "state": issue.get("state"),
        "body": issue.get("body"),
        "kind": "pull" if issue.get("pull_request") else "issue",
        "comments": [{"author": (c.get("user") or {}).get("login"),
                      "created": c.get("created_at"), "body": c.get("body")} for c in comments],
    }


@plugin.tool(description="Прочитать изменения pull request одним diff. Использовать, когда вопрос "
                         "касается того, что именно меняет этот pull request.",
             read_only=True,
             schema={"type": "object", "properties": {"key": {"type": "string"}},
                     "required": ["key"], "additionalProperties": False})
def get_pull_diff(key: str = ""):
    repo, kind, ident = split_key(key)
    scope.require(repo)
    diff = api("GET", f"/repos/{repo}/pulls/{ident}", accept="application/vnd.github.diff")
    return {"key": key, "diff": diff}


@plugin.tool(description="Написать комментарий в задачу или pull request.",
             read_only=False,
             schema={"type": "object",
                     "properties": {"key": {"type": "string"}, "text": {"type": "string"}},
                     "required": ["key", "text"], "additionalProperties": False})
def add_comment(key: str = "", text: str = ""):
    reply(issue=key, text=text)
    return {"ok": True}


if __name__ == "__main__":
    plugin.run()
