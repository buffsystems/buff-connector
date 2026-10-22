"""A PowerPoint 97–2003 presentation (ppt) as text, slide by slide.

The PowerPoint Document stream is a tree of records. Placeholder text (titles,
outlines) sits in the document's SlideListWithText, one SlidePersistAtom per
slide followed by its text atoms; text typed into free text boxes sits in each
slide's own container; speaker notes in the notes list. Slides come out in
list order, with the containers matched to them by position."""

from __future__ import annotations

import struct

from ole import Compound

_CONTAINERS = {0x03E8, 0x03EE, 0x03F0, 0x0FF0, 0x0FF5, 0x040C, 0xF000, 0xF001, 0xF002, 0xF003, 0xF004,
               0xF00D, 0x03F8, 0x0FF6, 0x0FF7, 0x0FF8, 0x0FF9, 0x0FFA, 0x0FFB, 0x0FFC, 0x0FFD, 0x0FFE}
_SLIDE_LIST, _SLIDE_PERSIST = 0x0FF0, 0x03F3
_TEXT_CHARS, _TEXT_BYTES, _TEXT_HEADER = 0x0FA0, 0x0FA8, 0x0F9F
_SLIDE, _NOTES, _DOCUMENT, _NOTES_ATOM = 0x03EE, 0x03F0, 0x03E8, 0x03F1


def text_of(data: bytes) -> str:
    stream = Compound(data).stream("PowerPoint Document")
    outline: list[tuple[int, list[str]]] = []   # (slide id, texts) from the slide list
    notes_list: list[list[str]] = []
    slides: list[list[str]] = []    # per slide container, in stream order
    notes: dict[int, list[str]] = {}  # by the slide the notes belong to; the master (0) is skipped
    for rec_type, instance, body in _records(stream, 0, len(stream)):
        if rec_type == _DOCUMENT:
            for t2, inst2, b2 in _records(stream, body[0], body[1]):
                if t2 == _SLIDE_LIST:
                    groups = _slide_list(stream, b2)
                    if inst2 == 0:
                        outline = groups
                    elif inst2 == 2:
                        notes_list = [g for _, g in groups]
        elif rec_type == _SLIDE:
            slides.append(_texts(stream, body))
        elif rec_type == _NOTES:
            ref = _notes_ref(stream, body)
            if ref:
                notes[ref] = _texts(stream, body)
    count = max(len(outline), len(slides))
    out: list[str] = []
    for n in range(count):
        slide_id, texts = outline[n] if n < len(outline) else (0, [])
        parts = texts + (slides[n] if n < len(slides) else [])
        out += [f"## Слайд {n + 1}", ""]
        out += [p for p in parts if p.strip()] + [""]
        note = " ".join((notes_list[n] if n < len(notes_list) else []) + notes.get(slide_id, []))
        note = " ".join(w for w in note.split() if w)
        if note:
            out += [f"Заметки: {note}", ""]
    return "\n".join(out) + "\n"


def _notes_ref(stream: bytes, body: tuple[int, int]) -> int:
    """The slide a notes container belongs to — 0 for the notes master."""
    for rec_type, _, (s, e) in _records(stream, body[0], body[1]):
        if rec_type == _NOTES_ATOM:
            return struct.unpack_from("<I", stream, s)[0]
    return 0


def _records(stream: bytes, start: int, end: int):
    """(type, instance, (body start, body end)) for the records between
    start and end."""
    i = start
    while i + 8 <= end:
        ver_inst, rec_type, length = struct.unpack_from("<HHI", stream, i)
        yield rec_type, ver_inst >> 4, (i + 8, min(i + 8 + length, end))
        i += 8 + length


def _texts(stream: bytes, body: tuple[int, int]) -> list[str]:
    """Every text atom under a container, depth first, in order."""
    out: list[str] = []
    for rec_type, _, (s, e) in _records(stream, body[0], body[1]):
        if rec_type == _TEXT_CHARS:
            out.append(_clean(stream[s:e].decode("utf-16-le", "replace")))
        elif rec_type == _TEXT_BYTES:
            out.append(_clean(stream[s:e].decode("latin-1")))
        elif rec_type in _CONTAINERS or _looks_like_container(stream, s, e):
            out += _texts(stream, (s, e))
    return out


def _looks_like_container(stream: bytes, s: int, e: int) -> bool:
    # A record whose version nibble is 0xF is a container by definition (MS-PPT
    # 2.3.1); the header sits 8 bytes before its body.
    return (stream[s - 8] & 0x0F) == 0x0F if s >= 8 else False


def _slide_list(stream: bytes, body: tuple[int, int]) -> list[tuple[int, list[str]]]:
    groups: list[tuple[int, list[str]]] = []
    for rec_type, _, (s, e) in _records(stream, body[0], body[1]):
        if rec_type == _SLIDE_PERSIST:
            groups.append((struct.unpack_from("<I", stream, s + 12)[0], []))
        elif rec_type == _TEXT_CHARS and groups:
            groups[-1][1].append(_clean(stream[s:e].decode("utf-16-le", "replace")))
        elif rec_type == _TEXT_BYTES and groups:
            groups[-1][1].append(_clean(stream[s:e].decode("latin-1")))
    return groups


def _clean(text: str) -> str:
    return text.replace("\r", "\n").replace("\x0b", "\n").replace("\x00", "").strip()
