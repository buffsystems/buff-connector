"""Demonstration plugin: the smallest thing that exercises the whole contract."""

from buff_plugin import Event, Plugin

plugin = Plugin("demo")


@plugin.tool(description="Echo a value back. Use when verifying the channel end to end.",
             read_only=True,
             schema={"type": "object", "properties": {"value": {"type": "string"}},
                     "required": ["value"], "additionalProperties": False})
def echo(value: str = ""):
    return {"echoed": value, "base_url": plugin.config.get("base_url", "")}


@plugin.tool(description="Fetch a path from the configured system through the host.",
             read_only=True,
             schema={"type": "object", "properties": {"path": {"type": "string"}},
                     "required": ["path"], "additionalProperties": False})
def fetch(path: str = "/"):
    res = plugin.http("GET", plugin.config["base_url"].rstrip("/") + path, credential="api")
    return {"status": res["status"], "body": res["body"]}


@plugin.check
def check():
    return {"ok": True, "detail": "demo plugin ready"}


@plugin.poll
def poll(since):
    return [Event(id="demo-1", at="2026-01-01T00:00:00Z", kind="comment",
                  title="demo", body="hello from the demo plugin", action="ask")]


if __name__ == "__main__":
    plugin.run()
