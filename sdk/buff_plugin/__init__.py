"""Buff connector plugin SDK.

A plugin is an MCP server over stdio with a few extra methods in the buff/*
namespace. This SDK implements that protocol on the standard library alone.

Why no official MCP SDK: it depends on pydantic, whose core is a compiled Rust
extension. A bundle containing compiled wheels stops being one artifact and
becomes a matrix of (OS x architecture x ABI x libc) — which is the thing the
whole distribution design is built to avoid. The protocol is a few hundred lines
of JSON over pipes; the matrix would have cost far more than writing them.

A plugin never holds a credential. It asks the host to make requests on its
behalf and names which credential to use; the host attaches it, checks the
request against the operator's allowlist, and journals it.
"""

from .markup import html_to_text
from .words import plural, visible
from .server import Plugin, HostError, Event
from .tracker import (
    ACTIONS,
    CONTEXTS,
    Command,
    Identity,
    Scope,
    after,
    at_second,
    event_id,
    moment,
    parse_command,
    source,
)

__all__ = [
    "Plugin", "HostError", "Event", "html_to_text", "plural", "visible",
    "ACTIONS", "CONTEXTS", "Command", "Identity", "Scope",
    "after", "at_second", "event_id", "moment", "parse_command", "source",
]
