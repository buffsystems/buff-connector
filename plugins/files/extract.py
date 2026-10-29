"""What a file IS and the text a person reads out of it.

Kind is decided by the bytes first (a zip, an OLE container, a PDF) and the
extension second — a docx renamed .zip is still a document, and a .doc that is
really RTF is not. Text files are decoded as UTF-8, falling back to the
Windows Cyrillic code page that old notes on a share tend to be in. Anything
else is named by kind and size, never "read" as garbage."""

from __future__ import annotations

import posixpath

from buff_plugin import say

import doc
import docx
import pdf
import ppt
import pptx
import xls
import xlsx

OOXML = {".docx": ("документ Word", docx.text_of), ".docm": ("документ Word", docx.text_of),
         ".xlsx": ("таблица Excel", xlsx.text_of), ".xlsm": ("таблица Excel", xlsx.text_of),
         ".pptx": ("презентация PowerPoint", pptx.text_of), ".pptm": ("презентация PowerPoint", pptx.text_of)}
OLE = {".doc": ("документ Word 97–2003", doc.text_of), ".dot": ("шаблон Word 97–2003", doc.text_of),
       ".xls": ("таблица Excel 97–2003", xls.text_of), ".xlt": ("шаблон Excel 97–2003", xls.text_of),
       ".ppt": ("презентация PowerPoint 97–2003", ppt.text_of), ".pps": ("презентация PowerPoint 97–2003", ppt.text_of)}
# The kinds above in English, for a reader of English (#565).
KIND_EN = {"документ Word": "Word document", "таблица Excel": "Excel spreadsheet",
           "презентация PowerPoint": "PowerPoint presentation", "документ Word 97–2003": "Word 97–2003 document",
           "шаблон Word 97–2003": "Word 97–2003 template", "таблица Excel 97–2003": "Excel 97–2003 spreadsheet",
           "шаблон Excel 97–2003": "Excel 97–2003 template",
           "презентация PowerPoint 97–2003": "PowerPoint 97–2003 presentation", "PDF": "PDF"}
TEXT = {".txt", ".md", ".markdown", ".csv", ".tsv", ".json", ".xml", ".yaml", ".yml", ".ini", ".cfg", ".conf",
        ".toml", ".log", ".html", ".htm", ".rst", ".sql", ".py", ".js", ".ts", ".go", ".java", ".c", ".h",
        ".cpp", ".cs", ".rb", ".php", ".sh", ".bat", ".ps1", ".tex", ".adoc", ".properties", ".env", ".svg"}


class Unreadable(Exception):
    """A file that has no text to give: binary, or a format that failed."""


def text_of(name: str, data: bytes) -> str:
    """The file as text, or Unreadable saying what it is instead."""
    ext = posixpath.splitext(name)[1].lower()
    if data[:4] == b"PK\x03\x04":
        kind, reader = OOXML.get(ext, ("", None)) if ext in OOXML else _ooxml_by_content(data)
        if reader:
            return _run(kind, reader, data)
        raise Unreadable(say(f"архив zip, {len(data)} байт", f"a zip archive, {len(data)} bytes"))
    if data[:8] == b"\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1":
        kind, reader = OLE.get(ext, ("", None)) if ext in OLE else _ole_by_content(data)
        if reader:
            return _run(kind, reader, data)
        raise Unreadable(say(f"файл OLE неизвестного вида, {len(data)} байт", f"an OLE file of an unknown kind, {len(data)} bytes"))
    if data[:5] == b"%PDF-":
        return _run("PDF", pdf.text_of, data)
    if ext in TEXT or _looks_like_text(data):
        return _decode(data)
    suffix = f" {ext}" if ext else ""
    raise Unreadable(say(f"двоичный файл{suffix}, {len(data)} байт", f"a binary file{suffix}, {len(data)} bytes"))


def kind_of(name: str) -> str:
    ext = posixpath.splitext(name)[1].lower()
    if ext in OOXML:
        return _kind(OOXML[ext][0])
    if ext in OLE:
        return _kind(OLE[ext][0])
    if ext == ".pdf":
        return "PDF"
    if ext in TEXT:
        return say("текст", "text")
    return ext.lstrip(".") or say("файл", "file")


def _kind(ru: str) -> str:
    return say(ru, KIND_EN.get(ru, ru))


def _run(kind: str, reader, data: bytes) -> str:
    try:
        return reader(data)
    except ValueError as exc:
        raise Unreadable(f"{_kind(kind)}: {exc}") from exc
    except Exception as exc:  # a malformed file must name itself, not crash the tool
        why = f"{type(exc).__name__}: {exc}"
        raise Unreadable(say(f"{kind}: не разбирается ({why})", f"{_kind(kind)}: does not parse ({why})")) from exc


def _ooxml_by_content(data: bytes):
    import zipfile
    import io
    try:
        names = zipfile.ZipFile(io.BytesIO(data)).namelist()
    except zipfile.BadZipFile:
        return "", None
    for part, key in (("word/document.xml", ".docx"), ("xl/workbook.xml", ".xlsx"), ("ppt/presentation.xml", ".pptx")):
        if part in names:
            return OOXML[key]
    return "", None


def _ole_by_content(data: bytes):
    import ole
    try:
        names = ole.Compound(data).names()
    except ValueError:
        return "", None
    for stream, key in (("WordDocument", ".doc"), ("Workbook", ".xls"), ("Book", ".xls"), ("PowerPoint Document", ".ppt")):
        if stream in names:
            return OLE[key]
    return "", None


def _looks_like_text(data: bytes) -> bool:
    sample = data[:4096]
    if b"\x00" in sample:
        return False
    try:
        sample.decode("utf-8")
        return True
    except UnicodeDecodeError:
        return sum(1 for b in sample if b < 32 and b not in (9, 10, 13)) == 0


def _decode(data: bytes) -> str:
    if data.startswith(b"\xef\xbb\xbf"):
        return data[3:].decode("utf-8", "replace")
    if data.startswith((b"\xff\xfe", b"\xfe\xff")):
        return data.decode("utf-16", "replace")
    try:
        return data.decode("utf-8")
    except UnicodeDecodeError:
        return data.decode("cp1251", "replace")
