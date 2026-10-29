"""Bitbucket Data Center / Server integration.

Bitbucket DC has NO issue tracker of its own. Teams there keep their tickets in
something else and use Bitbucket for code, so this integration lives where the
work is: comments on pull requests. The marketplace page says so in its first
line — a customer arriving for an issue tracker should not have to install
anything to find out.

Which means the branch context (#287) is not a nicety here but the whole point:
a question under a pull request is answered on that pull request's branch.

Comments on a COMMIT are not read. Data Center offers no repository-wide feed of
them, so polling would mean walking commits, and the callback shapes we could
route are not ones we have been able to try against a live server — a guess here
would be an integration that silently answers in the wrong place. Writing to a
commit is supported (`reply` handles an `@sha` key), because the platform can
address one when a question came from somewhere that knows the commit.

Two shapes off a live server decide the rest:

1. Activities, not comments, are what a pull request hands back
   (`/pull-requests/{id}/activities`), and the comment is nested inside an
   activity of action COMMENTED.
2. A repository is addressed by PROJECT KEY and slug — `ACME/api` — and the
   project key is upper-case by convention but compared without case, because
   the operator types it by hand.
"""

from __future__ import annotations

import json
import re

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

plugin = Plugin("bitbucket-dc")

BASE = plugin.config.get("base_url", "").rstrip("/")
REPOS = plugin.config.get("repos", [])
ALIAS = plugin.config.get("mention_alias", "")

# The marked repositories ARE the access control — see Scope in the SDK.
scope = Scope(REPOS, fold_case=True)

me = {"name": "", "display": "", "slug": ""}


# ---- transport ---------------------------------------------------------

def api(method: str, path: str, body: dict | None = None):
    res = plugin.http(method, BASE + path, credential="api",
                      headers={"Content-Type": "application/json"} if body else {},
                      body=json.dumps(body) if body else "")
    if res["status"] >= 400:
        raise RuntimeError(say(f"Bitbucket ответил {res['status']} на {path}: {res['body'][:300]}",
                               f"Bitbucket answered {res['status']} to {path}: {res['body'][:300]}"))
    return json.loads(res["body"]) if res["body"].strip() else {}


def parts(repo: str) -> tuple[str, str]:
    """`ACME/api` → project key and repository slug."""
    project, _, slug = str(repo).partition("/")
    return project, slug


def whoami() -> None:
    """Who the token belongs to.

    Bitbucket has no `/myself`: it names the authenticated account in the
    `X-AUSERNAME` header of every answer instead. That is the only place it is
    said without already knowing it, so the check reads it there and then looks
    the account up by name for its display name."""
    res = plugin.http("GET", f"{BASE}/rest/api/1.0/application-properties",
                      credential="api", headers={}, body="")
    if res["status"] >= 400:
        raise RuntimeError(say(f"Bitbucket ответил {res['status']} на проверку связи",
                               f"Bitbucket answered {res['status']} to the connection check"))
    name = (res.get("headers") or {}).get("X-Ausername") or \
        (res.get("headers") or {}).get("X-AUSERNAME") or ""
    me["name"] = name
    me["display"] = name
    if not name:
        return
    found = api("GET", f"/rest/api/1.0/users?filter={name}&limit=5").get("values", [])
    for u in found:
        if u.get("name") == name:
            me["display"] = u.get("displayName") or name
            me["slug"] = u.get("slug", "")
            break


def identity() -> Identity:
    return Identity(name=me["name"], display=me["display"], alias=ALIAS)


# ---- reading -----------------------------------------------------------

def issue_key(repo: str, kind: str, ident) -> str:
    """`ACME/api!7` is a pull request, `ACME/api@<sha>` a commit — the same
    notation the GitLab plugin uses, so a key reads the same everywhere."""
    return f"{repo}{'!' if kind == 'pull' else '@'}{ident}"


def split_key(key: str) -> tuple[str, str, str]:
    m = re.fullmatch(r"(?P<repo>[^!@\s]+/[^!@\s]+)(?P<mark>[!@])(?P<ident>[^!@\s]+)",
                     str(key).strip())
    if not m:
        raise RuntimeError(say(f"{key} — не адрес обсуждения в Bitbucket (нужно ПРОЕКТ/репозиторий!номер)",
                               f"{key} is not a Bitbucket discussion address (PROJECT/repository!number is needed)"))
    return m.group("repo"), ("pull" if m.group("mark") == "!" else "commit"), m.group("ident")


def event_from_activity(repo: str, pull: dict, activity: dict, since: str | None) -> Event | None:
    """One activity on a pull request, turned into a request — or into nothing.

    Every path that produces an event goes through here, which is why the scope
    check lives here rather than in the query (#282).
    """
    if not scope.allows(repo):
        return None
    if activity.get("action") != "COMMENTED":
        return None
    comment = activity.get("comment") or {}
    author = comment.get("author") or {}
    if identity().is_me(author.get("name"), author.get("displayName")):
        return None  # the bot's own replies are not requests
    # Bitbucket counts in milliseconds since the epoch; the SDK compares moments,
    # so the number is turned into one before anything else looks at it.
    at = epoch_to_iso(comment.get("createdDate") or activity.get("createdDate"))
    if not after(at, since):
        return None
    cmd = parse_command(comment.get("text", "") or "", identity())
    if not cmd:
        return None
    ident = pull.get("id")
    if not ident:
        return None
    key = issue_key(repo, "pull", ident)
    branch = ((pull.get("fromRef") or {}).get("displayId")) or ""
    return Event(
        id=event_id("comment", comment.get("id"), at), at=at, kind="comment",
        title=pull.get("title", ""), body=cmd.instruction,
        author=author.get("displayName") or author.get("name", ""),
        # The account name, not the display name — the connection's "who may ask"
        # rules match on it, and on the address Bitbucket shows (#476).
        author_login=author.get("name") or author.get("slug", ""),
        author_email=str(author.get("emailAddress") or ""),
        url=f"{BASE}/projects/{parts(repo)[0]}/repos/{parts(repo)[1]}/pull-requests/{ident}",
        action=cmd.action,
        extra=source(key, trigger="mention", context="pull", repo=repo, ref=branch),
    )


def epoch_to_iso(ms) -> str:
    """Bitbucket's timestamps are milliseconds since the epoch. Everything else
    in the platform speaks ISO, and comparing the two silently decides wrong."""
    from datetime import datetime, timezone

    try:
        return datetime.fromtimestamp(int(ms) / 1000, tz=timezone.utc).isoformat().replace("+00:00", "Z")
    except (TypeError, ValueError):
        return ""


# ---- writing -----------------------------------------------------------

def comment_text(text: str, link: dict | None, attachment_note: str) -> str:
    """What the comment says. Bitbucket DC has no remote links and no comment
    attachments, so both belong in the text — otherwise the person who asked sees
    an answer mentioning a document nobody can find."""
    parts_ = [text.strip()] if text and text.strip() else []
    if link and link.get("url"):
        parts_.append(f"[{link.get('title') or say('Открыть в Buff', 'Open in Buff')}]({link['url']})")
    if attachment_note:
        parts_.append(attachment_note)
    return "\n\n".join(parts_)


# ---- plugin surface ----------------------------------------------------

@plugin.check
def check():
    version = api("GET", "/rest/api/1.0/application-properties").get("version", "?")
    whoami()
    n = len(scope.allowed)
    who = me['display'] or me['name'] or '?'
    return {"ok": True, "detail": say(
        f"Bitbucket {version}, вошли как {who}. Бот работает в {n} {plural(n, 'репозитории', 'репозиториях', 'репозиториях')}.",
        f"Bitbucket {version}, signed in as {who}. The bot works in {n} {plural_en(n, 'repository', 'repositories')}.")}


@plugin.poll
def poll(since):
    if not me["name"]:
        whoami()
    events: list[Event] = []
    for repo in REPOS:
        project, slug = parts(repo)
        pulls = api("GET", f"/rest/api/1.0/projects/{project}/repos/{slug}"
                           f"/pull-requests?state=ALL&limit=25&order=NEWEST")
        for pull in pulls.get("values", []):
            acts = api("GET", f"/rest/api/1.0/projects/{project}/repos/{slug}"
                              f"/pull-requests/{pull['id']}/activities?limit=50")
            for activity in acts.get("values", []):
                e = event_from_activity(repo, pull, activity, since)
                if e:
                    events.append(e)
    return events


@plugin.webhook
def webhook(headers, body):
    """Bitbucket's callback carries the comment and the pull request it hangs on,
    including the source branch — so a request becomes an event without going
    back to the API at all.

    Anything else — a push, a merge, an event we do not act on — produces
    nothing, and the poll picks up whatever a callback missed.
    """
    try:
        payload = json.loads(body or "{}")
    except ValueError:
        return []
    if payload.get("eventKey") not in ("pr:comment:added", None):
        return []
    comment = payload.get("comment")
    pull = payload.get("pullRequest") or {}
    from_ref = pull.get("fromRef") or {}
    repo_info = from_ref.get("repository") or {}
    project = ((repo_info.get("project") or {}).get("key") or "").strip()
    slug = (repo_info.get("slug") or "").strip()
    if not comment or not project or not slug:
        return []
    if not me["name"]:
        whoami()
    e = event_from_activity(f"{project}/{slug}", pull,
                            {"action": "COMMENTED", "comment": comment}, None)
    return [e] if e else []


@plugin.reply
def reply(issue: str = "", text: str = "", link: dict | None = None,
          attachment: dict | None = None, **_):
    repo, kind, ident = split_key(issue)
    # The marked repositories bound writes too, not only reads.
    scope.require(repo)
    project, slug = parts(repo)
    body = comment_text(text, link,
                        say(f"Документ: {attachment.get('filename')}", f"Document: {attachment.get('filename')}") if attachment else "")
    if not body:
        return {"ok": True}
    if kind == "commit":
        api("POST", f"/rest/api/1.0/projects/{project}/repos/{slug}/commits/{ident}/comments",
            {"text": body})
    else:
        api("POST", f"/rest/api/1.0/projects/{project}/repos/{slug}"
                    f"/pull-requests/{ident}/comments", {"text": body})
    return {"ok": True}


@plugin.tool(read_only=True,
             schema={"type": "object",
                     "properties": {"query": {"type": "string"}, "limit": {"type": "integer"}},
                     "required": ["query"], "additionalProperties": False})
def search_pulls(query: str = "", limit: int = 20):
    found = []
    needle = (query or "").lower()
    for repo in REPOS:
        project, slug = parts(repo)
        data = api("GET", f"/rest/api/1.0/projects/{project}/repos/{slug}"
                          f"/pull-requests?state=ALL&limit={limit}")
        for p in data.get("values", []):
            if needle and needle not in (p.get("title", "") + p.get("description", "")).lower():
                continue
            found.append({"key": issue_key(repo, "pull", p.get("id")), "title": p.get("title"),
                          "state": p.get("state"),
                          "branch": ((p.get("fromRef") or {}).get("displayId"))})
    return {"total": len(found), "pulls": found}


@plugin.tool(read_only=True,
             schema={"type": "object", "properties": {"key": {"type": "string"}},
                     "required": ["key"], "additionalProperties": False})
def get_pull(key: str = ""):
    repo, kind, ident = split_key(key)
    scope.require(repo)
    if kind != "pull":
        raise RuntimeError(say(f"{key} — не pull request", f"{key} is not a pull request"))
    project, slug = parts(repo)
    pull = api("GET", f"/rest/api/1.0/projects/{project}/repos/{slug}/pull-requests/{ident}")
    acts = api("GET", f"/rest/api/1.0/projects/{project}/repos/{slug}"
                      f"/pull-requests/{ident}/activities?limit=100")
    return {
        "key": key,
        "title": pull.get("title"),
        "state": pull.get("state"),
        "body": pull.get("description"),
        "branch": ((pull.get("fromRef") or {}).get("displayId")),
        "target": ((pull.get("toRef") or {}).get("displayId")),
        "comments": [{"author": ((a.get("comment") or {}).get("author") or {}).get("displayName"),
                      "created": epoch_to_iso((a.get("comment") or {}).get("createdDate")),
                      "body": (a.get("comment") or {}).get("text")}
                     for a in acts.get("values", []) if a.get("action") == "COMMENTED"],
    }


@plugin.tool(read_only=True,
             schema={"type": "object", "properties": {"key": {"type": "string"}},
                     "required": ["key"], "additionalProperties": False})
def get_pull_diff(key: str = ""):
    repo, kind, ident = split_key(key)
    scope.require(repo)
    project, slug = parts(repo)
    res = plugin.http("GET", f"{BASE}/rest/api/1.0/projects/{project}/repos/{slug}"
                             f"/pull-requests/{ident}.diff", credential="api", headers={}, body="")
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
