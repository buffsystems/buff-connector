"""A Word document (docx) as the text a person reads: headings, paragraphs,
lists, tables, links, text boxes — in document order, as markdown."""

from __future__ import annotations

import re
import xml.etree.ElementTree as ET

from ooxml import NS, Package, md_table, tag, tidy

_HEADING = re.compile(r"(?:heading|заголовок)\s*(\d)", re.I)
_W = "{%s}" % NS["w"]
_R_ID = "{%s}id" % NS["r"]


def text_of(data: bytes) -> str:
    pkg = Package(data)
    body = pkg.xml("word/document.xml").find("w:body", NS)
    if body is None:
        raise ValueError("в документе нет тела (word/document.xml без w:body)")
    links = {rid: t for rid, (k, t) in pkg.rels("word/document.xml").items() if k == "hyperlink"}
    return "\n".join(tidy(_blocks(body, links)) + [""]) + "\n"


def _blocks(parent: ET.Element, links: dict) -> list[str]:
    """Block after block: a paragraph, a table, or the content of a wrapper
    (a content control, a text box) which is itself blocks."""
    out: list[str] = []
    for el in parent:
        name = tag(el)
        if name == "p":
            text = _paragraph(el, links)
            # Items of one list stay together; anything else is its own block.
            if text.lstrip().startswith("- ") and out and out[-1] == "" and len(out) > 1 and out[-2].lstrip().startswith("- "):
                out.pop()
            if text:
                out.append(text)
            out.extend(_nested(el, links))
            out.append("")
        elif name == "tbl":
            out.extend(md_table(_table(el, links)))
            out.append("")
        elif name in ("sdt", "sdtContent", "customXml", "smartTag"):
            out.extend(_blocks(el, links))
    return out


def _nested(p: ET.Element, links: dict) -> list[str]:
    """Blocks a paragraph carries inside its runs — a text box's content."""
    out = []
    for box in p.iter(_W + "txbxContent"):
        out.append("")
        out.extend(_blocks(box, links))
    return out


def _table(tbl: ET.Element, links: dict) -> list[list[str]]:
    rows = []
    for tr in tbl.findall("w:tr", NS):
        row = []
        for tc in tr.findall("w:tc", NS):
            paras = [b for b in _blocks(tc, links) if b != ""]
            row.append(" ".join(paras))
        rows.append(row)
    return rows


def _paragraph(p: ET.Element, links: dict) -> str:
    text = "".join(_inline(el, links) for el in p if tag(el) != "pPr")
    ppr = p.find("w:pPr", NS)
    if ppr is None:
        return text
    style = ppr.find("w:pStyle", NS)
    if style is not None:
        m = _HEADING.search(style.get(_W + "val", ""))
        if m:
            return "#" * int(m.group(1)) + " " + text
    if ppr.find("w:numPr", NS) is not None:
        lvl = ppr.find("w:numPr/w:ilvl", NS)
        depth = int(lvl.get(_W + "val", "0") or 0) if lvl is not None else 0
        return "  " * depth + "- " + text
    return text


def _inline(el: ET.Element, links: dict) -> str:
    name = tag(el)
    if name == "r":
        return _run(el)
    if name == "hyperlink":
        inner = "".join(_inline(c, links) for c in el)
        target = links.get(el.get(_R_ID, ""))
        return f"[{inner}]({target})" if target else inner
    if name in ("ins", "smartTag", "sdt", "sdtContent", "fldSimple", "customXml"):
        return "".join(_inline(c, links) for c in el)
    return ""


def _run(r: ET.Element) -> str:
    out = []
    for el in r:
        name = tag(el)
        if name == "t":
            out.append(el.text or "")
        elif name == "tab":
            out.append("\t")
        elif name in ("br", "cr"):
            out.append("\n")
        elif name == "sym":
            out.append(chr(int(el.get(_W + "char", "20"), 16)))
    return "".join(out)
