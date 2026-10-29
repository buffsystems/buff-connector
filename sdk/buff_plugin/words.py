"""Words a plugin says to a person (cloud-front#478).

A check that reads «из 1 проектов» or «корней в области: 1» reads like a form; the
words agree with the number instead, the way a person would say it.

They are said in the reader's language (#565): a tool call's own when the
connector names it (the model working in English reads its answers and refusals
in English), else the connector's, which it passes as BUFF_LANG; anything but
English is Russian.
"""

import contextvars
import os

# The language of the tool call being served: set by the protocol loop for the
# length of one call, empty outside one.
_call_lang: contextvars.ContextVar[str] = contextvars.ContextVar("buff_call_lang", default="")


def _norm(lang: str) -> str:
    return "en" if (lang or "").strip().lower()[:2] == "en" else "ru"


def language() -> str:
    """"en" or "ru": the call's language, else the connector's (BUFF_LANG)."""
    return _norm(_call_lang.get() or os.environ.get("BUFF_LANG", ""))


def english() -> bool:
    """Whether whoever reads what the plugin says now — a model in a call, a
    person at a connection check — reads English."""
    return language() == "en"


def say(ru: str, en: str) -> str:
    """The line in the connector's language."""
    return en if english() else ru


def plural_en(n: int, one: str, many: str) -> str:
    """English agreement: one project, 0 or 2 projects."""
    return one if abs(int(n)) == 1 else many


def plural(n: int, one: str, few: str, many: str) -> str:
    """The form of a word that agrees with `n`: 1 проект, 2 проекта, 5 проектов."""
    n = abs(int(n))
    if n % 10 == 1 and n % 100 != 11:
        return one
    if 2 <= n % 10 <= 4 and not 12 <= n % 100 <= 14:
        return few
    return many


def visible(seen: int, n: int, *, one: str, many: str, many_of: str, fix: str, en: dict | None = None) -> str:
    """How much of what the operator listed the bot can reach, as a sentence: «Бот видит
    2 из 3 указанных папок.» — and, when not all of it, what to do (`fix`).

    `en` is the same in English — one ("the listed folder"), many ("listed folders"),
    fix — used when the connector speaks English."""
    if en and english():
        return _visible_en(seen, n, **en)
    if n == 1:
        return f"Бот видит {one}." if seen else f"Бот не видит {one}. {fix}"
    if seen >= n:
        return f"Бот видит все {many} ({n})."
    if seen == 0:
        return f"Бот не видит ни одной из {many_of} ({n}). {fix}"
    return f"Бот видит {seen} из {n} {many_of}. {fix}"


def _visible_en(seen: int, n: int, *, one: str, many: str, fix: str) -> str:
    if n == 1:
        return f"The bot sees {one}." if seen else f"The bot does not see {one}. {fix}"
    if seen >= n:
        return f"The bot sees all the {many} ({n})."
    if seen == 0:
        return f"The bot sees none of the {many} ({n}). {fix}"
    return f"The bot sees {seen} of {n} {many}. {fix}"
