"""Where a path is — on this machine or on a share — and whether it lies
inside the connection's area.

A share path is accepted the three ways people write it (`\\\\server\\share\\x`,
`//server/share/x`, `smb://server/share/x`) and always shown one way, the URL
form, which survives JSON and markdown without escaping. The area is the list
of roots — everything under a root is inside — less the excludes. No roots
means every local path; a share is never reached unless a root names its
server, because the connection's password travels with each dial and must not
go to a server the model chose."""

from __future__ import annotations

import fnmatch
import posixpath
import re
from dataclasses import dataclass

from buff_plugin import say

_UNC = re.compile(r"^(?:smb://|\\\\|//)([^\\/]+)(?:[\\/]+(.*))?$", re.I)


@dataclass(frozen=True)
class Location:
    kind: str          # "local" | "smb"
    server: str = ""   # smb only
    share: str = ""    # smb only
    path: str = ""     # local: absolute path; smb: path inside the share, "" for its root

    def __str__(self) -> str:
        if self.kind == "local":
            return self.path
        return f"smb://{self.server}/{self.share}" + (f"/{self.path}" if self.path else "")

    @property
    def name(self) -> str:
        return posixpath.basename(self.path) or (self.share if self.kind == "smb" else "/")

    def join(self, rel: str) -> "Location":
        rel = rel.replace("\\", "/").strip("/")
        return Location(self.kind, self.server, self.share, posixpath.join(self.path, rel) if self.path else (rel if self.kind == "smb" else "/" + rel))

    def parent(self) -> "Location":
        return Location(self.kind, self.server, self.share, posixpath.dirname(self.path) if self.kind == "smb" else posixpath.dirname(self.path) or "/")

    def under(self, root: "Location") -> bool:
        if (self.kind, self.server.lower(), self.share.lower()) != (root.kind, root.server.lower(), root.share.lower()):
            return False
        if not root.path or root.path == "/":
            return True
        return self.path == root.path or self.path.startswith(root.path.rstrip("/") + "/")


def parse(raw) -> Location:
    text = str(raw or "").strip()
    if not text:
        raise ValueError(say("пустой путь", "an empty path"))
    m = _UNC.match(text)
    if m:
        server, rest = m.group(1).lower(), (m.group(2) or "").replace("\\", "/").strip("/")
        if not rest:
            raise ValueError(say(f"{text}: у сетевого пути нужна общая папка — \\\\сервер\\папка",
                                 f"{text}: a network path needs a share — \\\\server\\share"))
        share, _, inner = rest.partition("/")
        return Location("smb", server, share, posixpath.normpath(inner) if inner else "")
    if text.startswith("/"):
        return Location("local", path=posixpath.normpath(text))
    raise ValueError(say(f"{text}: путь должен быть абсолютным (/srv/docs) или сетевым (\\\\сервер\\папка)",
                         f"{text}: the path must be absolute (/srv/docs) or a network one (\\\\server\\share)"))


class Area:
    def __init__(self, roots, excludes) -> None:
        self.roots: list[Location] = []
        for r in roots or []:
            if not str(r or "").strip():
                continue
            try:
                self.roots.append(parse(r))
            except ValueError as exc:
                raise ValueError(say(f"корень «{r}»: {exc}", f"root “{r}”: {exc}")) from exc
        self.excludes = [str(e).strip() for e in (excludes or []) if str(e or "").strip()]

    def contains(self, loc: Location) -> bool:
        if not self.roots:
            return loc.kind == "local"
        return any(loc.under(r) for r in self.roots)

    def starting_points(self) -> list[Location]:
        return self.roots or [Location("local", path="/")]

    def excluded(self, loc: Location) -> bool:
        """An exclude pattern is tried against the name and against the path
        under the nearest root — so `*.bak`, `.git` and `archive/2019` all
        mean what they look like, at any depth."""
        parts = [p for p in loc.path.split("/") if p]
        rel = "/".join(parts)
        for root in self.roots:
            if loc.under(root) and root.path:
                rel = loc.path[len(root.path.rstrip("/")):].strip("/")
                parts = [p for p in rel.split("/") if p]
                break
        subpaths = {"/".join(parts[i:j]) for i in range(len(parts)) for j in range(i + 1, len(parts) + 1)} | {rel}
        for pat in self.excludes:
            candidates = subpaths if "/" in pat else parts
            if any(fnmatch.fnmatchcase(c, pat) for c in candidates):
                return True
        return False
