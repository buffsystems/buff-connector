"""Tests for the Bitbucket Cloud plugin's reading logic (#294).

The fixtures are real shapes, captured from bitbucket.org's public API. Three of
them decide everything here:

  * the text a person wrote is at `content.raw`; `content` also carries
    Bitbucket's rendered `html`, and reading that would feed the model markup;
  * a pull request names its branch at `source.branch.name`;
  * accounts have NO username: an `account_id`, a `nickname` and a
    `display_name`, so the bot has to answer to all three.
"""

import json
import os
import sys
import unittest

HERE = os.path.dirname(__file__)
sys.path.insert(0, os.path.join(HERE, "..", "..", "..", "sdk"))
os.environ["BUFF_CONFIG"] = json.dumps({
    "base_url": "https://api.bitbucket.org",
    "repos": ["acme/api", "acme/web"],
    "mention_alias": "buff",
})
sys.path.insert(0, os.path.join(HERE, ".."))

from buff_plugin import Scope  # noqa: E402

import main  # noqa: E402


def as_bot(nickname="buffbot", display="Buff", account_id="557058:aaaa-bbbb"):
    main.me.update({"nickname": nickname, "display": display, "account_id": account_id})


def comment(text="@buffbot почему падает экспорт?", **kw):
    """A comment as bitbucket.org actually returns one."""
    base = {
        "id": 49695105,
        "created_on": "2026-09-04T21:49:16.882859+00:00",
        "updated_on": "2026-09-04T21:49:16.887183+00:00",
        "deleted": False,
        "content": {"type": "rendered", "raw": text, "markup": "markdown",
                    "html": f"<p>{text}</p>"},
        "user": {"nickname": "petrov", "display_name": "Пётр Петров",
                 "account_id": "557058:86a9b692", "type": "user"},
    }
    base.update(kw)
    return base


class TestKeys(unittest.TestCase):
    def test_a_key_says_what_it_points_at(self):
        self.assertEqual(main.issue_key("acme/api", 7), "acme/api!7")

    def test_and_reads_back(self):
        self.assertEqual(main.split_key("acme/api!7"), ("acme/api", "7"))

    # There is nothing else to address: Atlassian removed the issue-tracker API
    # (410 Gone), so a key in the issue notation names something that cannot be
    # read or written and is refused rather than guessed at.
    def test_the_issue_notation_names_nothing_any_more(self):
        with self.assertRaises(RuntimeError):
            main.split_key("acme/api#7")

    def test_a_key_that_names_nothing_is_refused(self):
        with self.assertRaises(RuntimeError):
            main.split_key("7")


class TestReadingAComment(unittest.TestCase):
    def test_the_text_is_the_raw_one_not_the_rendered_html(self):
        self.assertEqual(main.text_of(comment("привет")), "привет")

    def test_a_pull_request_describes_itself_in_a_string(self):
        self.assertEqual(main.body_of({"description": "текст"}), "текст")


class TestEvents(unittest.TestCase):
    def setUp(self):
        as_bot()
        main.scope = Scope(["acme/api", "acme/web"], fold_case=True)

    def test_a_mention_in_a_pull_request_carries_its_branch(self):
        e = main.event_from_comment("acme/api", 7, "Выгрузка в CSV",
                                    "feature/csv", comment(), None)
        self.assertEqual(e.action, "ask")
        self.assertEqual(e.body, "почему падает экспорт?")
        self.assertEqual(e.extra["issue"], "acme/api!7")
        self.assertEqual(e.extra["context"], "pull")
        self.assertEqual(e.extra["ref"], "feature/csv")

    def test_a_slash_command_is_a_direct_address_too(self):
        e = main.event_from_comment("acme/api", 7, "t", "b",
                                    comment("/research почему тормозит"), None)
        self.assertEqual(e.action, "research")

    def test_the_bot_answers_to_its_account_id_as_well(self):
        # This is how Bitbucket's editor writes a mention.
        e = main.event_from_comment("acme/api", 7, "t", "b",
                                    comment("@{557058:aaaa-bbbb} посмотри"), None)
        self.assertIsNotNone(e)
        self.assertEqual(e.body, "посмотри")

    def test_the_bots_own_comment_is_not_a_request(self):
        own = comment(user={"nickname": "buffbot", "display_name": "Buff",
                            "account_id": "557058:aaaa-bbbb"})
        self.assertIsNone(main.event_from_comment("acme/api", 7, "t", "b", own, None))

    def test_a_deleted_comment_is_not_a_request(self):
        self.assertIsNone(main.event_from_comment("acme/api", 7, "t", "b", comment(deleted=True), None))

    def test_a_comment_without_an_address_is_not_a_request(self):
        self.assertIsNone(main.event_from_comment("acme/api", 7, "t", "b", comment("дело в кодировке"), None))

    def test_a_comment_older_than_the_watermark_is_not_reported_again(self):
        self.assertIsNone(main.event_from_comment("acme/api", 7, "t", "b", comment(), "2026-09-05T00:00:00Z"))

    def test_a_repository_outside_the_marked_list_produces_nothing(self):
        self.assertIsNone(main.event_from_comment("acme/secret", 7, "t", "b", comment(), None))


class TestCallback(unittest.TestCase):
    def setUp(self):
        as_bot()
        main.scope = Scope(["acme/api"], fold_case=True)

    def payload(self, **kw):
        base = {
            "repository": {"full_name": "acme/api"},
            "pullrequest": {"id": 7, "title": "Выгрузка в CSV",
                            "source": {"branch": {"name": "feature/csv"}}},
            "comment": comment("@buffbot что меняет этот pull request?"),
        }
        base.update(kw)
        return base

    def test_a_callback_about_a_pull_request_needs_no_further_request(self):
        events = main.webhook({"X-Event-Key": "pullrequest:comment_created"},
                              json.dumps(self.payload()))
        self.assertEqual(len(events), 1)
        self.assertEqual(events[0].extra["ref"], "feature/csv")
        self.assertEqual(events[0].extra["issue"], "acme/api!7")

    # A callback about an issue is a shape the service no longer produces, and a
    # payload we cannot answer must not be guessed at.
    def test_a_callback_about_an_issue_produces_nothing(self):
        self.assertEqual(main.webhook({}, json.dumps({
            "repository": {"full_name": "acme/api"},
            "issue": {"id": 3, "title": "Экспорт"},
            "comment": comment(),
        })), [])

    def test_a_callback_from_an_unmarked_repository_produces_nothing(self):
        self.assertEqual(
            main.webhook({}, json.dumps(self.payload(repository={"full_name": "acme/secret"}))), [])

    def test_a_payload_we_do_not_recognise_is_not_guessed_at(self):
        self.assertEqual(main.webhook({}, "не json"), [])
        self.assertEqual(main.webhook({}, json.dumps({"repository": {"full_name": "acme/api"}})), [])


class TestWriting(unittest.TestCase):
    def setUp(self):
        main.scope = Scope(["acme/api"], fold_case=True)

    def test_a_reply_outside_the_marked_repositories_is_refused(self):
        with self.assertRaises(RuntimeError):
            main.reply(issue="acme/secret!1", text="привет")

    def test_the_comment_carries_the_link_it_was_given(self):
        body = main.comment_body("Разобрался.", {"url": "https://buff.systems/ask/1",
                                                 "title": "Обсуждение в Buff"}, "")
        self.assertIn("https://buff.systems/ask/1", body)

    def test_a_document_is_named_rather_than_promised(self):
        body = main.comment_body("Готово.", None,
                                 "Документ приложен в обсуждении в Buff — Bitbucket не принимает файлы в комментарии.")
        self.assertIn("не принимает файлы", body)

    def test_a_bare_answer_stays_bare(self):
        self.assertEqual(main.comment_body("Готово.", None, ""), "Готово.")


class TestWhoAsked(unittest.TestCase):
    """The connection's "who may ask" rules match on the author's login (#476):
    here the nickname. Bitbucket Cloud shows nobody's e-mail, and an account_id
    is not a name anyone would write into a list."""

    def setUp(self):
        as_bot()
        main.scope = Scope(["acme/api"], fold_case=True)

    def test_a_request_carries_the_nickname_and_no_email(self):
        e = main.event_from_comment("acme/api", 7, "Выгрузка в CSV", "feature/csv", comment(), None)
        self.assertEqual((e.author_login, e.author_email), ("petrov", ""))

    def test_an_account_without_a_nickname_has_no_login(self):
        c = comment(user={"display_name": "Пётр Петров", "account_id": "557058:86a9b692", "type": "user"})
        e = main.event_from_comment("acme/api", 7, "Выгрузка в CSV", "feature/csv", c, None)
        self.assertEqual(e.author_login, "")
        self.assertEqual(e.action, "ask")


class TestTheseTestsActuallyRun(unittest.TestCase):
    """unittest.main() collects what is defined ABOVE it and then exits, so a
    class appended after that line never runs and is reported as passing."""

    def test_nothing_is_defined_after_the_entry_point(self):
        import ast

        module = ast.parse(open(__file__, encoding="utf-8").read())
        last = module.body[-1]
        self.assertIsInstance(
            last, ast.If,
            "после точки входа объявлено что-то ещё — оно никогда не выполнится",
        )


if __name__ == "__main__":
    unittest.main()
