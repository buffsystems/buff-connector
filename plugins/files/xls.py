"""An Excel 97–2003 workbook (xls) as text: every sheet as a markdown table.

The Workbook stream is a sequence of BIFF8 records; the globals come first
(sheet names, the shared string table, the cell formats), then one substream
per sheet with its cell records. Only the values matter here: a string, a
number — shown as a date when its format is a date — a boolean, a formula's
last result."""

from __future__ import annotations

import re
import struct
from datetime import datetime, timedelta

from ole import Compound
from ooxml import md_table

_EPOCH = datetime(1899, 12, 30)
_BUILTIN_DATES = {14, 15, 16, 17, 18, 19, 20, 21, 22, 45, 46, 47}


def text_of(data: bytes) -> str:
    book = Compound(data).stream("Workbook" if "Workbook" in Compound(data).names() else "Book")
    records = list(_records(book))
    sheets, sst, dates = _globals(records)
    out: list[str] = []
    for name, pos in sheets:
        out += [f"## {name}", ""]
        out += md_table(_cells(book, pos, sst, dates))
        out.append("")
    return "\n".join(out) + "\n"


def _records(book: bytes):
    """(type, offset of the body, body) for every record — CONTINUE records
    are glued onto the record they continue."""
    i, current = 0, None
    while i + 4 <= len(book):
        rt, ln = struct.unpack_from("<HH", book, i)
        body = book[i + 4:i + 4 + ln]
        if rt == 0x3C and current is not None:  # CONTINUE
            current[2].append(body)
        else:
            if current is not None:
                yield current[0], current[1], current[2]
            current = (rt, i, [body])
        i += 4 + ln
    if current is not None:
        yield current[0], current[1], current[2]


def _globals(records) -> tuple[list[tuple[str, int]], list[str], dict[int, str]]:
    sheets, sst, formats, xfs = [], [], {}, []
    for rt, _, parts in records:
        if rt == 0x85:  # BOUNDSHEET
            pos = struct.unpack_from("<I", parts[0], 0)[0]
            sheets.append((_short_string(parts[0], 6), pos))
        elif rt == 0xFC:  # SST
            sst = _sst(parts)
        elif rt == 0x41E:  # FORMAT
            fid = struct.unpack_from("<H", parts[0], 0)[0]
            formats[fid] = _unicode_string(parts[0], 2)[0]
        elif rt == 0xE0:  # XF
            xfs.append(struct.unpack_from("<H", parts[0], 2)[0])
        elif rt == 0x0A and sheets:  # EOF of the globals
            break
    dates = {}
    for i, fid in enumerate(xfs):
        if fid in _BUILTIN_DATES:
            dates[i] = "datetime" if fid in (22, 45, 46, 47) else "date"
        elif fid in formats:
            code = re.sub(r'"[^"]*"|\[[^\]]*\]|\\.', "", formats[fid])
            if re.search(r"[dmy]", code, re.I):
                dates[i] = "datetime" if re.search(r"[hs]", code, re.I) else "date"
    return sheets, sst, dates


def _short_string(raw: bytes, off: int) -> str:
    cch, flags = raw[off], raw[off + 1]
    if flags & 1:
        return raw[off + 2:off + 2 + 2 * cch].decode("utf-16-le", "replace")
    return raw[off + 2:off + 2 + cch].decode("latin-1")


def _unicode_string(raw: bytes, off: int) -> tuple[str, int]:
    """An XLUnicodeRichExtendedString at off → (text, offset after it)."""
    cch, flags = struct.unpack_from("<HB", raw, off)
    off += 3
    runs = ext = 0
    if flags & 8:
        runs = struct.unpack_from("<H", raw, off)[0]
        off += 2
    if flags & 4:
        ext = struct.unpack_from("<I", raw, off)[0]
        off += 4
    if flags & 1:
        text = raw[off:off + 2 * cch].decode("utf-16-le", "replace")
        off += 2 * cch
    else:
        text = raw[off:off + cch].decode("latin-1")
        off += cch
    return text, off + 4 * runs + ext


def _sst(parts: list[bytes]) -> list[str]:
    """The shared strings; a string may be split across CONTINUE records,
    and the continuation restarts with its own flags byte."""
    raw = parts[0]
    unique = struct.unpack_from("<I", raw, 4)[0]
    out: list[str] = []
    off, part = 8, 0
    while len(out) < unique:
        if off >= len(raw):
            part += 1
            if part >= len(parts):
                break
            raw, off = parts[part], 0
        cch, flags = struct.unpack_from("<HB", raw, off)
        off += 3
        runs = ext = 0
        if flags & 8:
            runs = struct.unpack_from("<H", raw, off)[0]
            off += 2
        if flags & 4:
            ext = struct.unpack_from("<I", raw, off)[0]
            off += 4
        text, wide = "", bool(flags & 1)
        need = cch
        while need:
            if off >= len(raw):
                part += 1
                raw, off = parts[part], 1
                wide = bool(parts[part][0] & 1)
            avail = (len(raw) - off) // (2 if wide else 1)
            take = min(need, avail)
            chunk = raw[off:off + take * (2 if wide else 1)]
            text += chunk.decode("utf-16-le", "replace") if wide else chunk.decode("latin-1")
            off += take * (2 if wide else 1)
            need -= take
        out.append(text)
        off += 4 * runs + ext
    return out


def _cells(book: bytes, pos: int, sst: list[str], dates: dict[int, str]) -> list[list[str]]:
    grid: dict[int, dict[int, str]] = {}

    def put(rw, col, ixfe, value):
        grid.setdefault(rw, {})[col] = value

    pending_formula = None
    for rt, off, parts in _records(book[pos:]):
        raw = parts[0]
        if rt == 0x0A:  # EOF
            break
        if rt == 0xFD:  # LABELSST
            rw, col, ixfe, isst = struct.unpack_from("<HHHI", raw, 0)
            put(rw, col, ixfe, sst[isst] if isst < len(sst) else "")
        elif rt == 0x204:  # LABEL
            rw, col, ixfe = struct.unpack_from("<HHH", raw, 0)
            put(rw, col, ixfe, _unicode_string(raw, 6)[0])
        elif rt == 0x203:  # NUMBER
            rw, col, ixfe, num = struct.unpack_from("<HHHd", raw, 0)
            put(rw, col, ixfe, _number(num, ixfe, dates))
        elif rt == 0x27E:  # RK
            rw, col, ixfe, rk = struct.unpack_from("<HHHI", raw, 0)
            put(rw, col, ixfe, _number(_rk(rk), ixfe, dates))
        elif rt == 0xBD:  # MULRK
            rw, first = struct.unpack_from("<HH", raw, 0)
            for k in range((len(raw) - 6) // 6):
                ixfe, rk = struct.unpack_from("<HI", raw, 4 + 6 * k)
                put(rw, first + k, ixfe, _number(_rk(rk), ixfe, dates))
        elif rt == 0x205:  # BOOLERR
            rw, col, ixfe, val, is_err = struct.unpack_from("<HHHBB", raw, 0)
            put(rw, col, ixfe, ("#ERR" if is_err else ("TRUE" if val else "FALSE")))
        elif rt == 0x06:  # FORMULA
            rw, col, ixfe = struct.unpack_from("<HHH", raw, 0)
            res = raw[6:14]
            if res[6:8] == b"\xff\xff":
                kind = res[0]
                if kind == 0:
                    pending_formula = (rw, col, ixfe)
                    continue
                put(rw, col, ixfe, {1: "TRUE" if res[2] else "FALSE", 2: "#ERR", 3: ""}.get(kind, ""))
            else:
                put(rw, col, ixfe, _number(struct.unpack("<d", res)[0], ixfe, dates))
        elif rt == 0x207 and pending_formula:  # STRING (a formula's text result)
            rw, col, ixfe = pending_formula
            put(rw, col, ixfe, _unicode_string(raw, 0)[0])
            pending_formula = None
    rows = []
    for rw in range(max(grid, default=-1) + 1):
        cols = grid.get(rw, {})
        rows.append([cols.get(c, "") for c in range(max(cols, default=-1) + 1)])
    return rows


def _rk(rk: int) -> float:
    if rk & 2:
        value = float(struct.unpack("<i", struct.pack("<I", rk))[0] >> 2)
    else:
        value = struct.unpack("<d", struct.pack("<Q", (rk & ~3) << 32))[0]
    return value / 100 if rk & 1 else value


def _number(num: float, ixfe: int, dates: dict[int, str]) -> str:
    style = dates.get(ixfe)
    if style:
        try:
            when = _EPOCH + timedelta(days=num)
            return when.strftime("%Y-%m-%d %H:%M" if style == "datetime" else "%Y-%m-%d")
        except OverflowError:
            pass
    return str(int(num)) if num == int(num) and abs(num) < 1e15 else repr(num)
