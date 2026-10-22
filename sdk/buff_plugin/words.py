"""Words a plugin says to a person (cloud-front#478).

A check that reads «из 1 проектов» or «корней в области: 1» reads like a form; the
words agree with the number instead, the way a person would say it.
"""


def plural(n: int, one: str, few: str, many: str) -> str:
    """The form of a word that agrees with `n`: 1 проект, 2 проекта, 5 проектов."""
    n = abs(int(n))
    if n % 10 == 1 and n % 100 != 11:
        return one
    if 2 <= n % 10 <= 4 and not 12 <= n % 100 <= 14:
        return few
    return many


def visible(seen: int, n: int, *, one: str, many: str, many_of: str, fix: str) -> str:
    """How much of what the operator listed the bot can reach, as a sentence: «Бот видит
    2 из 3 указанных папок.» — and, when not all of it, what to do (`fix`)."""
    if n == 1:
        return f"Бот видит {one}." if seen else f"Бот не видит {one}. {fix}"
    if seen >= n:
        return f"Бот видит все {many} ({n})."
    if seen == 0:
        return f"Бот не видит ни одной из {many_of} ({n}). {fix}"
    return f"Бот видит {seen} из {n} {many_of}. {fix}"
