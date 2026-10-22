"""What the three Office 2007+ readers share: the archive, its relationships,
and how a table of cells is written down for the model.

ECMA-376 packages are zip archives of XML parts joined by relationship files
(`_rels/<part>.rels`) — the same three mechanisms whether the part is a
document, a workbook or a slide. Nothing here knows any format's vocabulary."""

from __future__ import annotations

import io
import posixpath
import re
import zipfile
import xml.etree.ElementTree as ET

NS = {
    "w": "http://schemas.openxmlformats.org/wordprocessingml/2006/main",
    "a": "http://schemas.openxmlformats.org/drawingml/2006/main",
    "p": "http://schemas.openxmlformats.org/presentationml/2006/main",
    "s": "http://schemas.openxmlformats.org/spreadsheetml/2006/main",
    "r": "http://schemas.openxmlformats.org/officeDocument/2006/relationships",
    "pr": "http://schemas.openxmlformats.org/package/2006/relationships",
}


class Package:
    """One open archive with its parts read on demand."""

    def __init__(self, data: bytes) -> None:
        try:
            self.zip = zipfile.ZipFile(io.BytesIO(data))
        except zipfile.BadZipFile as exc:
            raise ValueError("файл не является архивом Office (zip повреждён или это другой формат)") from exc

    def has(self, name: str) -> bool:
        return name in self.zip.namelist()

    def xml(self, name: str) -> ET.Element:
        try:
            return ET.fromstring(self.zip.read(name))
        except KeyError as exc:
            raise ValueError(f"в архиве нет части {name}") from exc
        except ET.ParseError as exc:
            raise ValueError(f"часть {name} не разбирается как XML: {exc}") from exc

    def rels(self, part: str) -> dict[str, tuple[str, str]]:
        """rId → (type suffix, target resolved against the part's folder)."""
        folder, base = posixpath.split(part)
        name = posixpath.join(folder, "_rels", base + ".rels")
        if not self.has(name):
            return {}
        out = {}
        for rel in self.xml(name).findall("pr:Relationship", NS):
            typ = rel.get("Type", "").rsplit("/", 1)[-1]
            target = rel.get("Target", "")
            if rel.get("TargetMode") != "External" and "://" not in target:
                # ECMA-376 allows an absolute part name ("/xl/worksheets/sheet1.xml") —
                # openpyxl writes them that way; Excel writes them relative to the part.
                target = target.lstrip("/") if target.startswith("/") else posixpath.join(folder, target)
                target = posixpath.normpath(target)
            out[rel.get("Id", "")] = (typ, target)
        return out

    def targets(self, part: str, typ: str) -> list[str]:
        return [t for _, (k, t) in self.rels(part).items() if k == typ]


def tag(el: ET.Element) -> str:
    return el.tag.rsplit("}", 1)[-1]


def tidy(blocks: list[str]) -> list[str]:
    """Blocks with at most one empty line between them and none at the ends."""
    out: list[str] = []
    for b in blocks:
        if b == "" and (not out or out[-1] == ""):
            continue
        out.append(b)
    while out and out[-1] == "":
        out.pop()
    return out


def md_table(rows: list[list[str]]) -> list[str]:
    """A markdown table. Ragged rows are padded, the first row is the header
    because that is what a sheet's or a table's first row is; a pipe inside a
    cell is escaped so the column count survives."""
    width = max((len(r) for r in rows), default=0)
    if width == 0:
        return []

    def line(cells):
        cells = list(cells) + [""] * (width - len(cells))
        return "| " + " | ".join(_cell(c) for c in cells) + " |"

    out = [line(rows[0]), "| " + " | ".join(["---"] * width) + " |"]
    out.extend(line(r) for r in rows[1:])
    return out


def _cell(text: str) -> str:
    return re.sub(r"\s*\n\s*", " ", str(text or "")).replace("|", "\\|").strip()
