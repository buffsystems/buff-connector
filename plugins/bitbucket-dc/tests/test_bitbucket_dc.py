"""Tests for the Bitbucket Data Center plugin's reading logic (#292).

The fixtures are real shapes, captured from a live Bitbucket 9.6 rather than read
off the documentation. Three of them decide everything here:

  * a pull request hands back ACTIVITIES, and a comment is nested inside one
    whose action is COMMENTED — the others (OPENED, RESCOPED) carry a null
    comment and must not be mistaken for silence;
  * timestamps are MILLISECONDS since the epoch, not ISO;
  * the authenticated account is named only in the `X-AUSERNAME` header, because
    Bitbucket has no `/myself`.
"""

import json
import os
import sys
import unittest

HERE = os.path.dirname(__file__)
sys.path.insert(0, os.path.join(HERE, "..", "..", "..", "sdk"))
os.environ["BUFF_CONFIG"] = json.dumps({
    "base_url": "https://bitbucket.corp.local",
    "repos": ["ACME/api", "ACME/web"],
    "mention_alias": "buff",
})
sys.path.insert(0, os.path.join(HERE, ".."))

from buff_plugin import Scope  # noqa: E402

import main  # noqa: E402


def as_bot(name="buffbot", display="Buff"):
    main.me.update({"name": name, "display": display, "slug": name})


def pull(**kw):
    base = {
        "id": 1,
        "title": "Выгрузка в CSV",
        "state": "OPEN",
        "fromRef": {"displayId": "feature/csv"},
        "toRef": {"displayId": "master"},
    }
    base.update(kw)
    return base


def activity(action="COMMENTED", **comment):
    """An activity as Bitbucket 9.6 actually returns one."""
    if action != "COMMENTED":
        return {"action": action, "comment": None, "createdDate": 1788542465857}
    base = {
        "id": 1,
        "createdDate": 1788542465857,  # milliseconds since the epoch
        "text": "@buffbot почему падает экспорт?",
        "author": {"name": "petrov", "displayName": "Пётр Петров"},
    }
    base.update(comment)
    return {"action": "COMMENTED", "comment": base, "createdDate": base["createdDate"]}


class TestKeys(unittest.TestCase):
    def test_a_key_says_what_it_points_at(self):
        self.assertEqual(main.issue_key("ACME/api", "pull", 7), "ACME/api!7")
        self.assertEqual(main.issue_key("ACME/api", "commit", "9b8a182"), "ACME/api@9b8a182")

    def test_and_reads_back(self):
        self.assertEqual(main.split_key("ACME/api!7"), ("ACME/api", "pull", "7"))
        self.assertEqual(main.split_key("ACME/api@9b8a182"), ("ACME/api", "commit", "9b8a182"))

    def test_a_key_that_names_nothing_is_refused(self):
        with self.assertRaises(RuntimeError):
            main.split_key("7")

    def test_a_repository_splits_into_project_and_slug(self):
        self.assertEqual(main.parts("ACME/api"), ("ACME", "api"))


class TestTime(unittest.TestCase):
    def test_milliseconds_become_a_moment(self):
        # Bitbucket counts in milliseconds; everything else in the platform
        # speaks ISO, and comparing the two silently decides wrong.
        self.assertEqual(main.epoch_to_iso(1788542465857), "2026-09-04T17:21:05.857000Z")

    def test_a_missing_timestamp_is_empty_rather_than_wrong(self):
        self.assertEqual(main.epoch_to_iso(None), "")
        self.assertEqual(main.epoch_to_iso("вчера"), "")


class TestEvents(unittest.TestCase):
    def setUp(self):
        as_bot()
        main.scope = Scope(["ACME/api", "ACME/web"], fold_case=True)

    def test_a_mention_in_a_pull_request_becomes_a_request_on_its_branch(self):
        e = main.event_from_activity("ACME/api", pull(), activity(), None)
        self.assertEqual(e.action, "ask")
        self.assertEqual(e.body, "почему падает экспорт?")
        self.assertEqual(e.extra["issue"], "ACME/api!1")
        self.assertEqual(e.extra["context"], "pull")
        self.assertEqual(e.extra["ref"], "feature/csv")
        self.assertTrue(e.id.startswith("comment:1@"))

    def test_a_slash_command_is_a_direct_address_too(self):
        e = main.event_from_activity("ACME/api", pull(), activity(text="/research почему тормозит"), None)
        self.assertEqual(e.action, "research")

    def test_an_activity_that_is_not_a_comment_is_not_a_request(self):
        # OPENED and RESCOPED carry a null comment; reading them as comments
        # would mean parsing None on every pull request that ever moved.
        for action in ("OPENED", "RESCOPED", "MERGED", "APPROVED"):
            self.assertIsNone(main.event_from_activity("ACME/api", pull(), activity(action), None))

    def test_the_bots_own_comment_is_not_a_request(self):
        self.assertIsNone(main.event_from_activity(
            "ACME/api", pull(), activity(author={"name": "buffbot", "displayName": "Buff"}), None))

    def test_a_comment_without_an_address_is_not_a_request(self):
        self.assertIsNone(main.event_from_activity(
            "ACME/api", pull(), activity(text="дело в кодировке"), None))

    def test_a_comment_older_than_the_watermark_is_not_reported_again(self):
        self.assertIsNone(main.event_from_activity(
            "ACME/api", pull(), activity(), "2026-09-05T00:00:00Z"))

    def test_a_repository_outside_the_marked_list_produces_nothing(self):
        self.assertIsNone(main.event_from_activity("ACME/secret", pull(), activity(), None))


class TestCallback(unittest.TestCase):
    """Bitbucket's `pr:comment:added`, as it actually arrives."""

    def setUp(self):
        as_bot()
        main.scope = Scope(["ACME/api"], fold_case=True)

    def payload(self, **kw):
        base = {
            "eventKey": "pr:comment:added",
            "actor": {"name": "petrov", "displayName": "Пётр Петров"},
            "pullRequest": {
                "id": 1, "title": "Выгрузка в CSV",
                "fromRef": {"displayId": "feature/csv",
                            "repository": {"slug": "api", "project": {"key": "ACME"}}},
                "toRef": {"displayId": "master"},
            },
            "comment": {"id": 42, "text": "@buffbot что меняет этот pull request?",
                        "createdDate": 1788542465857,
                        "author": {"name": "petrov", "displayName": "Пётр Петров"}},
        }
        base.update(kw)
        return base

    def test_a_callback_needs_no_further_request(self):
        # The branch is in the payload, so the plugin does not go back for it.
        events = main.webhook({"X-Event-Key": "pr:comment:added"}, json.dumps(self.payload()))
        self.assertEqual(len(events), 1)
        self.assertEqual(events[0].extra["issue"], "ACME/api!1")
        self.assertEqual(events[0].extra["ref"], "feature/csv")

    def test_a_callback_from_an_unmarked_repository_produces_nothing(self):
        p = self.payload()
        p["pullRequest"]["fromRef"]["repository"]["slug"] = "secret"
        self.assertEqual(main.webhook({}, json.dumps(p)), [])

    def test_an_event_we_do_not_act_on_produces_nothing(self):
        self.assertEqual(main.webhook({}, json.dumps(self.payload(eventKey="pr:merged"))), [])
        self.assertEqual(main.webhook({}, "не json"), [])


class TestWriting(unittest.TestCase):
    def setUp(self):
        main.scope = Scope(["ACME/api"], fold_case=True)

    def test_a_reply_outside_the_marked_repositories_is_refused(self):
        with self.assertRaises(RuntimeError):
            main.reply(issue="ACME/secret!1", text="привет")

    def test_the_comment_carries_the_link_it_was_given(self):
        body = main.comment_text("Разобрался.", {"url": "https://buff.systems/ask/1",
                                                 "title": "Обсуждение в Buff"}, "")
        self.assertIn("Разобрался.", body)
        self.assertIn("https://buff.systems/ask/1", body)

    def test_the_comment_names_the_document(self):
        # Bitbucket DC has no attachments on a comment, so the document has to be
        # named in the text or nobody finds it.
        self.assertIn("research.pdf", main.comment_text("Готово.", None, "Документ: research.pdf"))

    def test_a_bare_answer_stays_bare(self):
        self.assertEqual(main.comment_text("Готово.", None, ""), "Готово.")


class TestWhoAsked(unittest.TestCase):
    """The connection's "who may ask" rules match on the author's account name
    and e-mail (#476); Bitbucket's user object carries both."""

    def setUp(self):
        as_bot()
        main.scope = Scope(["ACME/api"])

    def author(self, **kw):
        base = {"name": "petrov", "emailAddress": "petrov@company.ru", "active": True,
                "displayName": "Пётр Петров", "id": 102, "slug": "petrov", "type": "NORMAL"}
        base.update(kw)
        return base

    def test_a_request_carries_the_account_name_and_the_email(self):
        e = main.event_from_activity("ACME/api", pull(), activity(author=self.author()), None)
        self.assertEqual((e.author_login, e.author_email), ("petrov", "petrov@company.ru"))

    def test_an_author_shown_without_an_address_still_asks(self):
        author = self.author()
        del author["emailAddress"]
        e = main.event_from_activity("ACME/api", pull(), activity(author=author), None)
        self.assertEqual((e.author_login, e.author_email), ("petrov", ""))
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
