"""What the model is told about a plugin's tools, read from files (cloud-front#718).

A tool's description and its parameters' are prompt text, and prompt text lives in
files a person reviews, one per language, beside the plugin's code:
`tools.ru.md` and `tools.en.md`. The code keeps what a tool IS — its name, its
input schema, its marks; the files give the words. The connector asks for the
catalogue in each language and passes each call's language, so a model working
in English reads English.

The format is the connector's own (internal/tooltext):

    <!-- anything before the first tool is a note for whoever edits the file -->
    # search_issues
    Find issues by text. Use it when …

    ## query
    What to search for

A "# " heading opens a tool, "## " one of its parameters. Lines that follow each
other join with a space (wrap them freely); a blank line starts a new line of the
text. A {{name}} stands for a value only the running plugin knows (the `fill` of
its tool), such as the connection's area.
"""

from __future__ import annotations

import copy
import os
import re

LANGS = ("ru", "en")

_NAME = re.compile(r"^[A-Za-z_][A-Za-z0-9_.-]*$")
_PLACEHOLDER = re.compile(r"\{\{([A-Za-z_][A-Za-z0-9_]*)\}\}")


class TextsError(ValueError):
    """A tools file that does not parse, or does not fit the tools."""


def parse(text: str) -> dict[str, dict]:
    """{tool: {"description": str, "params": {name: str}}}, in the order written."""
    tools: dict[str, dict] = {}
    tool: dict | None = None
    tool_name = param = ""
    para: list[str] = []
    paras: list[str] = []

    def flush() -> None:
        nonlocal para, paras
        if para:
            paras.append(" ".join(para))
            para = []
        body = "\n".join(paras)
        paras = []
        if tool is None:
            return
        if param:
            if not body:
                raise TextsError(f"{tool_name}.{param}: no text")
            tool["params"][param] = body
        else:
            if not body:
                raise TextsError(f"{tool_name}: no text")
            tool["description"] = body

    for line in text.replace("\r\n", "\n").split("\n"):
        stripped = line.strip()
        if line.startswith("## "):
            if tool is None:
                raise TextsError(f"parameter {stripped!r} before any tool")
            flush()
            param = line[3:].strip()
            if not _NAME.match(param):
                raise TextsError(f"{tool_name}: {param!r} is not a parameter name")
            if param in tool["params"]:
                raise TextsError(f"{tool_name}.{param}: written twice")
        elif line.startswith("# "):
            flush()
            tool_name, param = line[2:].strip(), ""
            if not _NAME.match(tool_name):
                raise TextsError(f"{tool_name!r} is not a tool name")
            if tool_name in tools:
                raise TextsError(f"{tool_name}: written twice")
            tool = {"description": "", "params": {}}
            tools[tool_name] = tool
        elif tool is None:
            continue  # the note before the first tool
        elif not stripped:
            if para:
                paras.append(" ".join(para))
                para = []
        else:
            para.append(stripped)
    flush()
    if not tools:
        raise TextsError("no tools")
    return tools


def load(directory: str) -> dict[str, dict[str, dict]]:
    """Every language's file in `directory`: {lang: parse(file)}; a language
    without a file is left out (a plugin that still words its tools in code)."""
    out = {}
    for lang in LANGS:
        path = os.path.join(directory, f"tools.{lang}.md")
        if not os.path.exists(path):
            continue
        with open(path, encoding="utf-8") as f:
            try:
                out[lang] = parse(f.read())
            except TextsError as exc:
                raise TextsError(f"tools.{lang}.md: {exc}") from None
    return out


def describe(schema: dict, params: dict[str, str]) -> dict:
    """The schema with the parameters' texts in its properties. A text for a
    parameter the schema lacks means the file and the code drifted apart."""
    if not params:
        return schema
    out = copy.deepcopy(schema)
    props = out.get("properties") or {}
    for name, text in params.items():
        if name not in props:
            raise TextsError(f"parameter {name}: not in the schema")
        props[name]["description"] = text
    return out


def placeholders(words: dict) -> set[str]:
    """The {{names}} one tool's words use."""
    texts = [words["description"], *words["params"].values()]
    return {m for t in texts for m in _PLACEHOLDER.findall(t)}


def filled(words: dict, values: dict[str, str]) -> dict:
    """One tool's words with its {{names}} replaced by their values."""
    def sub(text: str) -> str:
        return _PLACEHOLDER.sub(lambda m: values.get(m.group(1), m.group(0)), text)

    return {"description": sub(words["description"]),
            "params": {k: sub(v) for k, v in words["params"].items()}}
