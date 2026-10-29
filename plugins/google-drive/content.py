"""Google Drive's shapes, turned into what the model and the platform read.

Everything here is pure: ids out of links, a MIME type into a kind a person
recognises, sheet rows into a markdown table, a document into the multipart
body Drive imports. Kept apart from main.py so it can be tested against the
documented shapes without a plugin process.
"""

from __future__ import annotations

import json
import re
import secrets
from datetime import timedelta, timezone

from buff_plugin import english, moment, say

# A Drive id is a run of letters, digits, `-` and `_`. In a link it follows
# `/folders/`, `/d/` or `/file/d/`, or sits in `?id=` (older sharing links).
_ID = r"[A-Za-z0-9_-]{10,}"
_IN_PATH = re.compile(rf"/(?:folders|d|file/d)/({_ID})")
_BARE = re.compile(rf"^{_ID}$")

DOC = "application/vnd.google-apps.document"
SHEET = "application/vnd.google-apps.spreadsheet"
SLIDES = "application/vnd.google-apps.presentation"
FOLDER = "application/vnd.google-apps.folder"
SHORTCUT = "application/vnd.google-apps.shortcut"

# The kinds whose comments carry requests: what people write on.
COMMENTABLE = (DOC, SHEET, SLIDES)

KINDS = {DOC: "документ", SHEET: "таблица", SLIDES: "презентация", FOLDER: "папка",
         SHORTCUT: "ярлык", "application/pdf": "PDF"}
KINDS_EN = {DOC: "document", SHEET: "spreadsheet", SLIDES: "presentation", FOLDER: "folder",
            SHORTCUT: "shortcut", "application/pdf": "PDF"}

# What a person reads of each Google type, and the export that gives it.
EXPORT = {DOC: "text/markdown", SLIDES: "text/plain"}

# Files whose bytes ARE text and are read as such.
TEXT_TYPES = ("text/", "application/json", "application/xml", "application/x-yaml",
              "application/yaml", "application/javascript")


def bare_id(value) -> str:
    """The id of a file or folder — from an id, or from any Drive, Docs, Sheets
    or Slides link."""
    text = str(value or "").strip()
    if not text:
        raise ValueError(say("пустая ссылка или идентификатор Google Диска", "an empty Google Drive link or id"))
    if _BARE.match(text):
        return text
    m = _IN_PATH.search(text)
    if m:
        return m.group(1)
    _, _, query = text.partition("?")
    for pair in query.split("&"):
        key, _, val = pair.partition("=")
        if key == "id" and _BARE.match(val):
            return val
    raise ValueError(say(f"{text} — не ссылка и не идентификатор Google Диска",
                         f"{text} is neither a Google Drive link nor an id"))


def kind_of(mime: str) -> str:
    """The kind a person recognises. Anything not a Google type is named by
    its top-level MIME family, so «image» and «video» read as such."""
    mime = str(mime or "")
    if mime in KINDS:
        return KINDS_EN[mime] if english() else KINDS[mime]
    family = mime.split("/")[0]
    if english():
        return family if family in ("image", "video", "audio", "text") else "file"
    return {"image": "изображение", "video": "видео", "audio": "аудио", "text": "текст"}.get(family, "файл")


def is_text(mime: str) -> bool:
    return any(str(mime or "").startswith(t) for t in TEXT_TYPES)


def q_string(value: str) -> str:
    """A value inside a `q` query: single-quoted, with the two characters that
    would end or break the quote escaped."""
    return "'" + str(value or "").replace("\\", "\\\\").replace("'", "\\'") + "'"


def commentable_q() -> str:
    return "(" + " or ".join(f"mimeType = '{m}'" for m in COMMENTABLE) + ")"


# Drive's `fullText contains 'a b'` wants EVERY word, as Drive tokenises it, and
# knows nothing of Russian inflection: «миграций» and «миграции» are two words
# to it, and a question rarely uses the form the document does. So the search
# asks for ANY of the question's words — in the text or in the name — and the
# plugin ranks what came back by reading it: crudely stemmed, so the forms meet.

def words_of(query: str, min_len: int = 4) -> list[str]:
    """The words worth looking for: long enough to mean something on their own."""
    out = []
    for w in re.split(r"[^\w]+", str(query or "").lower()):
        if len(w) >= min_len and w not in out:
            out.append(w)
    return out


def stem(word: str) -> str:
    """A word without its last letters — enough to survive an inflection, not so
    much that unrelated words collide."""
    return word[: max(4, len(word) - 2)]


def search_q(words: list[str]) -> str:
    """`(fullText contains 'a' or name contains 'a' or …) and trashed = false`."""
    if not words:
        return "trashed = false"
    parts = [f"fullText contains {q_string(w)} or name contains {q_string(w)}" for w in words]
    return "(" + " or ".join(parts) + ") and trashed = false"


def match_score(text: str, words: list[str]) -> int:
    """How many of the words (stemmed) the text carries."""
    hay = str(text or "").lower()
    return sum(1 for w in words if stem(w) in hay)


def excerpt(text: str, words: list[str], width: int = 240) -> str:
    """The passage around the first matching word, on one line."""
    flat = re.sub(r"\s+", " ", str(text or ""))
    low = flat.lower()
    at = -1
    for w in words:
        at = low.find(stem(w))
        if at >= 0:
            break
    if at < 0:
        return flat[:width]
    start = max(0, at - width // 3)
    piece = flat[start:start + width]
    return ("…" if start > 0 else "") + piece + ("…" if start + width < len(flat) else "")


def thread_key(file_id: str, comment: str) -> str:
    """`FILE#COMMENT` — the comment IS the thread: its replies are the talk,
    and the answer goes back as one of them."""
    return f"{file_id}#{comment}" if comment else file_id


def split_key(key: str) -> tuple[str, str]:
    file_id, _, comment = str(key or "").partition("#")
    try:
        return bare_id(file_id), comment.strip()
    except ValueError as exc:
        raise RuntimeError(say(f"{key} — не адрес файла Google Диска (нужно ФАЙЛ или ФАЙЛ#комментарий)",
                               f"{key} is not a Google Drive file address (FILE or FILE#comment is needed)")) from exc


def comment_url(view_link: str, comment: str) -> str:
    """Where the discussion opens: Docs, Sheets and Slides all take `disco=`."""
    if not comment or not view_link:
        return view_link or ""
    sep = "&" if "?" in view_link else "?"
    return f"{view_link}{sep}disco={comment}"


def sheet_markdown(title: str, rows: list[list]) -> str:
    """One sheet as a markdown table: the first row is the header. Nothing is
    cut — a sheet of a thousand rows is a thousand rows."""
    if not rows:
        return f"## {title}\n\n{say('(пусто)', '(empty)')}\n"
    width = max(len(r) for r in rows)
    cell = lambda v: str(v if v is not None else "").replace("|", "\\|").replace("\n", " ")  # noqa: E731
    pad = lambda r: [cell(v) for v in list(r) + [""] * (width - len(r))]  # noqa: E731
    head = pad(rows[0])
    lines = [f"## {title}", "", "| " + " | ".join(head) + " |", "|" + "---|" * width]
    lines.extend("| " + " | ".join(pad(r)) + " |" for r in rows[1:])
    return "\n".join(lines) + "\n"


def multipart_related(meta: dict, media_type: str, media: bytes) -> tuple[str, bytes]:
    """The body Drive's `uploadType=multipart` takes: metadata JSON, then the
    bytes. Returns the Content-Type (with the boundary) and the body."""
    boundary = "buff" + secrets.token_hex(12)
    body = (
        f"--{boundary}\r\nContent-Type: application/json; charset=UTF-8\r\n\r\n"
        f"{json.dumps(meta, ensure_ascii=False)}\r\n"
        f"--{boundary}\r\nContent-Type: {media_type}\r\n\r\n"
    ).encode() + media + f"\r\n--{boundary}--\r\n".encode()
    return f"multipart/related; boundary={boundary}", body


def a_minute_before(at: str) -> str:
    """The moment a minute earlier, in Drive's own spelling — a window widened
    so nothing is missed at the edge; ids keep what was already reported apart."""
    m = moment(at)
    if m is None:
        return ""
    if m.utcoffset() is None:
        m = m.replace(tzinfo=timezone.utc)
    return (m.astimezone(timezone.utc) - timedelta(minutes=1)).strftime("%Y-%m-%dT%H:%M:%S.000Z")


def author_text(author: dict | None) -> str:
    a = author or {}
    name = str(a.get("displayName") or "")
    email = str(a.get("emailAddress") or "")
    if name and email and name != email:
        return f"{name} ({email})"
    return name or email
