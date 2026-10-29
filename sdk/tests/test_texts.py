"""A plugin's tool words come from its files, one per language, and each listing
and each call is answered in the language the connector names (cloud-front#718)."""

import io
import json
import os
import sys
import unittest
from unittest import mock

HERE = os.path.dirname(__file__)
sys.path.insert(0, os.path.join(HERE, ".."))

from buff_plugin import Plugin, say  # noqa: E402
from buff_plugin.texts import TextsError, describe, parse  # noqa: E402

WORDED = os.path.join(HERE, "fixtures", "worded")
SCHEMA = {"type": "object", "properties": {"q": {"type": "string"}}, "required": ["q"]}


def drive(plugin, messages):
    plugin._in = io.StringIO("".join(json.dumps(m) + "\n" for m in messages))
    plugin._out = io.StringIO()
    plugin.run()
    return [json.loads(line) for line in plugin._out.getvalue().splitlines() if line.strip()]


def worded() -> Plugin:
    p = Plugin("demo", texts_dir=WORDED)

    @p.tool(read_only=True, schema=SCHEMA)
    def search(q: str = ""):
        if not q:
            raise RuntimeError(say("пустой запрос", "an empty query"))
        return {"found": q}

    @p.tool()
    def create(title: str = ""):
        return "created"

    return p


def listing(p, lang=None):
    params = {"_meta": {"buff/lang": lang}} if lang else {}
    out = drive(p, [{"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": params}])
    return {t["name"]: t for t in out[0]["result"]["tools"]}


class TestFormat(unittest.TestCase):
    def test_paragraphs_and_wrapping(self):
        got = parse("note\n# t\nFirst line,\nshown alone.\n\nThe rest.\n\n## p\nparam\ntext\n")
        self.assertEqual(got["t"]["description"], "First line, shown alone.\nThe rest.")
        self.assertEqual(got["t"]["params"], {"p": "param text"})

    def test_refuses_what_the_connector_refuses(self):
        for text in ("## p\nx\n", "# a\nx\n# a\ny\n", "# a\nx\n## p\ny\n## p\nz\n",
                     "# a\n## p\ny\n", "# a\nx\n## p\n", "# two words\nx\n", "only a note\n"):
            with self.assertRaises(TextsError, msg=text):
                parse(text)

    def test_describe_fills_the_schema_and_refuses_a_stranger(self):
        got = describe(SCHEMA, {"q": "what"})
        self.assertEqual(got["properties"]["q"], {"type": "string", "description": "what"})
        self.assertNotIn("description", SCHEMA["properties"]["q"], "the code's schema is not changed")
        with self.assertRaises(TextsError):
            describe(SCHEMA, {"gone": "x"})


class TestListing(unittest.TestCase):
    def test_each_language_as_asked(self):
        en, ru = listing(worded(), "en"), listing(worded(), "ru")
        self.assertEqual(en["search"]["description"],
                         "Find issues by text. Use it when an issue key is mentioned.\nReturns keys and titles.")
        self.assertEqual(en["search"]["inputSchema"]["properties"]["q"]["description"], "what to search for")
        self.assertEqual(ru["search"]["inputSchema"]["properties"]["q"]["description"], "что искать")
        self.assertEqual(ru["create"]["description"], "Создать задачу.")
        self.assertTrue(en["search"]["annotations"]["readOnlyHint"])

    def test_unnamed_is_the_connectors_language(self):
        with mock.patch.dict(os.environ, {"BUFF_LANG": "en"}):
            self.assertEqual(listing(worded())["create"]["description"], "Create an issue.")
        with mock.patch.dict(os.environ, {"BUFF_LANG": ""}):
            self.assertEqual(listing(worded())["create"]["description"], "Создать задачу.")

    def test_a_plugin_that_words_its_tools_in_code_still_lists(self):
        p = Plugin("old", texts_dir=os.path.join(HERE, "no-such-dir"))

        @p.tool(description="Search.", schema=SCHEMA)
        def search(q: str = ""):
            return q

        self.assertEqual(listing(p, "en")["search"]["description"], "Search.")


class TestLoadingFailsLoud(unittest.TestCase):
    def test_a_tool_the_files_do_not_describe(self):
        p = Plugin("demo", texts_dir=WORDED)
        with self.assertRaises(TextsError):
            @p.tool()
            def undescribed():
                return ""

    def test_a_parameter_the_schema_lacks(self):
        p = Plugin("demo", texts_dir=WORDED)
        with self.assertRaises(TextsError):
            @p.tool(schema={"type": "object", "properties": {}})
            def search():
                return ""

    def test_no_words_at_all(self):
        p = Plugin("demo", texts_dir=os.path.join(HERE, "no-such-dir"))
        with self.assertRaises(TextsError):
            @p.tool()
            def mute():
                return ""


class TestFill(unittest.TestCase):
    """A value only the running plugin knows — the connection's area — fills a {{name}},
    said in the listing's language."""

    def test_a_placeholder_is_filled_in_the_listings_language(self):
        import tempfile
        with tempfile.TemporaryDirectory() as d:
            for lang, text in (("ru", "Найти. Область: {{area}}."), ("en", "Find. Area: {{area}}.")):
                with open(os.path.join(d, f"tools.{lang}.md"), "w", encoding="utf-8") as f:
                    f.write(f"# find\n{text}\n")
            p = Plugin("demo", texts_dir=d)

            @p.tool(fill={"area": lambda: say("весь диск", "the whole disk")})
            def find():
                return ""

            self.assertEqual(listing(p, "en")["find"]["description"], "Find. Area: the whole disk.")
            self.assertEqual(listing(p, "ru")["find"]["description"], "Найти. Область: весь диск.")

            with self.assertRaises(TextsError):
                @p.tool()
                def find():  # noqa: F811 — the same words, nothing to fill them with
                    return ""


class TestCallLanguage(unittest.TestCase):
    def call(self, lang=None):
        params = {"name": "search", "arguments": {}}
        if lang:
            params["_meta"] = {"buff/lang": lang}
        return drive(worded(), [{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": params}])[0]

    def test_a_refusal_in_the_calls_language(self):
        with mock.patch.dict(os.environ, {"BUFF_LANG": "ru"}):
            self.assertIn("an empty query", self.call("en")["error"]["message"])
            self.assertIn("пустой запрос", self.call("ru")["error"]["message"])
            self.assertIn("пустой запрос", self.call()["error"]["message"])
        with mock.patch.dict(os.environ, {"BUFF_LANG": "en"}):
            self.assertIn("an empty query", self.call()["error"]["message"])

    def test_the_language_ends_with_the_call(self):
        with mock.patch.dict(os.environ, {"BUFF_LANG": "ru"}):
            self.call("en")
            self.assertEqual(say("ру", "en"), "ру")


if __name__ == "__main__":
    unittest.main()
