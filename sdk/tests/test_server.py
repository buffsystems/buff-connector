"""Contract tests for the plugin SDK, driven through the real pipes."""

import io
import json
import os
import sys
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from buff_plugin import Event, HostError, Plugin  # noqa: E402


def drive(plugin: Plugin, messages: list[dict]) -> list[dict]:
    """Feeds the plugin a script of host messages and returns what it wrote."""
    plugin._in = io.StringIO("".join(json.dumps(m) + "\n" for m in messages))
    plugin._out = io.StringIO()
    plugin.run()
    out = []
    for line in plugin._out.getvalue().splitlines():
        if line.strip():
            out.append(json.loads(line))
    return out


def build() -> Plugin:
    p = Plugin("demo")

    @p.tool(description="Search tickets. Use when a task mentions a ticket key.", read_only=True)
    def search(q: str = ""):
        return {"found": q}

    @p.tool(description="Create a ticket.")
    def create(title: str = ""):
        return "created " + title

    @p.tool(description="Render a frame as PNG. Only for a model that can see images.",
            read_only=True, vision=True)
    def render(node: str = ""):
        return {"content": [{"type": "image", "data": "aGk=", "mimeType": "image/png"}]}

    @p.poll
    def poll(since):
        return [Event(id="1", at="2026-01-01T00:00:00Z", kind="comment", body="hi", action="ask")]

    @p.check
    def check():
        return {"ok": True, "detail": "connected as bot"}

    return p


class TestHandshake(unittest.TestCase):
    def test_initialize_announces_host_api_and_capabilities(self):
        out = drive(build(), [{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {}}])
        self.assertEqual(len(out), 1)
        res = out[0]["result"]
        self.assertEqual(res["hostApi"], 1)
        self.assertIn("buff/poll", res["capabilities"])
        self.assertIn("buff/check", res["capabilities"])
        self.assertNotIn("buff/reply", res["capabilities"],
                         "a capability must not be announced when it is not implemented")

    def test_a_plugin_may_ask_for_a_newer_host(self):
        out = drive(Plugin("web", host_api=2),
                    [{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {}}])
        self.assertEqual(out[0]["result"]["hostApi"], 2)

    def test_url_allowed_asks_the_host_and_reads_its_verdict(self):
        p = Plugin("web", host_api=2)
        p._in = io.StringIO(
            json.dumps({"jsonrpc": "2.0", "id": 1,
                        "result": {"allowed": False, "reason": "pastebin.com is on the blocked list"}}) + "\n")
        p._out = io.StringIO()
        ok, why = p.url_allowed("https://pastebin.com/x")
        self.assertFalse(ok)
        self.assertIn("blocked", why)
        sent = json.loads(p._out.getvalue().splitlines()[0])
        self.assertEqual(sent["method"], "buff/url_allowed")
        self.assertEqual(sent["params"], {"url": "https://pastebin.com/x"})

    def test_tools_list_carries_the_read_only_flag(self):
        out = drive(build(), [{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}])
        tools = {t["name"]: t for t in out[0]["result"]["tools"]}
        self.assertTrue(tools["search"]["annotations"]["readOnlyHint"])
        self.assertFalse(tools["create"]["annotations"]["readOnlyHint"],
                         "a tool that writes into the customer's system must not claim read-only")

    def test_tools_list_carries_the_vision_flag(self):
        # A tool whose answer is a picture is useless to a text-only model, and worse
        # than useless: the model gets a blob it cannot read and answers as if it had.
        # The flag lets the platform withhold it from a model that cannot see.
        out = drive(build(), [{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}])
        tools = {t["name"]: t for t in out[0]["result"]["tools"]}
        self.assertTrue(tools["render"]["annotations"]["visionHint"])
        self.assertFalse(tools["search"]["annotations"]["visionHint"],
                         "a tool that answers in text must not be withheld from a text-only model")


class TestCalls(unittest.TestCase):
    def test_tool_may_answer_with_ready_mcp_content(self):
        # A picture cannot be JSON-dumped into a text block: a tool that already
        # speaks MCP content (image, resource) is passed through untouched.
        out = drive(build(), [{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
                               "params": {"name": "render", "arguments": {"node": "1:2"}}}])
        content = out[0]["result"]["content"]
        self.assertEqual(content[0]["type"], "image")
        self.assertEqual(content[0]["mimeType"], "image/png")

    def test_tool_result_is_wrapped_as_mcp_content(self):
        out = drive(build(), [{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
                               "params": {"name": "search", "arguments": {"q": "PROJ-1"}}}])
        text = out[0]["result"]["content"][0]["text"]
        self.assertIn("PROJ-1", text)

    def test_a_failing_tool_answers_with_an_error_and_keeps_the_channel(self):
        p = build()
        out = drive(p, [
            {"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": {"name": "nope"}},
            {"jsonrpc": "2.0", "id": 2, "method": "tools/list"},
        ])
        self.assertIn("error", out[0])
        self.assertIn("no such tool", out[0]["error"]["message"])
        self.assertIn("result", out[1], "the channel must survive a tool error")

    def test_poll_returns_events_in_the_shared_shape(self):
        out = drive(build(), [{"jsonrpc": "2.0", "id": 1, "method": "buff/poll",
                               "params": {"since": None}}])
        ev = out[0]["result"]["events"][0]
        self.assertEqual(ev["id"], "1")
        self.assertEqual(ev["action"], "ask")

    def test_check_reports_the_connection(self):
        out = drive(build(), [{"jsonrpc": "2.0", "id": 1, "method": "buff/check"}])
        self.assertTrue(out[0]["result"]["ok"])


class TestHostCallbacks(unittest.TestCase):
    def test_http_goes_through_the_host_and_returns_its_answer(self):
        p = Plugin("demo")

        @p.tool(description="fetch", read_only=True)
        def fetch():
            return p.http("GET", "https://jira.example.com/rest/api/2/myself", credential="api")

        # The host answers the plugin's buff/http request, then asks for nothing
        # more. Both messages arrive on the same pipe, in order.
        p._in = io.StringIO(
            json.dumps({"jsonrpc": "2.0", "id": 1, "method": "tools/call",
                        "params": {"name": "fetch"}}) + "\n" +
            json.dumps({"jsonrpc": "2.0", "id": 1, "result": {"status": 200, "body": "{}"}}) + "\n")
        p._out = io.StringIO()
        p.run()

        lines = [json.loads(l) for l in p._out.getvalue().splitlines() if l.strip()]
        self.assertEqual(lines[0]["method"], "buff/http")
        self.assertEqual(lines[0]["params"]["credential"], "api",
                         "the plugin names a credential; it never carries one")
        self.assertNotIn("token", json.dumps(lines[0]).lower())
        self.assertIn("200", json.dumps(lines[1]))

    def test_a_refused_request_raises_rather_than_returning_nonsense(self):
        p = Plugin("demo")
        p._in = io.StringIO(
            json.dumps({"jsonrpc": "2.0", "id": 1,
                        "error": {"code": -32000, "message": "buff/http: evil.example.net is not allowed"}}) + "\n")
        p._out = io.StringIO()
        with self.assertRaises(HostError) as ctx:
            p.http("GET", "https://evil.example.net/")
        self.assertIn("not allowed", str(ctx.exception))


class TestHostHTTP(unittest.TestCase):
    def test_binary_is_asked_for_only_when_wanted(self):
        # A picture forced through a text field arrives corrupt; text asked for as
        # binary would make every plugin decode base64 for nothing. The flag
        # travels only when set.
        p = Plugin("demo")
        sent = []
        p._call = lambda method, params: sent.append((method, params)) or {"status": 200, "body_base64": "iVBORw0="}
        res = p.http("GET", "https://x.example/renders/1.png", credential="api", binary=True)
        self.assertEqual(sent[0][0], "buff/http")
        self.assertTrue(sent[0][1]["binary"])
        self.assertEqual(res["body_base64"], "iVBORw0=")
        p.http("GET", "https://x.example/v1/me", credential="api")
        self.assertNotIn("binary", sent[1][1])


class TestRobustness(unittest.TestCase):
    def test_noise_on_the_input_is_ignored(self):
        out = drive(build(), [{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}])
        self.assertEqual(len(out), 1)

        p = build()
        p._in = io.StringIO("hello\nnot json\n" +
                            json.dumps({"jsonrpc": "2.0", "id": 1, "method": "buff/check"}) + "\n")
        p._out = io.StringIO()
        p.run()
        self.assertIn("result", json.loads(p._out.getvalue().strip()))

    def test_unknown_method_is_an_error_not_a_crash(self):
        out = drive(build(), [
            {"jsonrpc": "2.0", "id": 1, "method": "buff/whatever"},
            {"jsonrpc": "2.0", "id": 2, "method": "buff/check"},
        ])
        self.assertIn("error", out[0])
        self.assertIn("result", out[1])



class CredentialTests(unittest.TestCase):
    """buff/credential — the one host service that hands a secret over, for a
    protocol the host cannot speak on the plugin's behalf."""

    def test_credential_is_asked_from_the_host_and_returned_whole(self):
        p = Plugin("files", host_api=5)
        p._in = io.StringIO(
            json.dumps({"jsonrpc": "2.0", "id": 1,
                        "result": {"username": "CORP\\svc", "password": "hunter2"}}) + "\n")
        p._out = io.StringIO()
        got = p.credential("share")
        self.assertEqual(got, {"username": "CORP\\svc", "password": "hunter2"})
        sent = json.loads(p._out.getvalue().splitlines()[0])
        self.assertEqual(sent["method"], "buff/credential")
        self.assertEqual(sent["params"], {"name": "share"})

    def test_a_refused_credential_raises(self):
        p = Plugin("files", host_api=5)
        p._in = io.StringIO(
            json.dumps({"jsonrpc": "2.0", "id": 1,
                        "error": {"code": -32000, "message": "credential api is not handed to plugins"}}) + "\n")
        p._out = io.StringIO()
        with self.assertRaises(HostError):
            p.credential("api")



class TestEventCarriesTheAsker(unittest.TestCase):
    """Who asked travels as the system knows them, not only as a display name
    anyone can set; and how the request was written, so the reply keeps it
    (cloud-front#476). Both are optional: a system that hides an address sends
    nothing, and an ordinary comment has no visibility."""

    def test_login_email_and_visibility_reach_the_host(self):
        e = Event(id="c:1", at="2026-10-05T10:00:00Z", kind="comment", author="Пётр",
                  author_login="petrov", author_email="petrov@company.ru", visibility="internal")
        j = e.to_json()
        self.assertEqual((j["author_login"], j["author_email"], j["visibility"]),
                         ("petrov", "petrov@company.ru", "internal"))

    def test_an_older_event_still_serialises(self):
        j = Event(id="c:2", at="2026-10-05T10:00:00Z", kind="comment").to_json()
        self.assertEqual((j["author_login"], j["author_email"], j["visibility"]), ("", "", ""))


if __name__ == "__main__":
    unittest.main()
