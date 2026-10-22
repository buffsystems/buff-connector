"""A Word 97–2003 document (doc) as text.

Word stores the text in PIECES (MS-DOC's piece table, the Clx in the table
stream): each piece says where its characters sit in the WordDocument stream
and whether they are 8-bit or UTF-16. The main document's characters come
first; what follows (footnotes, headers) is not part of the body a person
reads and is left out. Control characters carry structure — paragraph and
cell ends, field codes — and are turned into text or dropped here."""

from __future__ import annotations

import re
import struct

from ole import Compound


def text_of(data: bytes) -> str:
    c = Compound(data)
    word = c.stream("WordDocument")
    if word[:2] != b"\xec\xa5":
        raise ValueError("поток WordDocument не начинается с FIB — файл повреждён")
    flags = struct.unpack_from("<H", word, 0x0A)[0]
    table = c.stream("1Table" if flags & 0x0200 else "0Table")
    if flags & 0x0100:
        raise ValueError("документ зашифрован — прочитать нельзя")
    ccp_text = struct.unpack_from("<I", word, 0x4C)[0]
    fc_clx, lcb_clx = struct.unpack_from("<II", word, 0x1A2)
    chars = _pieces(word, table[fc_clx:fc_clx + lcb_clx])[:ccp_text]
    return _clean(chars)


def _pieces(word: bytes, clx: bytes) -> str:
    """The characters, piece by piece, from the Clx — skipping the property
    modifiers (clxt 1) that may precede the piece table (clxt 2)."""
    i = 0
    while i < len(clx) and clx[i] == 0x01:
        i += 3 + struct.unpack_from("<H", clx, i + 1)[0]
    if i >= len(clx) or clx[i] != 0x02:
        raise ValueError("таблица кусков текста не найдена")
    lcb = struct.unpack_from("<I", clx, i + 1)[0]
    plc = clx[i + 5:i + 5 + lcb]
    n = (len(plc) - 4) // 12
    cps = struct.unpack_from("<%dI" % (n + 1), plc, 0)
    out = []
    for k in range(n):
        _, fc, _ = struct.unpack_from("<HIH", plc, 4 * (n + 1) + 8 * k)
        count = cps[k + 1] - cps[k]
        if fc & 0x40000000:
            start = (fc & 0x3FFFFFFF) // 2
            out.append(_cp1252(word[start:start + count]))
        else:
            out.append(word[fc:fc + 2 * count].decode("utf-16-le", "replace"))
    return "".join(out)


_SPECIAL = {0x82: "‚", 0x83: "ƒ", 0x84: "„", 0x85: "…", 0x86: "†", 0x87: "‡",
            0x88: "ˆ", 0x89: "‰", 0x8a: "Š", 0x8b: "‹", 0x8c: "Œ", 0x91: "‘",
            0x92: "’", 0x93: "“", 0x94: "”", 0x95: "•", 0x96: "–", 0x97: "—",
            0x98: "˜", 0x99: "™", 0x9a: "š", 0x9b: "›", 0x9c: "œ", 0x9f: "Ÿ"}


def _cp1252(raw: bytes) -> str:
    """Word's 8-bit pieces: Windows-1252 with the spec's substitutions."""
    return "".join(_SPECIAL.get(b, chr(b)) for b in raw)


def _clean(chars: str) -> str:
    # Field code between 0x13 and 0x14 is the instruction (HYPERLINK …); the
    # result between 0x14 and 0x15 is what the reader sees.
    chars = re.sub("\x13[^\x14\x15]*\x14", "", chars)
    chars = chars.replace("\x13", "").replace("\x14", "").replace("\x15", "")
    chars = chars.replace("\x07\x07", "\n").replace("\x07", " | ")
    chars = chars.replace("\r", "\n").replace("\x0b", "\n").replace("\x0c", "\n")
    chars = re.sub("[\x00-\x06\x08\x0e-\x1f]", "", chars)
    chars = chars.replace(" ", " ")
    return re.sub(r"\n{3,}", "\n\n", chars).strip() + "\n"
