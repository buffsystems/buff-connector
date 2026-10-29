"""Bitbucket Cloud integration.

Bitbucket Cloud is code first, and as of 2026 it is code ONLY: Atlassian removed
the issue-tracker API outright — `/2.0/repositories/…/issues` answers 410 Gone
(CHANGE-3071), on any repository, including public ones with a tracker visible in
the interface. So this integration lives in pull requests, like the Data Center
one, and a repository whose team keeps tickets elsewhere loses nothing.

Three shapes off the live API decide most of what is here:

1. A comment's text is at `content.raw` — `content` also carries a rendered
   `html`, and reading that instead would feed the model markup.
2. A pull request names its branch at `source.branch.name`.
3. Bitbucket Cloud has no usernames any more: an account is an `account_id`, a
   `nickname` and a `display_name`. The bot therefore recognises itself by all
   three, and a mention can be written as any of them.
"""

from __future__ import annotations

import json

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

plugin = Plugin("bitbucket-cloud")

# The address is fixed in practice, but it is a CONFIG field: the connector
# derives what a plugin may reach from the operator's own values, so a hard-coded
# host would be a promise the consent screen could not show.
BASE = plugin.config.get("base_url", "https://api.bitbucket.org").rstrip("/") + "/2.0"
REPOS = plugin.config.get("repos", [])
ALIAS = plugin.config.get("mention_alias", "")

# The marked repositories ARE the access control — see Scope in the SDK.
scope = Scope(REPOS, fold_case=True)

me = {"account_id": "", "nickname": "", "display": ""}


# ---- transport ---------------------------------------------------------

def api(method: str, path: str, body: dict | None = None):
    res = plugin.http(method, BASE + path, credential="api",
                      headers={"Content-Type": "application/json"} if body else {},
                      body=json.dumps(body) if body else "")
    if res["status"] >= 400:
        raise RuntimeError(say(f"Bitbucket ответил {res['status']} на {path}: {res['body'][:300]}",
                               f"Bitbucket answered {res['status']} to {path}: {res['body'][:300]}"))
    return json.loads(res["body"]) if res["body"].strip() else {}


def whoami() -> None:
    who = api("GET", "/user")
    me["account_id"] = who.get("account_id", "")
    me["nickname"] = who.get("nickname", "")
    me["display"] = who.get("display_name") or who.get("nickname", "")


def identity() -> Identity:
    # A mention in Bitbucket Cloud can be written at the nickname, at the display
    # name, or — as the editor inserts it — at the account id in braces.
    return Identity(
        name=me["nickname"], display=me["display"], alias=ALIAS,
        mentions=tuple(f"@{{{i}}}" for i in (me["account_id"],) if i),
    )


# ---- reading -----------------------------------------------------------

def issue_key(repo: str, ident) -> str:
    """`acme/api!7` — a pull request, in the same notation every tracker uses, so
    a key reads the same everywhere. Bitbucket Cloud has nothing else to address:
    its issue tracker is gone from the API."""
    return f"{repo}!{ident}"


def split_key(key: str) -> tuple[str, str]:
    repo, sep, ident = key.rpartition("!")
    if sep and repo and ident:
        return repo, ident
    raise RuntimeError(say(f"{key} — не адрес pull request в Bitbucket (нужно владелец/репозиторий!номер)",
                           f"{key} is not a Bitbucket pull request address (owner/repository!number is needed)"))


def text_of(comment: dict) -> str:
    """The comment as a person wrote it. `content` carries both the raw text and
    Bitbucket's rendered HTML; the raw one is what a model should read."""
    return str((comment.get("content") or {}).get("raw", "") or "")


def author_of(comment: dict) -> dict:
    return comment.get("user") or {}


def body_of(item: dict) -> str:
    """A pull request describes itself in a plain string."""
    return str(item.get("description") or "")


def event_from_comment(repo: str, ident, title: str, branch: str,
                       comment: dict, since: str | None) -> Event | None:
    """One comment, turned into a request — or into nothing at all.

    Every path that produces an event goes through here, which is why the scope
    check lives here rather than in the query (#282).
    """
    if not scope.allows(repo):
        return None
    if comment.get("deleted"):
        return None
    author = author_of(comment)
    if identity().is_me(author.get("nickname"), author.get("display_name"),
                        author.get("account_id")):
        return None  # the bot's own replies are not requests
    at = str(comment.get("created_on", ""))
    if not after(at, since):
        return None
    cmd = parse_command(text_of(comment), identity())
    if not cmd:
        return None
    key = issue_key(repo, ident)
    extra = source(key, trigger="mention", context="pull", repo=repo, ref=branch)
    return Event(
        id=event_id("comment", comment.get("id"), at), at=at, kind="comment",
        title=title, body=cmd.instruction,
        author=author.get("display_name") or author.get("nickname", ""),
        # The nickname is the one name a person types; Bitbucket Cloud shows no
        # e-mail of anyone, so the "who may ask" rules match on the login alone (#476).
        author_login=author.get("nickname", ""),
        url=f"https://bitbucket.org/{repo}/pull-requests/{ident}", action=cmd.action, extra=extra,
    )


# ---- writing -----------------------------------------------------------

def comment_body(text: str, link: dict | None, attachment_note: str) -> str:
    """What the reply says. Bitbucket Cloud has no comment attachments, so a
    document is named by its link — promising a file nobody can open is worse
    than saying where it is."""
    parts = [text.strip()] if text and text.strip() else []
    if link and link.get("url"):
        parts.append(f"[{link.get('title') or say('Открыть в Buff', 'Open in Buff')}]({link['url']})")
    if attachment_note:
        parts.append(attachment_note)
    return "\n\n".join(parts)


# ---- plugin surface ----------------------------------------------------

@plugin.check
def check():
    whoami()
    # Every repository in the scope is reached, so the check fails where a
    # customer's list names something this account cannot see.
    for repo in REPOS:
        api("GET", f"/repositories/{repo}")
    n = len(scope.allowed)
    who = me['display'] or me['nickname']
    return {"ok": True, "detail": say(
        f"Bitbucket Cloud, вошли как {who}. Бот работает в {n} {plural(n, 'репозитории', 'репозиториях', 'репозиториях')}.",
        f"Bitbucket Cloud, signed in as {who}. The bot works in {n} {plural_en(n, 'repository', 'repositories')}.")}


@plugin.poll
def poll(since):
    if not me["account_id"]:
        whoami()
    events: list[Event] = []
    for repo in REPOS:
        pulls = api("GET", f"/repositories/{repo}/pullrequests?state=OPEN&state=MERGED&pagelen=25")
        for pull in pulls.get("values", []):
            branch = ((pull.get("source") or {}).get("branch") or {}).get("name", "")
            comments = api("GET", f"/repositories/{repo}/pullrequests/{pull['id']}"
                                  f"/comments?pagelen=50&sort=-created_on")
            for c in comments.get("values", []):
                e = event_from_comment(repo, pull["id"], pull.get("title", ""), branch, c, since)
                if e:
                    events.append(e)
    return events


@plugin.webhook
def webhook(headers, body):
    """Bitbucket's callback carries the comment and what it hangs on, including
    the pull request's branch — so a request becomes an event without going back
    to the API at all."""
    try:
        payload = json.loads(body or "{}")
    except ValueError:
        return []
    comment = payload.get("comment")
    repo = ((payload.get("repository") or {}).get("full_name") or "").strip()
    if not comment or not repo:
        return []
    if not me["account_id"]:
        whoami()
    pull = payload.get("pullrequest")
    if not pull:
        return []
    branch = ((pull.get("source") or {}).get("branch") or {}).get("name", "")
    e = event_from_comment(repo, pull.get("id"), pull.get("title", ""), branch, comment, None)
    return [e] if e else []


@plugin.reply
def reply(issue: str = "", text: str = "", link: dict | None = None,
          attachment: dict | None = None, **_):
    repo, ident = split_key(issue)
    # The marked repositories bound writes too, not only reads.
    scope.require(repo)
    note = (say("Документ приложен в обсуждении в Buff — Bitbucket не принимает файлы в комментарии.",
                "The document is attached in the discussion in Buff — Bitbucket does not accept files in comments.")
            if attachment else "")
    body = comment_body(text, link, note)
    if not body:
        return {"ok": True}
    api("POST", f"/repositories/{repo}/pullrequests/{ident}/comments",
        {"content": {"raw": body}})
    return {"ok": True}


@plugin.tool(read_only=True,
             schema={"type": "object",
                     "properties": {"query": {"type": "string"}, "limit": {"type": "integer"}},
                     "required": ["query"], "additionalProperties": False})
def search_pulls(query: str = "", limit: int = 20):
    found = []
    needle = (query or "").lower()
    for repo in REPOS:
        data = api("GET", f"/repositories/{repo}/pullrequests?state=OPEN&state=MERGED"
                          f"&pagelen={limit}")
        for p in data.get("values", []):
            haystack = f"{p.get('title', '')} {p.get('description', '')}".lower()
            if needle and needle not in haystack:
                continue
            found.append({"key": issue_key(repo, p.get("id")), "title": p.get("title"),
                          "state": p.get("state"),
                          "branch": ((p.get("source") or {}).get("branch") or {}).get("name")})
    return {"total": len(found), "pulls": found}


@plugin.tool(read_only=True,
             schema={"type": "object", "properties": {"key": {"type": "string"}},
                     "required": ["key"], "additionalProperties": False})
def get_pull(key: str = ""):
    repo, ident = split_key(key)
    scope.require(repo)
    item = api("GET", f"/repositories/{repo}/pullrequests/{ident}")
    comments = api("GET", f"/repositories/{repo}/pullrequests/{ident}/comments?pagelen=100")
    return {
        "key": key,
        "title": item.get("title"),
        "state": item.get("state"),
        "body": body_of(item),
        "branch": ((item.get("source") or {}).get("branch") or {}).get("name"),
        "comments": [{"author": (author_of(c).get("display_name")),
                      "created": c.get("created_on"), "body": text_of(c)}
                     for c in comments.get("values", []) if not c.get("deleted")],
    }


@plugin.tool(read_only=True,
             schema={"type": "object", "properties": {"key": {"type": "string"}},
                     "required": ["key"], "additionalProperties": False})
def get_pull_diff(key: str = ""):
    repo, kind, ident = split_key(key)
    scope.require(repo)
    if kind != "pull":
        raise RuntimeError(say(f"{key} — не pull request", f"{key} is not a pull request"))
    res = plugin.http("GET", f"{BASE}/repositories/{repo}/pullrequests/{ident}/diff",
                      credential="api", headers={}, body="")
    if res["status"] >= 400:
        raise RuntimeError(say(f"Bitbucket ответил {res['status']} на diff {key}", f"Bitbucket answered {res['status']} to the diff of {key}"))
    return {"key": key, "diff": res["body"]}


@plugin.tool(read_only=False,
             schema={"type": "object",
                     "properties": {"key": {"type": "string"}, "text": {"type": "string"}},
                     "required": ["key", "text"], "additionalProperties": False})
def add_comment(key: str = "", text: str = ""):
    reply(issue=key, text=text)
    return {"ok": True}


if __name__ == "__main__":
    plugin.run()
