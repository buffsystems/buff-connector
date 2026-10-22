"""Tests for the web plugin (#300).

The DuckDuckGo fixtures are LIVE captures from html.duckduckgo.com: a result
page, and the "select all squares containing a duck" page the same endpoint
answers with under load (status 202, no results). The Brave and SearXNG
fixtures follow their published response shapes.
"""

import io
import json
import os
import sys
import unittest

HERE = os.path.dirname(__file__)
sys.path.insert(0, os.path.join(HERE, "..", "..", "..", "sdk"))
os.environ["BUFF_CONFIG"] = json.dumps({
    "engine": "duckduckgo",
    "duckduckgo_base": "https://html.duckduckgo.com",
    "brave_base": "https://api.search.brave.com",
    "stop_words": ["casino", "Зарплаты"],
})
sys.path.insert(0, os.path.join(HERE, ".."))

import engines  # noqa: E402
import main  # noqa: E402
from page import page_text  # noqa: E402


def fixture(name: str) -> str:
    with open(os.path.join(HERE, "fixtures", name), encoding="utf-8") as f:
        return f.read()


class TestDuckDuckGo(unittest.TestCase):
    def test_live_result_page_becomes_a_list_with_real_addresses(self):
        results = engines.parse_duckduckgo(200, fixture("ddg-results.html"))
        self.assertEqual(len(results), 10)
        first = results[0]
        self.assertEqual(first["url"], "https://www.golinuxcloud.com/golang-http-client-timeout/",
                         "the redirect wrapper must be unwrapped to the real address")
        self.assertIn("Golang http client timeout", first["title"])
        self.assertTrue(first["snippet"].startswith("Golang http client timeout"))
        self.assertNotIn("<b>", first["snippet"], "highlighting markup is not text")
        for r in results:
            self.assertTrue(r["url"].startswith("http"), r)

    def test_the_challenge_page_is_named_not_reported_as_nothing_found(self):
        with self.assertRaises(engines.Challenge) as ctx:
            engines.parse_duckduckgo(202, fixture("ddg-challenge.html"))
        self.assertIn("SearXNG", str(ctx.exception))
        self.assertIn("Brave", str(ctx.exception))

    def test_unwrap(self):
        self.assertEqual(engines.unwrap_duckduckgo(
            "//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fa%3Fb%3D1&rut=abc"),
            "https://example.com/a?b=1")
        self.assertEqual(engines.unwrap_duckduckgo("https://example.com/direct"), "https://example.com/direct")


class TestBrave(unittest.TestCase):
    def test_web_results_are_read(self):
        body = json.dumps({"query": {"original": "go http timeout"}, "web": {"results": [
            {"title": "net/http", "url": "https://pkg.go.dev/net/http", "description": "Package http…",
             "age": "March 1, 2026", "page_age": "2026-03-01T00:00:00"},
            {"title": "No age", "url": "https://example.com/x", "description": "d"},
        ]}})
        results = engines.parse_brave(200, body)
        self.assertEqual([r["url"] for r in results], ["https://pkg.go.dev/net/http", "https://example.com/x"])
        self.assertEqual(results[0]["published"], "March 1, 2026")
        self.assertNotIn("published", results[1])

    def test_an_error_carries_braves_reason(self):
        body = json.dumps({"type": "ErrorResponse", "error": {"id": "x", "status": 422,
                                                               "detail": "Invalid subscription token"}})
        with self.assertRaises(RuntimeError) as ctx:
            engines.parse_brave(422, body)
        self.assertIn("Invalid subscription token", str(ctx.exception))


class TestSearXNG(unittest.TestCase):
    def test_results_are_read(self):
        body = json.dumps({"query": "go", "number_of_results": 0, "results": [
            {"title": "Go", "url": "https://go.dev/", "content": "Build simple…", "engine": "duckduckgo",
             "publishedDate": None, "score": 1.0},
            {"title": "Wiki", "url": "https://en.wikipedia.org/wiki/Go", "content": "…",
             "engine": "wikipedia", "publishedDate": "2026-01-02T00:00:00", "score": 0.5},
        ], "suggestions": []})
        results = engines.parse_searxng(200, body)
        self.assertEqual(results[0], {"title": "Go", "url": "https://go.dev/", "snippet": "Build simple…",
                                      "engine": "duckduckgo"})
        self.assertEqual(results[1]["published"], "2026-01-02T00:00:00")

    def test_forbidden_points_at_the_json_format_setting(self):
        with self.assertRaises(RuntimeError) as ctx:
            engines.parse_searxng(403, "")
        self.assertIn("search.formats", str(ctx.exception))


class TestPage(unittest.TestCase):
    def test_html_keeps_title_and_structure(self):
        title, text = page_text("text/html; charset=utf-8", """<html><head><title> Go &amp; HTTP  guide </title>
        <script>var x = 1;</script></head><body><h1>Timeouts</h1><ul><li>client</li><li>server</li></ul>
        <p>See <a href="https://pkg.go.dev/net/http">the package</a>.</p></body></html>""")
        self.assertEqual(title, "Go & HTTP guide")
        self.assertIn("Timeouts", text)
        self.assertIn("- client", text)
        self.assertIn("https://pkg.go.dev/net/http", text)
        self.assertNotIn("var x", text)

    def test_json_is_pretty_printed_and_the_rest_passes_through(self):
        _, text = page_text("application/json", '{"a":[1,2]}')
        self.assertEqual(text, '{\n  "a": [\n    1,\n    2\n  ]\n}')
        _, text = page_text("text/plain", "as is\n")
        self.assertEqual(text, "as is\n")


class FakeHost:
    """Stands in for the connector: answers buff/http from a script and applies
    a deny list the way the real host does."""

    def __init__(self, pages: dict, denied=()):
        self.pages, self.denied, self.calls = pages, denied, []

    def http(self, method, url, credential="", headers=None, body="", binary=False):
        self.calls.append((method, url, credential, headers or {}))
        if any(d in url for d in self.denied):
            raise main.HostError(f"buff/http: {url} is on the blocked list")
        if url not in self.pages:
            raise main.HostError(f"buff/http: {url} is not allowed for this instance")
        return self.pages[url]

    def url_allowed(self, url):
        if any(d in url for d in self.denied):
            return False, "blocked"
        return True, ""


def use(host: FakeHost):
    main.plugin.http = host.http
    main.plugin.url_allowed = host.url_allowed


class TestTools(unittest.TestCase):
    def test_search_hides_what_the_operator_excluded_and_says_how_many(self):
        host = FakeHost({"https://html.duckduckgo.com/html/?q=golang%20http%20timeout":
                         {"status": 200, "headers": {}, "body": fixture("ddg-results.html")}},
                        denied=["golinuxcloud.com"])
        use(host)
        out = main.web_search("golang http timeout")
        self.assertEqual(out["engine"], "duckduckgo")
        self.assertEqual(out["hidden"], 1)
        self.assertEqual(len(out["results"]), 9)
        self.assertFalse(any("golinuxcloud" in r["url"] for r in out["results"]))
        method, url, credential, headers = host.calls[0]
        self.assertEqual(credential, "", "DuckDuckGo needs no key")
        self.assertIn("buff-connector", headers["User-Agent"])

    def test_stop_words_refuse_on_the_spot_case_insensitively(self):
        host = FakeHost({})
        use(host)
        with self.assertRaises(RuntimeError) as ctx:
            main.web_search("best online CASINO")
        self.assertIn("casino", str(ctx.exception))
        with self.assertRaises(RuntimeError):
            main.web_fetch("https://example.com/зарплаты/2026")
        self.assertEqual(host.calls, [], "nothing went outside")

    def test_fetch_walks_redirects_one_checked_hop_at_a_time(self):
        host = FakeHost({
            "https://example.com/old": {"status": 301, "headers": {"Location": "/new"}, "body": ""},
            "https://example.com/new": {"status": 200, "headers": {"Content-Type": "text/html; charset=utf-8"},
                                        "body": "<title>New</title><p>moved here</p>"},
        })
        use(host)
        out = main.web_fetch("https://example.com/old")
        self.assertEqual(out["final_url"], "https://example.com/new")
        self.assertEqual(out["title"], "New")
        self.assertEqual(out["status"], 200)
        self.assertEqual(out["content_type"], "text/html")
        self.assertIn("moved here", out["text"])
        self.assertEqual([c[1] for c in host.calls], ["https://example.com/old", "https://example.com/new"],
                         "each hop is its own request the host sees")

    def test_a_redirect_into_a_blocked_site_is_the_hosts_refusal(self):
        host = FakeHost({"https://example.com/go": {"status": 302, "headers": {"Location": "https://pastebin.com/x"},
                                                    "body": ""}}, denied=["pastebin.com"])
        use(host)
        with self.assertRaises(RuntimeError) as ctx:
            main.web_fetch("https://example.com/go")
        self.assertIn("коннектор отказал", str(ctx.exception))
        self.assertIn("blocked", str(ctx.exception))

    def test_a_loop_is_named_as_a_loop(self):
        host = FakeHost({"https://example.com/a": {"status": 302, "headers": {"Location": "/b"}, "body": ""},
                         "https://example.com/b": {"status": 302, "headers": {"Location": "/a"}, "body": ""}})
        use(host)
        with self.assertRaises(RuntimeError) as ctx:
            main.web_fetch("https://example.com/a")
        self.assertIn("петл", str(ctx.exception))

    def test_an_error_page_is_returned_with_its_status_not_hidden(self):
        host = FakeHost({"https://example.com/missing": {"status": 404, "headers": {"Content-Type": "text/html"},
                                                         "body": "<title>Not found</title>gone"}})
        use(host)
        out = main.web_fetch("https://example.com/missing")
        self.assertEqual(out["status"], 404)
        self.assertIn("gone", out["text"])

    def test_brave_and_searxng_requests_take_their_own_shape(self):
        self.assertEqual(main.search_url("brave", "a b"),
                         "https://api.search.brave.com/res/v1/web/search?q=a%20b&count=20")
        main.BASES["searxng"] = "https://search.corp.local"
        self.assertEqual(main.search_url("searxng", "a&b"),
                         "https://search.corp.local/search?q=a%26b&format=json")
        with self.assertRaises(RuntimeError):
            main.BASES["searxng"] = ""
            main.search_url("searxng", "x")


if __name__ == "__main__":
    unittest.main()
