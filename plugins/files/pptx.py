"""A presentation (pptx) as the text a person reads: slide by slide in show
order — the title, the text of every shape, tables, and the speaker notes."""

from __future__ import annotations

import posixpath

from ooxml import NS, Package, md_table, tag

_A = "{%s}" % NS["a"]
_P = "{%s}" % NS["p"]


def text_of(data: bytes) -> str:
    pkg = Package(data)
    out: list[str] = []
    for n, part in enumerate(_slides(pkg), 1):
        title, blocks = _slide(pkg.xml(part))
        out += [f"## Слайд {n}. {title}".rstrip(". ") if title else f"## Слайд {n}", ""]
        out += blocks
        notes = _notes(pkg, part)
        if notes:
            out += [f"Заметки: {notes}", ""]
    return "\n".join(out) + "\n"


def _slides(pkg: Package) -> list[str]:
    rels = pkg.rels("ppt/presentation.xml")
    rid = "{%s}id" % NS["r"]
    out = []
    for sid in pkg.xml("ppt/presentation.xml").iter(_P + "sldId"):
        _, target = rels.get(sid.get(rid, ""), ("", ""))
        if target:
            out.append(target)
    return out


def _slide(root) -> tuple[str, list[str]]:
    title = ""
    blocks: list[str] = []
    tree = root.find("p:cSld/p:spTree", NS)
    for el in (tree if tree is not None else []):
        name = tag(el)
        if name == "sp":
            paras = _paragraphs(el.find("p:txBody", NS))
            if not paras:
                continue
            if _placeholder(el) in ("title", "ctrTitle") and not title:
                title = " ".join(paras)
                continue
            blocks += paras + [""]
        elif name == "graphicFrame":
            for tbl in el.iter(_A + "tbl"):
                blocks += md_table(_table(tbl)) + [""]
        elif name == "grpSp":
            _, inner = _slide_group(el)
            blocks += inner
    return title, blocks


def _slide_group(grp) -> tuple[str, list[str]]:
    fake = type("T", (), {})()
    blocks: list[str] = []
    for el in grp:
        if tag(el) == "sp":
            paras = _paragraphs(el.find("p:txBody", NS))
            if paras:
                blocks += paras + [""]
        elif tag(el) == "grpSp":
            blocks += _slide_group(el)[1]
    return "", blocks


def _placeholder(sp) -> str:
    ph = sp.find("p:nvSpPr/p:nvPr/p:ph", NS)
    return ph.get("type", "body") if ph is not None else ""


def _paragraphs(body) -> list[str]:
    if body is None:
        return []
    out = []
    for p in body.findall("a:p", NS):
        text = "".join(_run(el) for el in p)
        if text.strip():
            out.append(text)
    return out


def _run(el) -> str:
    name = tag(el)
    if name == "r" or name == "fld":
        t = el.find("a:t", NS)
        return t.text or "" if t is not None else ""
    if name == "br":
        return "\n"
    return ""


def _table(tbl) -> list[list[str]]:
    return [[" ".join(_paragraphs(tc.find("a:txBody", NS))) for tc in tr.findall("a:tc", NS)]
            for tr in tbl.findall("a:tr", NS)]


def _notes(pkg: Package, slide_part: str) -> str:
    out = []
    for part in pkg.targets(slide_part, "notesSlide"):
        tree = pkg.xml(part).find("p:cSld/p:spTree", NS)
        for sp in (tree.iter(_P + "sp") if tree is not None else []):
            if _placeholder(sp) in ("sldNum", "hdr", "ftr", "dt", "sldImg"):
                continue
            out += _paragraphs(sp.find("p:txBody", NS))
    return " ".join(out)
