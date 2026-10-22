"""A Figma document, read as PARAMETERS AND TOKENS rather than as a picture.

The REST API returns a node tree in which every layer carries everything the
editor knows about it — bounding boxes, auto-layout, paints, typography, effects,
the styles and variables bound to each field, the component an instance came
from. That is exactly what a model that cannot see needs: the layout in numbers
and the design tokens by name. It is also far too much as it comes — colours are
0..1 floats, a paint is eight fields, a text node carries a style override table —
so this module turns the tree into what a person would read off the inspector
panel and nothing else.

Pure functions over dicts: no I/O, no plugin state. `Context` carries the two
maps that make names out of ids — the file's styles and components — and, when
the plan allows reading them, its variables.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any, Iterator


@dataclass(frozen=True)
class Context:
    """What a node's ids resolve to, from the same response the node came in."""

    styles: dict = field(default_factory=dict)      # style id → {name, styleType, …}
    components: dict = field(default_factory=dict)  # component id → {name, description, …}
    variables: dict = field(default_factory=dict)   # variable id → name ("Цвет/Акцент")


# ---- colours -------------------------------------------------------------

def hex_of(color: dict | None, opacity: float | None = None) -> str:
    """`#RRGGBB`, with the alpha as a percentage when it is not 100%.

    Figma keeps channels as 0..1 floats; a designer names a colour in hex, and a
    model reads hex from a design system's documentation. Alpha is folded in as
    text rather than a fourth pair of hex digits because `#0FA37F 50%` is what
    the inspector shows and what a reviewer will type back.
    """
    if not color:
        return ""
    r, g, b = (_channel(color.get(k)) for k in ("r", "g", "b"))
    out = f"#{r:02X}{g:02X}{b:02X}"
    alpha = float(color.get("a", 1)) * (1.0 if opacity is None else float(opacity))
    if alpha < 0.999:
        out += f" {round(alpha * 100)}%"
    return out


def _channel(v: Any) -> int:
    try:
        return max(0, min(255, round(float(v) * 255)))
    except (TypeError, ValueError):
        return 0


# ---- paints, effects, typography ---------------------------------------

def paint(p: dict, ctx: Context) -> dict | None:
    """One fill or stroke. An invisible paint is not a colour the design has."""
    if p.get("visible") is False:
        return None
    kind = str(p.get("type", ""))
    out: dict[str, Any] = {}
    if kind == "SOLID":
        out = {"type": "solid", "color": hex_of(p.get("color"), p.get("opacity"))}
    elif kind.startswith("GRADIENT_"):
        out = {
            "type": kind.lower(),
            "stops": [{"at": round(float(s.get("position", 0)), 3), "color": hex_of(s.get("color"))}
                      for s in p.get("gradientStops", [])],
        }
    elif kind == "IMAGE":
        out = {"type": "image", "ref": p.get("imageRef", ""), "fit": str(p.get("scaleMode", "")).lower()}
    else:
        out = {"type": kind.lower()}
    variable = ((p.get("boundVariables") or {}).get("color") or {}).get("id")
    if variable:
        out["variable"] = ctx.variables.get(variable, variable)
    return out


def paints(items: list | None, ctx: Context) -> list[dict]:
    return [q for q in (paint(p, ctx) for p in items or []) if q]


def effect(e: dict) -> dict | None:
    if e.get("visible") is False:
        return None
    kind = str(e.get("type", "")).lower()
    out: dict[str, Any] = {"type": kind}
    if "shadow" in kind:
        offset = e.get("offset") or {}
        out.update({"color": hex_of(e.get("color")), "offset": {"x": offset.get("x", 0), "y": offset.get("y", 0)},
                    "blur": e.get("radius", 0)})
        if e.get("spread"):
            out["spread"] = e["spread"]
    elif "blur" in kind:
        out["radius"] = e.get("radius", 0)
    return out


def effects(items: list | None) -> list[dict]:
    return [q for q in (effect(e) for e in items or []) if q]


def typography(style: dict | None) -> dict:
    """The text style as the inspector shows it, with line height in pixels."""
    if not style:
        return {}
    out: dict[str, Any] = {
        "font": style.get("fontFamily"),
        "style": style.get("fontStyle"),
        "weight": style.get("fontWeight"),
        "size": style.get("fontSize"),
    }
    if style.get("lineHeightPx") is not None:
        out["line_height"] = style["lineHeightPx"]
    if style.get("letterSpacing"):
        out["letter_spacing"] = style["letterSpacing"]
    if style.get("textAlignHorizontal") and style["textAlignHorizontal"] != "LEFT":
        out["align"] = str(style["textAlignHorizontal"]).lower()
    if style.get("textCase") and style["textCase"] != "ORIGINAL":
        out["case"] = str(style["textCase"]).lower()
    if style.get("textDecoration") and style["textDecoration"] != "NONE":
        out["decoration"] = str(style["textDecoration"]).lower()
    return {k: v for k, v in out.items() if v is not None}


# ---- layout ---------------------------------------------------------------

def bounds(node: dict) -> dict | None:
    box = node.get("absoluteBoundingBox")
    if not box:
        return None
    return {k: _num(box.get(src)) for k, src in (("x", "x"), ("y", "y"), ("w", "width"), ("h", "height"))}


def _num(v: Any) -> Any:
    try:
        f = float(v)
    except (TypeError, ValueError):
        return v
    return int(f) if f.is_integer() else round(f, 2)


def layout(node: dict) -> dict | None:
    """Auto-layout of a frame: direction, padding, gap, alignment, sizing."""
    mode = node.get("layoutMode")
    if not mode or mode == "NONE":
        return None
    pads = [node.get(k, 0) for k in ("paddingTop", "paddingRight", "paddingBottom", "paddingLeft")]
    out: dict[str, Any] = {
        "mode": str(mode).lower(),
        "padding": pads[0] if len(set(pads)) == 1 else {"top": pads[0], "right": pads[1], "bottom": pads[2], "left": pads[3]},
        "gap": node.get("itemSpacing", 0),
        "align": {"primary": str(node.get("primaryAxisAlignItems", "MIN")).lower(),
                  "counter": str(node.get("counterAxisAlignItems", "MIN")).lower()},
        "sizing": {"primary": str(node.get("primaryAxisSizingMode", "AUTO")).lower(),
                   "counter": str(node.get("counterAxisSizingMode", "AUTO")).lower()},
    }
    if node.get("layoutWrap") == "WRAP":
        out["wrap"] = True
        out["row_gap"] = node.get("counterAxisSpacing", 0)
    if mode == "GRID":
        out["grid"] = {"rows": node.get("gridRowCount"), "columns": node.get("gridColumnCount"),
                       "row_gap": node.get("gridRowGap", 0), "column_gap": node.get("gridColumnGap", 0)}
    return out


def placement(node: dict) -> dict | None:
    """How a child sits in its auto-layout parent."""
    out: dict[str, Any] = {}
    if node.get("layoutSizingHorizontal"):
        out["width"] = str(node["layoutSizingHorizontal"]).lower()
    if node.get("layoutSizingVertical"):
        out["height"] = str(node["layoutSizingVertical"]).lower()
    if node.get("layoutPositioning") == "ABSOLUTE":
        out["absolute"] = True
    if node.get("layoutGrow"):
        out["grow"] = True
    return out or None


def corners(node: dict) -> Any:
    radii = node.get("rectangleCornerRadii")
    if radii and len(set(radii)) > 1:
        return list(radii)
    return node.get("cornerRadius") or (radii[0] if radii else None)


# ---- names for ids ---------------------------------------------------------

def style_refs(node: dict, ctx: Context) -> dict:
    """`{fill: "Кнопка/Малахит", text: "Заголовок/H1"}` — the tokens on this node.

    The node carries style IDS; the response's top-level `styles` map carries
    their names. A name is what a design system documents and what the answer
    should say; an id is a lookup key nobody reads.
    """
    out = {}
    for slot, style_id in (node.get("styles") or {}).items():
        meta = ctx.styles.get(style_id) or {}
        out[str(slot)] = meta.get("name") or style_id
    return out


def variable_refs(bound: dict | None, ctx: Context) -> dict:
    """`{fills: ["Цвет/Акцент"], itemSpacing: "Отступ/M"}` — variables bound to fields.

    Available on every plan through the file itself (`boundVariables`), even where
    the Variables endpoint is not: the id is resolved to a name when the
    collection could be read, and kept as the id otherwise — an id still tells a
    reviewer two fields share one token.
    """
    out: dict[str, Any] = {}
    for fld, ref in (bound or {}).items():
        if isinstance(ref, list):
            names = [ctx.variables.get(r.get("id"), r.get("id")) for r in ref if isinstance(r, dict)]
            if names:
                out[fld] = names
        elif isinstance(ref, dict):
            if ref.get("id"):
                out[fld] = ctx.variables.get(ref["id"], ref["id"])
            else:
                nested = variable_refs(ref, ctx)
                if nested:
                    out[fld] = nested
    return out


# ---- the node itself -------------------------------------------------------

def compress(node: dict, ctx: Context) -> dict:
    """One node, its inspector panel, and its children the same way.

    Nothing is truncated: a frame with forty children answers with forty
    children. What is DROPPED is what carries no design meaning for a reader —
    defaults (visible, opacity 1, no rotation), export settings, prototype
    plumbing, vector geometry.
    """
    out: dict[str, Any] = {"id": node.get("id"), "name": node.get("name"), "type": str(node.get("type", "")).lower()}
    if node.get("visible") is False:
        out["hidden"] = True
    box = bounds(node)
    if box:
        out["bounds"] = box
    if node.get("rotation"):
        out["rotation"] = node["rotation"]
    if node.get("opacity") is not None and float(node["opacity"]) < 0.999:
        out["opacity"] = node["opacity"]
    if node.get("clipsContent"):
        out["clips"] = True
    lay = layout(node)
    if lay:
        out["layout"] = lay
    place = placement(node)
    if place:
        out["placement"] = place
    radius = corners(node)
    if radius:
        out["radius"] = radius
    fills = paints(node.get("fills"), ctx)
    if fills:
        out["fills"] = fills
    strokes = paints(node.get("strokes"), ctx)
    if strokes:
        out["strokes"] = {"paints": strokes, "weight": node.get("strokeWeight", 1),
                          "align": str(node.get("strokeAlign", "")).lower() or None}
        if node.get("individualStrokeWeights"):
            out["strokes"]["weight"] = node["individualStrokeWeights"]
        if node.get("strokeDashes"):
            out["strokes"]["dashes"] = node["strokeDashes"]
    fx = effects(node.get("effects"))
    if fx:
        out["effects"] = fx
    if node.get("type") == "TEXT":
        out["text"] = node.get("characters", "")
        out["typography"] = typography(node.get("style"))
        if node.get("styleOverrideTable"):
            out["typography_overrides"] = len(node["styleOverrideTable"])
    refs = style_refs(node, ctx)
    if refs:
        out["styles"] = refs
    variables = variable_refs(node.get("boundVariables"), ctx)
    if variables:
        out["variables"] = variables
    if node.get("type") == "INSTANCE":
        comp = ctx.components.get(node.get("componentId")) or {}
        out["component"] = comp.get("name") or node.get("componentId")
        props = {}
        for name, prop in (node.get("componentProperties") or {}).items():
            props[name.split("#")[0]] = prop.get("value") if isinstance(prop, dict) else prop
        if props:
            out["properties"] = props
    if node.get("type") in ("COMPONENT", "COMPONENT_SET"):
        comp = ctx.components.get(node.get("id")) or {}
        if comp.get("description"):
            out["description"] = comp["description"]
        defs = node.get("componentPropertyDefinitions") or {}
        if defs:
            out["property_definitions"] = {
                name.split("#")[0]: {"type": str(d.get("type", "")).lower(), "default": d.get("defaultValue"),
                                     **({"options": d["variantOptions"]} if d.get("variantOptions") else {})}
                for name, d in defs.items()
            }
    children = node.get("children")
    if children is not None:
        out["children"] = [compress(c, ctx) for c in children]
    return out


def outline(node: dict) -> dict:
    """The skeleton of a file: names, types, sizes — to pick what to read next."""
    out: dict[str, Any] = {"id": node.get("id"), "name": node.get("name"), "type": str(node.get("type", "")).lower()}
    box = bounds(node)
    if box:
        out["size"] = {"w": box["w"], "h": box["h"]}
    if node.get("type") == "TEXT" and node.get("characters"):
        out["text"] = node["characters"]
    if node.get("visible") is False:
        out["hidden"] = True
    kids = node.get("children")
    if kids:
        out["children"] = [outline(c) for c in kids]
    return out


def walk(node: dict, path: tuple[str, ...] = ()) -> Iterator[tuple[dict, tuple[str, ...]]]:
    """Every node with the names of its ancestors, pages first."""
    yield node, path
    for child in node.get("children") or []:
        yield from walk(child, path + (str(node.get("name", "")),))


def matches(node: dict, words: list[str], types: list[str]) -> bool:
    """A node matches when every word occurs in its name or its text.

    By WORDS, not as one phrase — the way the editor's own search behaves: a
    query for «кнопка войти» must find a layer named «Кнопка «Войти»».
    """
    if types and str(node.get("type", "")).lower() not in types:
        return False
    hay = f"{node.get('name', '')} {node.get('characters', '')}".lower()
    return all(w in hay for w in words)


def words_of(query: str) -> list[str]:
    return [w for w in "".join(ch if ch.isalnum() else " " for ch in str(query or "").lower()).split() if w]


# ---- tokens -------------------------------------------------------------------

def style_value(style_type: str, node: dict | None, ctx: Context) -> Any:
    """What a published style IS, read off the node that defines it."""
    if not node:
        return None
    kind = str(style_type).upper()
    if kind == "FILL":
        return paints(node.get("fills"), ctx)
    if kind == "TEXT":
        return typography(node.get("style"))
    if kind == "EFFECT":
        return effects(node.get("effects"))
    if kind == "GRID":
        return [{"pattern": str(g.get("pattern", "")).lower(), "size": g.get("sectionSize"),
                 "count": g.get("count"), "gutter": g.get("gutterSize"), "offset": g.get("offset")}
                for g in node.get("layoutGrids", [])]
    return None


def variables_of(meta: dict) -> tuple[list[dict], dict]:
    """Collections with their modes and every variable's value per mode, plus the
    id → name map the node reader uses. Aliases are written as `→ Имя`."""
    names = {vid: v.get("name", vid) for vid, v in (meta.get("variables") or {}).items()}
    collections = []
    for cid, coll in (meta.get("variableCollections") or {}).items():
        modes = {m.get("modeId"): m.get("name") for m in coll.get("modes", [])}
        variables = []
        for vid in coll.get("variableIds", []):
            v = (meta.get("variables") or {}).get(vid) or {}
            values = {}
            for mode_id, raw in (v.get("valuesByMode") or {}).items():
                values[modes.get(mode_id, mode_id)] = variable_value(raw, names)
            variables.append({"name": v.get("name", vid), "type": str(v.get("resolvedType", "")).lower(),
                              "values": values, **({"description": v["description"]} if v.get("description") else {})})
        collections.append({"name": coll.get("name", cid), "modes": list(modes.values()),
                            "default_mode": modes.get(coll.get("defaultModeId")), "variables": variables})
    return collections, names


def variable_value(raw: Any, names: dict) -> Any:
    if isinstance(raw, dict):
        if raw.get("type") == "VARIABLE_ALIAS":
            return f"→ {names.get(raw.get('id'), raw.get('id'))}"
        if "r" in raw and "g" in raw and "b" in raw:
            return hex_of(raw)
    return raw
