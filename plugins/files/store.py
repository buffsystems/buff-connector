"""Two places a file can be, behind one shape: this machine's filesystem and
an SMB share. Each answers the same four questions — what is at a path, what
is in a folder, the bytes of a file, every file under a folder — so the tools
never ask which one they are talking to.

The share client is pysmb, bundled with the plugin, imported at start so a
bundle that lacks it fails loudly then and not in the middle of a question;
the account and password come from the host through buff/credential the
first time a server is dialled.
Symbolic links on the local side are not followed into: a link inside the
area pointing outside it would otherwise be a door out of the area."""

from __future__ import annotations

import io
import os
import stat as statmod
from dataclasses import dataclass
from typing import Callable, Iterator

from area import Location
from smb.smb_structs import OperationFailure
from smb2first import Smb2Connection


@dataclass(frozen=True)
class Entry:
    name: str
    is_dir: bool
    size: int
    mtime: float  # seconds since the epoch


class Missing(Exception):
    """Nothing at that path."""


class Local:
    def stat(self, loc: Location) -> Entry:
        try:
            st = os.lstat(loc.path)
        except FileNotFoundError as exc:
            raise Missing(str(loc)) from exc
        if statmod.S_ISLNK(st.st_mode):
            st = os.stat(loc.path)
        return Entry(loc.name, statmod.S_ISDIR(st.st_mode), st.st_size, st.st_mtime)

    def list(self, loc: Location) -> list[Entry]:
        try:
            with os.scandir(loc.path) as it:
                out = []
                for e in it:
                    try:
                        st = e.stat(follow_symlinks=True)
                        out.append(Entry(e.name, e.is_dir(follow_symlinks=True), st.st_size, st.st_mtime))
                    except OSError:
                        out.append(Entry(e.name, False, 0, 0.0))
        except FileNotFoundError as exc:
            raise Missing(str(loc)) from exc
        return sorted(out, key=lambda e: e.name)

    def read(self, loc: Location) -> bytes:
        try:
            with open(loc.path, "rb") as f:
                return f.read()
        except FileNotFoundError as exc:
            raise Missing(str(loc)) from exc

    def realpath(self, loc: Location) -> Location:
        return Location("local", path=os.path.realpath(loc.path))

    def walk(self, loc: Location, skip: Callable[[Location, Entry], bool]) -> Iterator[tuple[Location, Entry]]:
        """Every file under loc, depth first, folders in name order — or loc
        itself when it is a file (a root may name one file). `skip` says
        which entries (files or whole folders) are left out."""
        try:
            entries = self.list(loc)
        except NotADirectoryError:
            yield loc, self.stat(loc)
            return
        except (Missing, PermissionError):
            return
        for e in entries:
            child = loc.join(e.name)
            if skip(child, e):
                continue
            if os.path.islink(child.path):
                continue
            if e.is_dir:
                yield from self.walk(child, skip)
            else:
                yield child, e


class Smb:
    def __init__(self, credential: Callable[[], dict]) -> None:
        self._credential = credential
        self._conns: dict[str, object] = {}

    def _conn(self, server: str):
        conn = self._conns.get(server)
        if conn is not None:
            return conn
        cred = self._credential()
        user, domain = _split_account(cred.get("username", ""))
        conn = Smb2Connection(user, cred.get("password", ""), "buff-connector", server, domain=domain,
                              use_ntlm_v2=True, is_direct_tcp=True)
        if not conn.connect(server, 445):
            raise ConnectionError(f"{server}: сервер отказал во входе (проверьте учётную запись и пароль)")
        self._conns[server] = conn
        return conn

    def _call(self, loc: Location, fn):
        """Runs fn(conn) and, if the connection has gone stale, once more on a
        fresh one — a share that dropped an idle session is not an error."""
        try:
            return fn(self._conn(loc.server))
        except (OSError, ConnectionError, EOFError):
            self._conns.pop(loc.server, None)
            return fn(self._conn(loc.server))

    def stat(self, loc: Location) -> Entry:
        def go(conn):
            try:
                f = conn.getAttributes(loc.share, "/" + loc.path)
            except OperationFailure as exc:
                raise Missing(str(loc)) from exc
            return Entry(loc.name, f.isDirectory, f.file_size, f.last_write_time)
        return self._call(loc, go)

    def list(self, loc: Location) -> list[Entry]:
        def go(conn):
            try:
                files = conn.listPath(loc.share, "/" + loc.path)
            except OperationFailure as exc:
                raise Missing(str(loc)) from exc
            return sorted((Entry(f.filename, f.isDirectory, f.file_size, f.last_write_time)
                           for f in files if f.filename not in (".", "..")), key=lambda e: e.name)
        return self._call(loc, go)

    def read(self, loc: Location) -> bytes:
        def go(conn):
            buf = io.BytesIO()
            try:
                conn.retrieveFile(loc.share, "/" + loc.path, buf)
            except OperationFailure as exc:
                raise Missing(str(loc)) from exc
            return buf.getvalue()
        return self._call(loc, go)

    def realpath(self, loc: Location) -> Location:
        return loc

    def walk(self, loc: Location, skip: Callable[[Location, Entry], bool]) -> Iterator[tuple[Location, Entry]]:
        try:
            entry = self.stat(loc)
        except Missing:
            return
        if not entry.is_dir:
            yield loc, entry
            return
        entries = self.list(loc)
        for e in entries:
            child = loc.join(e.name)
            if skip(child, e):
                continue
            if e.is_dir:
                yield from self.walk(child, skip)
            else:
                yield child, e

    def close(self) -> None:
        for conn in self._conns.values():
            try:
                conn.close()
            except Exception:
                pass
        self._conns.clear()


def _split_account(account: str) -> tuple[str, str]:
    """`DOMAIN\\user` or `user@domain` → (user, domain); a bare user has none."""
    if "\\" in account:
        domain, _, user = account.partition("\\")
        return user, domain
    if "@" in account:
        user, _, domain = account.partition("@")
        return user, domain
    return account, ""
