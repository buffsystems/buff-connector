"""Tests for the Confluence plugin's reading logic (#297).

The fixtures are real shapes, captured from a live public Confluence Data Center
(confluence.atlassian.com, 9.2). Four of them decide everything here:

  * `type=comment` in CQL is a feed of comments across a space, and the page a
    comment hangs on arrives in `container` — with its space, when asked for;
  * the comment's browser address is ready-made in `_links.webui`;
  * the body is storage-format XHTML, which only becomes a request after it is
    read as text;
  * `/rest/api/search` wraps matched words in `@@@hl@@@…@@@endhl@@@`.
"""

import json
import os
import sys
import unittest
import unittest.mock

HERE = os.path.dirname(__file__)
sys.path.insert(0, os.path.join(HERE, "..", "..", "..", "sdk"))
os.environ["BUFF_CONFIG"] = json.dumps({
    "base_url": "https://wiki.corp.local",
    "spaces": ["DOC", "OPS"],
    "mention_alias": "buff",
})
sys.path.insert(0, os.path.join(HERE, ".."))

import main  # noqa: E402


def as_bot(username="buffbot", display="Buff Bot", key="ff8081813fe5abb4013fe5ad45d62309"):
    main.me.update({"username": username, "display": display, "key": key})


def comment(**kw):
    """A comment as the live server returns it from a CQL search."""
    base = {
        "id": "211648985",
        "type": "comment",
        "title": "Re: Panel Macro",
        "body": {"storage": {"value": "<p>@buffbot почему падает экспорт?</p>",
                             "representation": "storage"}},
        "version": {
            "by": {"type": "known", "username": "zyap", "userKey": "ff8081813fe5abb401",
                   "displayName": "Zed Yap"},
            "when": "2026-09-04T05:45:59.009Z",
            "number": 1,
        },
        "container": {
            "id": "51872380", "type": "page", "title": "Panel Macro",
            "space": {"id": 3, "key": "DOC", "name": "Documentation", "type": "global"},
        },
        "_links": {
            "webui": "/spaces/DOC/pages/51872380/Panel+Macro"
                     "?focusedCommentId=211648985#comment-211648985",
            "self": "https://wiki.corp.local/rest/api/content/211648985",
        },
    }
    base.update(kw)
    return base


class Addressing(unittest.TestCase):
    def test_the_thread_is_the_page(self):
        self.assertEqual(main.page_key("DOC", "51872380"), "DOC#51872380")
        self.assertEqual(main.split_key("DOC#51872380"), ("DOC", "51872380"))

    def test_a_key_that_is_not_an_address_is_refused(self):
        with self.assertRaises(RuntimeError):
            main.split_key("51872380")


class Requests(unittest.TestCase):
    def setUp(self):
        as_bot()

    def test_a_mention_becomes_a_request(self):
        e = main.comment_event(comment(), None)
        self.assertIsNotNone(e)
        self.assertEqual(e.action, "ask")
        self.assertEqual(e.body, "почему падает экспорт?")
        self.assertEqual(e.extra["issue"], "DOC#51872380")
        self.assertEqual(e.title, "Panel Macro")
        self.assertEqual(e.author, "Zed Yap")

    def test_the_asker_is_named_by_login_on_data_center(self):
        # The «who may ask» rules match on it (#476); Data Center shows no e-mail.
        e = main.comment_event(comment(), None)
        self.assertEqual((e.author_login, e.author_email), ("zyap", ""))

    def test_the_link_points_at_the_comment_itself(self):
        e = main.comment_event(comment(), None)
        self.assertTrue(e.url.endswith("#comment-211648985"))
        self.assertTrue(e.url.startswith("https://wiki.corp.local/spaces/DOC/"))

    def test_a_slash_command_chooses_the_work(self):
        c = comment(body={"storage": {"value": "<p>/research откуда задержка</p>"}})
        e = main.comment_event(c, None)
        self.assertEqual((e.action, e.body), ("research", "откуда задержка"))

    def test_a_comment_without_an_address_is_not_a_request(self):
        c = comment(body={"storage": {"value": "<p>спасибо, помогло</p>"}})
        self.assertIsNone(main.comment_event(c, None))

    def test_the_bot_does_not_answer_itself(self):
        c = comment()
        c["version"]["by"] = {"username": "buffbot", "displayName": "Buff Bot"}
        self.assertIsNone(main.comment_event(c, None))

    def test_a_space_outside_the_marked_list_is_ignored(self):
        c = comment()
        c["container"]["space"] = {"key": "SECRET"}
        self.assertIsNone(main.comment_event(c, None))

    def test_an_older_comment_is_not_new(self):
        self.assertIsNone(main.comment_event(comment(), "2026-09-05T00:00:00Z"))

    def test_confluence_holds_no_code_so_the_request_carries_no_branch(self):
        e = main.comment_event(comment(), None)
        self.assertEqual(e.extra.get("context"), "issue")
        self.assertEqual(e.extra.get("repo", ""), "")
        self.assertEqual(e.extra.get("ref", ""), "")

    def test_the_request_is_read_as_text_not_as_markup(self):
        c = comment(body={"storage": {"value":
            '<p>@buffbot смотри <a href="https://x.example">тут</a> и в '
            '<ac:link><ri:page ri:content-title="Регламент"/></ac:link></p>'}})
        e = main.comment_event(c, None)
        self.assertEqual(e.body, "смотри тут (https://x.example) и в «Регламент»")


class Scope(unittest.TestCase):
    def test_the_query_is_bounded_by_the_marked_spaces(self):
        self.assertEqual(main.cql("type=comment"),
                         'space in ("DOC", "OPS") and type=comment')

    def test_the_window_is_whole_minutes(self):
        self.assertEqual(main.since_clause("2026-09-04T05:45:59.009Z"),
                         ' and lastmodified >= "2026/09/04 05:45"')
        self.assertEqual(main.since_clause(None), "")

    def test_writing_outside_the_marked_spaces_is_refused(self):
        as_bot()
        with self.assertRaisesRegex(RuntimeError, "вне области"):
            main.reply(issue="SECRET#1", text="нет")


class EmptyMeansEverything(unittest.TestCase):
    """An empty list of spaces is every space the bot's account can open (#429).

    Confluence already draws that line — its own permissions decide what the
    account sees — so an unlisted connection works wherever the account does.
    """

    def setUp(self):
        as_bot()
        self.posted: list[tuple[str, str]] = []

        def http(method, url, credential="", headers=None, body=""):
            self.posted.append((method, url))
            if "/rest/api/content/77" in url and method == "GET":
                return {"status": 200, "body": json.dumps({
                    "title": "Регламент", "space": {"key": "ANY"}, "body": {"storage": {"value": "<p>текст</p>"}},
                    "version": {}, "ancestors": [], "_links": {}, "results": []})}
            if "/rest/api/space/" in url:
                return {"status": 200, "body": json.dumps({"key": "ANY"})}
            if "/rest/api/user/current" in url:
                return {"status": 200, "body": json.dumps({"username": "buffbot", "displayName": "Buff Bot"})}
            return {"status": 200, "body": json.dumps({"results": []})}

        self.patches = [unittest.mock.patch.object(main, "SPACES", []),
                        unittest.mock.patch.object(main, "scope", main.Scope([], fold_case=True)),
                        unittest.mock.patch.object(main.plugin, "http", http)]
        for p in self.patches:
            p.start()

    def tearDown(self):
        for p in self.patches:
            p.stop()

    def test_the_query_spans_every_space(self):
        self.assertEqual(main.cql("type=comment"), "space is not empty and type=comment")

    def test_a_mention_in_any_space_is_a_request(self):
        c = comment()
        c["container"]["space"] = {"key": "ANY"}
        e = main.comment_event(c, None)
        self.assertIsNotNone(e)
        self.assertEqual(e.extra["issue"], "ANY#51872380")

    def test_reading_and_writing_work_in_any_space(self):
        self.assertEqual(main.get_page(key="ANY#77")["title"], "Регламент")
        main.reply(issue="ANY#77", text="готово")
        self.assertIn(("POST", "https://wiki.corp.local/rest/api/content"), self.posted)

    def test_the_check_says_the_area_is_everything_the_account_opens(self):
        detail = main.check()["detail"]
        self.assertIn("все пространства, открытые учётной записи", detail)
        self.assertNotIn("0 из", detail)


class Highlighting(unittest.TestCase):
    """The search endpoint marks what it matched. Those markers are for a browser
    that renders them; a model just sees noise."""

    def test_markers_are_stripped(self):
        self.assertEqual(main.clean("@@@hl@@@Backup@@@endhl@@@ and Restore"),
                         "Backup and Restore")

    def test_the_space_key_comes_from_the_address(self):
        # The search endpoint names the space by its TITLE («Confluence 10.2»),
        # and a title is not a key.
        self.assertEqual(main._space_of({"url": "/spaces/DOC/pages/163578/Backup"}), "DOC")


class Writing(unittest.TestCase):
    def test_text_becomes_paragraphs_and_is_escaped(self):
        self.assertEqual(main.as_storage("<b>да</b>\n\nи нет"),
                         "<p>&lt;b&gt;да&lt;/b&gt;</p><p /><p>и нет</p>")

    def test_a_link_is_named_in_the_comment(self):
        body = main.comment_body("Готово.", {"url": "https://buff.systems/x",
                                             "title": "Обсуждение в Buff"}, "")
        self.assertIn("Обсуждение в Buff: https://buff.systems/x", body)


class TestTheseTestsActuallyRun(unittest.TestCase):
    def test_nothing_is_defined_after_the_entry_point(self):
        import ast

        module = ast.parse(open(__file__, encoding="utf-8").read())
        self.assertIsInstance(
            module.body[-1], ast.If,
            "после точки входа объявлено что-то ещё — оно никогда не выполнится",
        )



class Cloud(unittest.TestCase):
    """The SAME code has to read Confluence Cloud, and this fixture came from a
    live public Cloud site (openmrs.atlassian.net).

    Two differences the shapes actually have, and one that matters:
      * an author is named by `accountId` and carries `publicName` beside
        `displayName` — there is no `username` at all;
      * the comment's address has no `#comment-…` fragment, only the query.
    Everything else — the CQL comment feed, `container.space.key`, the storage
    body — is identical, which is why one integration serves both.
    """

    def cloud_comment(self, **kw):
        base = {
            "id": "1286930433",
            "type": "comment",
            "title": "Re: O3 Order Templates",
            "body": {"storage": {
                "value": '<p local-id="cb9c7f9cb275">@buff как мы договорились '
                         'нумеровать релизы?</p>',
                "representation": "storage"}},
            "version": {
                "by": {"type": "known",
                       "accountId": "712020:83b2606e-19eb-4a3a-ad7d-2578748b0c52",
                       "publicName": "Burke Mamlin", "displayName": "Burke Mamlin"},
                "when": "2026-08-20T03:44:45.279Z"},
            "container": {
                "id": "356483074", "type": "page", "title": "O3 Order Templates",
                "space": {"key": "DOC", "name": "Projects", "type": "global"}},
            "_links": {"webui": "/spaces/projects/pages/356483074/O3+Order+Templates"
                                "?focusedCommentId=1286930433"},
        }
        base.update(kw)
        return base

    def test_a_cloud_comment_is_read_the_same_way(self):
        as_bot()
        e = main.comment_event(self.cloud_comment(), None)
        self.assertIsNotNone(e)
        self.assertEqual(e.body, "как мы договорились нумеровать релизы?")
        self.assertEqual(e.extra["issue"], "DOC#356483074")
        self.assertEqual(e.author, "Burke Mamlin")
        self.assertTrue(e.url.endswith("focusedCommentId=1286930433"))

    def test_on_cloud_there_is_no_login_and_the_e_mail_only_when_shown(self):
        as_bot()
        hidden = main.comment_event(self.cloud_comment(), None)
        self.assertEqual((hidden.author_login, hidden.author_email), ("", ""))
        c = self.cloud_comment()
        c["version"]["by"]["email"] = "author@example.org"
        shown = main.comment_event(c, None)
        self.assertEqual((shown.author_login, shown.author_email), ("", "author@example.org"))

    def test_the_bot_knows_itself_by_account_id_on_cloud(self):
        # Cloud has no login: the account id is the only stable identity, and
        # without this the bot answers its own comments forever.
        as_bot(username="", display="Buff Bot", key="712020:83b2606e-19eb-4a3a-ad7d-2578748b0c52")
        self.assertIsNone(main.comment_event(self.cloud_comment(), None))

    def test_an_entity_in_the_body_is_decoded(self):
        as_bot()
        c = self.cloud_comment(body={"storage": {"value":
            "<p>@buff we don&rsquo;t need a UI</p>"}})
        e = main.comment_event(c, None)
        self.assertEqual(e.body, "we don\u2019t need a UI")


class Edition(unittest.TestCase):
    """Which Confluence this is, told by what each edition actually answers —
    verified on a live server of both kinds.

      * Cloud answers `/rest/api/settings/systemInfo` (cloudId, edition) and
        reports a meaningless synthetic version, `1000.0.0-<hash>`;
      * Data Center answers 404 there and names a real version in the
        application-links manifest.
    """

    def test_cloud_is_named_by_its_edition_not_its_version(self):
        self.assertEqual(
            main.edition({"cloudId": "1fc7c3fe", "edition": "standard",
                          "siteTitle": "OpenMRS Wiki"}, ""),
            "Confluence Cloud (standard)")

    def test_cloud_without_an_edition_is_still_cloud(self):
        self.assertEqual(main.edition({"cloudId": "1fc7c3fe"}, ""), "Confluence Cloud")

    def test_data_center_is_named_by_its_version(self):
        xml = ("<?xml version=\"1.0\"?><manifest><name>Docs</name>"
               "<version>9.2.21</version><typeId>confluence</typeId></manifest>")
        self.assertEqual(main.edition({}, xml), "Confluence 9.2.21")

    def test_saying_nothing_beats_guessing(self):
        self.assertEqual(main.edition({}, ""), "Confluence")

if __name__ == "__main__":
    unittest.main()
