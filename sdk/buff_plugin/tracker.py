"""The parts every tracker plugin repeats (#288).

A tracker plugin is mostly its own API dialect. What is NOT its own is how a
request is recognised, how the bot knows it is being addressed, what counts as
already seen, and where it is allowed to act at all. Those were written once for
Jira (#156) and each of them cost a live run against a real tracker to get
right; copying them into every new plugin would mean re-earning the same
lessons, and fixing one copy would leave the others wrong.

So they live here, in the SDK that is bundled with every plugin, and a plugin
supplies only what is genuinely tracker-specific: how it spells a mention, what
it calls a project, and how it reads its own API.
"""

from __future__ import annotations

import re
from dataclasses import dataclass, field
from datetime import datetime, timezone

from .words import say

# The work a request can ask for. `ask` is the default because a person who
# writes to the bot without naming a mode wants an answer, not a ceremony.
ACTIONS = ("ask", "research", "task")
DEFAULT_ACTION = "ask"

# What a source can be. A question asked in a pull request has to be answered on
# the code of THAT pull request, so the context travels with the event.
CONTEXTS = ("issue", "pull", "commit")

# Characters that can continue a handle. `@buff` inside `@buff-bot` is somebody
# else, and `/asking` is not a command.
_HANDLE = r"[^\w.\-]"


@dataclass(frozen=True)
class Identity:
    """Who the bot is in one tracker.

    Trackers identify the same account by different things depending on where
    you look: Jira's changelog has an internal key and a display name but no
    login, while a comment's author has the login and not the key. The bot
    therefore answers to any of its names rather than to one canonical one.

    `mentions` holds spellings only this tracker uses — Jira's `[~account]`, for
    instance, which is what its autocomplete inserts.
    """

    name: str = ""
    key: str = ""
    display: str = ""
    alias: str = ""
    mentions: tuple[str, ...] = field(default_factory=tuple)

    def is_me(self, *values: str | None) -> bool:
        known = {v.lower() for v in (self.name, self.key, self.display) if v}
        return any(v and str(v).lower() in known for v in values)

    def handles(self) -> list[str]:
        """Every way this bot can be addressed, longest first so that a longer
        handle is never mistaken for a shorter one plus text."""
        out = [f"@{n}" for n in (self.name, self.alias) if n]
        out.extend(m for m in self.mentions if m)
        return sorted(set(out), key=len, reverse=True)

    def signed_in(self) -> str:
        """Who a connection check says it logged in as.

        A display name is what a person recognises; a login is what access was
        granted to and what a mention is written at. When they differ, both are
        named — an operator comparing the check against the account they created
        needs the one they typed.
        """
        if self.display and self.name and self.display != self.name:
            return f"{self.display} ({self.name})"
        return self.display or self.name or self.key

    def slash_names(self) -> list[str]:
        return [n for n in (self.name, self.alias) if n]


@dataclass(frozen=True)
class Command:
    """A request addressed to the bot: what to do, and what was asked."""

    action: str
    instruction: str


def parse_command(text: str, identity: Identity) -> Command | None:
    """Reads a comment and returns the request in it, or None if there is none.

    Two spellings are understood, because two are in use. `@bot research …` is
    how Jira and Redmine users address anyone; `/research …` is the language of
    GitHub, GitLab and Gitea. Both are a DIRECT address — a person typed them at
    the bot — which is the only thing that starts work (#282).
    """
    text = text or ""
    return _from_mention(text, identity) or _from_slash(text, identity)


def _from_mention(text: str, identity: Identity) -> Command | None:
    for token in identity.handles():
        idx = _find_handle(text, token)
        if idx < 0:
            continue
        before = text[:idx].strip()
        after = text[idx + len(token):].strip()
        action, rest = _split_action(after)
        # Text on BOTH sides is kept: people write the context first and the
        # instruction after, and dropping either half loses the question.
        instruction = " ".join(p for p in (before, rest) if p).strip()
        return Command(action, instruction) if instruction else None
    return None


def _from_slash(text: str, identity: Identity) -> Command | None:
    names = "|".join(re.escape(n) for n in identity.slash_names())
    actions = "|".join(ACTIONS)
    scoped = rf"(?:(?:{names})\s+)?" if names else ""
    # Case-insensitive: `/Research` and `/RESEARCH` are the same request, and a
    # phone's keyboard capitalises the first word of a line by itself.
    pattern = re.compile(rf"^[ \t]*/{scoped}({actions})(?![\w.\-])[ \t]*",
                         re.MULTILINE | re.IGNORECASE)
    m = pattern.search(text)
    if not m:
        return None
    rest = (text[: m.start()] + text[m.end():]).strip()
    lines = [ln.strip() for ln in rest.splitlines()]
    instruction = "\n".join(ln for ln in lines if ln)
    # The action is normalised: the platform routes on these three words.
    return Command(m.group(1).lower(), instruction) if instruction else None


def _find_handle(text: str, token: str) -> int:
    """Index of `token` used as a whole handle, or -1.

    Case is ignored: people write «@Buff», a tracker's autocomplete inserts the
    name as the account spells it, and a mention that does not match is not an
    error anybody sees — it is simply a question that never arrives.

    A bare `str.find` reports `@buff` inside `@buffalo`, which is a different
    person and a request nobody made, so the boundary is checked either way.
    """
    haystack, token = text.lower(), token.lower()
    start = 0
    while True:
        idx = haystack.find(token, start)
        if idx < 0:
            return -1
        tail = haystack[idx + len(token): idx + len(token) + 1]
        if not tail or re.match(_HANDLE, tail):
            return idx
        start = idx + len(token)


def _split_action(after: str) -> tuple[str, str]:
    first, _, rest = after.partition(" ")
    word = first.lower().strip(":,.")
    if word in ACTIONS:
        return word, rest.strip()
    return DEFAULT_ACTION, after


class Scope:
    """Where a connection may act.

    The list IS the access control, so the check belongs in the function that
    produces every event rather than in the query that fetches them: the poller's
    filter looked sufficient until a webhook started producing events too, and a
    tracker's callbacks are configured globally (#282). An empty list means
    NOWHERE — an unconfigured connection must not answer across a whole tracker.

    Writes are bound by the same list: the connector is the customer's trust
    boundary in both directions.
    """

    def __init__(self, allowed, *, fold_case: bool = False) -> None:
        self.fold_case = fold_case
        self.allowed = [self._fold(a) for a in (allowed or []) if a]

    def _fold(self, value) -> str:
        text = str(value or "").strip()
        return text.lower() if self.fold_case else text

    def allows(self, value) -> bool:
        return bool(self.allowed) and self._fold(value) in self.allowed

    def require(self, value) -> None:
        if not self.allows(value):
            raise RuntimeError(say(f"{value} вне области, разрешённой этому подключению",
                                   f"{value} is outside the area this connection is allowed"))


def moment(value: str | None) -> datetime | None:
    """A tracker's timestamp as an instant, or None if it cannot be read.

    Trackers spell the same instant differently — `…+0000` against `…Z` — so
    comparing them as STRINGS silently decides the wrong way. Everything that
    filters by time goes through here.
    """
    if not value:
        return None
    text = str(value).strip().replace("Z", "+00:00")
    # Some trackers write the offset without a colon; fromisoformat wants one.
    if len(text) >= 5 and text[-5] in "+-" and text[-3] != ":":
        text = text[:-2] + ":" + text[-2:]
    try:
        return datetime.fromisoformat(text)
    except ValueError:
        return None


def after(at: str, since: str | None) -> bool:
    """True when `at` happened after the watermark. An unreadable timestamp
    counts as new: acting twice on a request is recoverable, losing it is not."""
    if not since:
        return True
    a, b = moment(at), moment(since)
    if a is None or b is None:
        return True
    return a > b


def at_second(value: str | None) -> str:
    """A watermark as whole seconds in UTC — `2026-09-04T19:36:42Z`.

    The platform's watermark carries fractional seconds. Some trackers refuse a
    timestamp that has them outright (Redmine answers `422 Updated is invalid`,
    and the poll then fails on every pass while the integration looks healthy),
    and the rest accept it by luck rather than by contract: whole seconds in UTC
    is the spelling they all document. Rounding DOWN is deliberate — the query
    window only widens, and which events are actually new is still decided
    exactly, by `after`.
    """
    at = moment(value)
    if at is None:
        return ""
    return at.astimezone(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def event_id(kind: str, ident, at: str) -> str:
    """The identifier the platform de-duplicates on.

    It carries the moment as well as the tracker's own id, because a recreated
    tracker numbers its comments from one again and real events would then be
    swallowed as repeats (#156).
    """
    return f"{kind}:{ident}@{at}"


def source(issue: str, *, trigger: str = "mention", context: str = "issue",
           repo: str = "", ref: str = "") -> dict:
    """The `extra` an event carries: what it is about and where that lives.

    `repo` and `ref` are what let a question asked in a pull request be answered
    on the code of that pull request instead of on the default branch (#287).
    """
    if context not in CONTEXTS:
        raise ValueError(f"unknown context {context!r}")
    out = {"issue": issue, "trigger": trigger, "context": context}
    if repo:
        out["repo"] = repo
    if ref:
        out["ref"] = ref
    return out
