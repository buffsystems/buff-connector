"""A workbook (xlsx) as the text a person reads: every sheet, or one, as a
markdown table — shared strings resolved, formulas by their last value, a
date-formatted serial shown as a date rather than as 46000."""

from __future__ import annotations

import re
from datetime import datetime, timedelta

from ooxml import NS, Package, md_table, tag

_BUILTIN_DATES = {14, 15, 16, 17, 18, 19, 20, 21, 22, 45, 46, 47}
_EPOCH = datetime(1899, 12, 30)
_COL = re.compile(r"^([A-Z]+)")


def sheet_names(data: bytes) -> list[str]:
    return [name for name, _ in _sheets(Package(data))]


def text_of(data: bytes, sheet: str = "") -> str:
    pkg = Package(data)
    sheets = _sheets(pkg)
    if sheet:
        sheets = [(n, p) for n, p in sheets if n == sheet]
        if not sheets:
            raise KeyError(f"листа «{sheet}» в книге нет")
    shared = _shared_strings(pkg)
    dates = _date_styles(pkg)
    out: list[str] = []
    for name, part in sheets:
        out += [f"## {name}", ""]
        out += md_table(_rows(pkg.xml(part), shared, dates))
        out.append("")
    return "\n".join(out) + "\n"


def _sheets(pkg: Package) -> list[tuple[str, str]]:
    rels = pkg.rels("xl/workbook.xml")
    out = []
    rid = "{%s}id" % NS["r"]
    for sh in pkg.xml("xl/workbook.xml").iter("{%s}sheet" % NS["s"]):
        _, target = rels.get(sh.get(rid, ""), ("", ""))
        if target:
            out.append((sh.get("name", ""), target))
    return out


def _shared_strings(pkg: Package) -> list[str]:
    if not pkg.has("xl/sharedStrings.xml"):
        return []
    return ["".join(t.text or "" for t in si.iter("{%s}t" % NS["s"]))
            for si in pkg.xml("xl/sharedStrings.xml").iter("{%s}si" % NS["s"])]


def _date_styles(pkg: Package) -> dict[int, str]:
    """Style index → 'date' or 'datetime' for the styles that format a date."""
    if not pkg.has("xl/styles.xml"):
        return {}
    root = pkg.xml("xl/styles.xml")
    custom = {}
    for f in root.iter("{%s}numFmt" % NS["s"]):
        code = re.sub(r'"[^"]*"|\[[^\]]*\]|\\.', "", f.get("formatCode", ""))
        if re.search(r"[dmy]", code, re.I):
            custom[int(f.get("numFmtId", "0"))] = "datetime" if re.search(r"[hs]", code, re.I) else "date"
    out = {}
    xfs = root.find("s:cellXfs", NS)
    for i, xf in enumerate(xfs if xfs is not None else []):
        fid = int(xf.get("numFmtId", "0") or 0)
        if fid in _BUILTIN_DATES:
            out[i] = "datetime" if fid in (22, 45, 46, 47) else "date"
        elif fid in custom:
            out[i] = custom[fid]
    return out


def _rows(sheet, shared: list[str], dates: dict[int, str]) -> list[list[str]]:
    rows = []
    for row in sheet.iter("{%s}row" % NS["s"]):
        cells: list[str] = []
        for c in row.findall("s:c", NS):
            col = _col_index(c.get("r", ""))
            while col is not None and len(cells) < col:
                cells.append("")
            cells.append(_value(c, shared, dates))
        rows.append(cells)
    return rows


def _col_index(ref: str) -> int | None:
    m = _COL.match(ref)
    if not m:
        return None
    n = 0
    for ch in m.group(1):
        n = n * 26 + ord(ch) - 64
    return n - 1


def _value(c, shared: list[str], dates: dict[int, str]) -> str:
    kind = c.get("t", "n")
    v = c.find("s:v", NS)
    raw = v.text if v is not None and v.text is not None else ""
    if kind == "s":
        return shared[int(raw)] if raw.isdigit() and int(raw) < len(shared) else ""
    if kind == "inlineStr":
        return "".join(t.text or "" for t in c.iter("{%s}t" % NS["s"]))
    if kind == "b":
        return "TRUE" if raw == "1" else "FALSE"
    if kind in ("str", "e"):
        return raw
    style = dates.get(int(c.get("s", "0") or 0))
    if style and raw:
        return _date(raw, style)
    return _number(raw)


def _number(raw: str) -> str:
    try:
        f = float(raw)
    except ValueError:
        return raw
    return str(int(f)) if f == int(f) and "e" not in raw.lower() else raw


def _date(raw: str, style: str) -> str:
    try:
        when = _EPOCH + timedelta(days=float(raw))
    except (ValueError, OverflowError):
        return raw
    return when.strftime("%Y-%m-%d %H:%M" if style == "datetime" else "%Y-%m-%d")
