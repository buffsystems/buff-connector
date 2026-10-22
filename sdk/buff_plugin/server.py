"""The stdio JSON-RPC loop a plugin runs."""

from __future__ import annotations

import json
import os
import sys
import traceback
from dataclasses import dataclass, field
from typing import Any, Callable, Iterable

HOST_API = 1
PROTOCOL_VERSION = "2026-01-01"


class HostError(RuntimeError):
    """The host refused a request — usually the egress policy, or a credential
    the operator has not configured."""


@dataclass
class Event:
    """One thing that happened in the customer's system.

    `id` must be unique within the source, and `at` is the moment it happened as
    the SOURCE reports it. Both are used for de-duplication upstream, so an event
    replayed after a restart is recognised rather than acted on twice.
    """

    id: str
    at: str
    kind: str
    title: str = ""
    body: str = ""
    author: str = ""
    url: str = ""
    action: str = ""
    extra: dict = field(default_factory=dict)
    # Who asked as the SYSTEM identifies them — the display name in `author` is
    # for people and anyone may change it — so the connection's list of who may
    # ask can be checked (cloud-front#476). Empty when the system does not say.
    author_login: str = ""
    author_email: str = ""
    # How the request was written: "internal" for a note only the team sees.
    # The reply is posted the same way; empty is an ordinary comment.
    visibility: str = ""

    def to_json(self) -> dict:
        return {
            "id": self.id, "at": self.at, "kind": self.kind, "title": self.title,
            "body": self.body, "author": self.author, "url": self.url,
            "action": self.action, "extra": self.extra,
            "author_login": self.author_login, "author_email": self.author_email,
            "visibility": self.visibility,
        }


class Plugin:
    """Declares what a plugin offers and runs its protocol loop."""

    def __init__(self, name: str, host_api: int = HOST_API) -> None:
        """host_api is the connector surface this plugin needs. Leave the default
        unless the plugin uses something newer — a connector that is older than
        the number refuses the plugin by name, which is the right failure: loud,
        at install, with both numbers in the message."""
        self.name = name
        self.host_api = host_api
        self.config: dict = json.loads(os.environ.get("BUFF_CONFIG", "{}"))
        self.instance = os.environ.get("BUFF_INSTANCE", "")
        self.slug = os.environ.get("BUFF_SLUG", "")
        self._tools: dict[str, dict] = {}
        self._poll: Callable[[str | None], Iterable[Event]] | None = None
        self._webhook: Callable[[dict, str], Iterable[Event]] | None = None
        self._reply: Callable[..., Any] | None = None
        self._check: Callable[[], dict] | None = None
        self._next_id = 0
        self._out = sys.stdout
        self._in = sys.stdin

    # ---- declaration ----------------------------------------------------

    def tool(self, description: str, read_only: bool = False, schema: dict | None = None,
             vision: bool = False):
        """Registers a tool.

        read_only is not decoration: a pipeline stage that only reads is given
        read-only tools and nothing else, so a tool that changes anything in the
        customer's system must say so.

        vision marks a tool whose answer is a PICTURE (an MCP image block). Not
        every model can see: the platform withholds such a tool from a text-only
        model instead of handing it a blob it would silently "read". Return the
        content ready-made — {"content": [{"type": "image", "data": ..., "mimeType": ...}]}
        — and it goes out untouched.
        """

        def decorate(fn: Callable[..., Any]) -> Callable[..., Any]:
            self._tools[fn.__name__] = {
                "fn": fn,
                "description": description,
                "read_only": read_only,
                "vision": vision,
                "schema": schema or {"type": "object"},
            }
            return fn

        return decorate

    def poll(self, fn: Callable[[str | None], Iterable[Event]]):
        """Registers the event source. The host owns the schedule, the watermark
        and de-duplication; this returns what happened since `since`."""
        self._poll = fn
        return fn

    def webhook(self, fn: Callable[[dict, str], Iterable[Event]]):
        """Turn a callback the host has already validated into events.

        For a source there is nothing to poll: an internal service that can only
        push, or a change that no longer exists by the time a poll runs. The HOST
        owns the listener, the URL, the secret and the signature — this is handed
        a body that has already been found genuine, and never a socket.

        Declare `webhook: deliver` in the manifest to receive these. Implement
        `poll` as well if the source allows it: callbacks get lost, and the poll
        is what makes that recoverable.
        """
        self._webhook = fn
        return fn

    def reply(self, fn: Callable[..., Any]):
        """Registers how an answer is posted back into the customer's system."""
        self._reply = fn
        return fn

    def check(self, fn: Callable[[], dict]):
        """Registers the connection test the operator presses in setup."""
        self._check = fn
        return fn

    # ---- host services --------------------------------------------------

    def http(self, method: str, url: str, credential: str = "", headers: dict | None = None,
             body: str = "", binary: bool = False, body_bytes: bytes | None = None) -> dict:
        """Asks the HOST to make a request. The plugin never sees the credential.

        binary=True asks for the response as bytes: the answer carries
        `body_base64` instead of `body`. A rendered picture forced through a text
        field arrives corrupt, one byte of invalid UTF-8 at a time.

        body_bytes sends the REQUEST body as bytes, for an upload — the same
        corruption in the other direction otherwise. Needs host_api 3. Give one
        of body and body_bytes, not both.
        """
        params = {
            "method": method, "url": url, "credential": credential,
            "headers": headers or {}, "body": body,
        }
        if body_bytes is not None:
            import base64 as _b64

            params["body"] = ""
            params["body_base64"] = _b64.b64encode(body_bytes).decode("ascii")
        if binary:
            params["binary"] = True
        res = self._call("buff/http", params)
        return res

    def url_allowed(self, url: str) -> tuple[bool, str]:
        """Asks the HOST whether the operator's web lists admit this address —
        the one implementation of those lists, so a search result can be
        dropped before anyone tries to read it. No lookup happens here; the
        public-only check runs when the address is actually fetched.
        Needs host_api 2."""
        res = self._call("buff/url_allowed", {"url": url}) or {}
        return bool(res.get("allowed")), str(res.get("reason") or "")

    def credential(self, name: str) -> dict:
        """Asks the HOST for a credential of the `password` kind — the account
        and the password themselves, for a protocol the host cannot speak on
        the plugin's behalf (a file share over SMB). The manifest must declare
        the credential with kinds [password]; any other kind is refused by the
        host. Needs host_api 5."""
        res = self._call("buff/credential", {"name": name}) or {}
        return {"username": str(res.get("username") or ""), "password": str(res.get("password") or "")}

    def log(self, message: str) -> None:
        """Sends a line to the instance log. Plain prints go to stdout, which is
        the protocol channel — they are ignored there, so use this."""
        try:
            self._notify("buff/log", {"message": message})
        except Exception:
            pass

    # ---- protocol -------------------------------------------------------

    def run(self) -> None:
        for line in self._in:
            line = line.strip()
            if not line or not line.startswith("{"):
                continue
            try:
                msg = json.loads(line)
            except json.JSONDecodeError:
                continue
            if "method" not in msg:
                continue  # a response we are not waiting for
            self._dispatch(msg)

    def _dispatch(self, msg: dict) -> None:
        mid, method, params = msg.get("id"), msg["method"], msg.get("params") or {}
        try:
            result = self._handle(method, params)
        except Exception as exc:  # a plugin bug must not kill the channel
            if mid is not None:
                self._send({"jsonrpc": "2.0", "id": mid, "error": {
                    "code": -32000, "message": f"{type(exc).__name__}: {exc}",
                    "data": traceback.format_exc()[-2000:],
                }})
            return
        if mid is not None:
            self._send({"jsonrpc": "2.0", "id": mid, "result": result})

    def _handle(self, method: str, params: dict) -> Any:
        if method == "initialize":
            return {
                "protocolVersion": PROTOCOL_VERSION,
                "hostApi": self.host_api,
                "serverInfo": {"name": self.name, "version": params.get("version", "")},
                "capabilities": {
                    "tools": {},
                    **({"buff/poll": {}} if self._poll else {}),
                    **({"buff/webhook": {}} if self._webhook else {}),
                    **({"buff/reply": {}} if self._reply else {}),
                    **({"buff/check": {}} if self._check else {}),
                },
            }
        if method == "tools/list":
            return {"tools": [{
                "name": name,
                "description": t["description"],
                "inputSchema": t["schema"],
                "annotations": {"readOnlyHint": t["read_only"], "visionHint": t["vision"]},
            } for name, t in sorted(self._tools.items())]}
        if method == "tools/call":
            name = params.get("name", "")
            if name not in self._tools:
                raise KeyError(f"no such tool: {name}")
            out = self._tools[name]["fn"](**(params.get("arguments") or {}))
            if isinstance(out, dict) and "content" in out:
                return out
            return {"content": [{"type": "text", "text": out if isinstance(out, str) else json.dumps(out, ensure_ascii=False)}]}
        if method == "buff/webhook":
            if not self._webhook:
                raise RuntimeError("этот плагин не разбирает вебхуки")
            events = list(self._webhook(params.get("headers") or {}, params.get("body") or ""))
            return {"events": [e.to_json() for e in events]}
        if method == "buff/poll":
            if not self._poll:
                raise NotImplementedError("this plugin has no event source")
            events = list(self._poll(params.get("since")))
            return {"events": [e.to_json() for e in events]}
        if method == "buff/reply":
            if not self._reply:
                raise NotImplementedError("this plugin cannot post replies")
            return self._reply(**params) or {}
        if method == "buff/check":
            if not self._check:
                return {"ok": True, "detail": "no connection test implemented"}
            return self._check()
        if method == "buff/describe":
            return {"name": self.name, "instance": self.instance, "slug": self.slug}
        raise NotImplementedError(f"unknown method: {method}")

    def _call(self, method: str, params: dict) -> Any:
        """Calls the host and waits for the answer, serving anything the host
        asks in the meantime so a nested request cannot deadlock."""
        self._next_id += 1
        want = self._next_id
        self._send({"jsonrpc": "2.0", "id": want, "method": method, "params": params})
        for line in self._in:
            line = line.strip()
            if not line or not line.startswith("{"):
                continue
            try:
                msg = json.loads(line)
            except json.JSONDecodeError:
                continue
            if "method" in msg:
                self._dispatch(msg)
                continue
            if msg.get("id") == want:
                if msg.get("error"):
                    raise HostError(msg["error"].get("message", "host refused the request"))
                return msg.get("result")
        raise HostError("the host closed the channel")

    def _notify(self, method: str, params: dict) -> None:
        self._send({"jsonrpc": "2.0", "method": method, "params": params})

    def _send(self, msg: dict) -> None:
        self._out.write(json.dumps(msg, ensure_ascii=False) + "\n")
        self._out.flush()
