"""Tests for the Redmine plugin's reading logic (#291).

The fixtures are real shapes, captured from a live Redmine 6 rather than read off
the documentation. The one that decides everything here: a journal entry names
its author by DISPLAY NAME and nothing else — no login, no id — so a bot that
only knows its login answers its own replies for ever.
"""

import json
import os
import sys
import unittest

HERE = os.path.dirname(__file__)
sys.path.insert(0, os.path.join(HERE, "..", "..", "..", "sdk"))
os.environ["BUFF_CONFIG"] = json.dumps({
    "base_url": "https://redmine.corp.local",
    "projects": ["catalog", "ops"],
    "mention_alias": "buff",
})
sys.path.insert(0, os.path.join(HERE, ".."))

from buff_plugin import Scope  # noqa: E402

import main  # noqa: E402

# The host's side of plugin.http, as these tests see it: every request is kept,
# and an account is answered from USERS — 403 for any other, which is what a
# token without admin rights gets for somebody else's account.
HTTP: list = []
USERS: dict = {}


def fake_http(method, url, credential="", headers=None, body="", **_):
    HTTP.append((method, url, body))
    if "/users/" in url and url.endswith(".json"):
        uid = url.rsplit("/", 1)[1].removesuffix(".json")
        if uid in USERS:
            return {"status": 200, "body": json.dumps({"user": USERS[uid]})}
        return {"status": 403, "body": ""}
    return {"status": 200, "body": ""}


main.plugin.http = fake_http


def as_bot(login="buffbot", name="Buff Bot"):
    main.me.update({"login": login, "name": name, "id": 5})


def issue(**kw):
    base = {
        "id": 12,
        "project": {"id": 1, "name": "Каталог"},
        "subject": "Экспорт ломается на кириллице",
        "updated_on": "2026-09-04T16:49:54Z",
        "journals": [],
    }
    base.update(kw)
    return base


def journal(**kw):
    """A journal entry as Redmine 6 actually returns one — the author has a
    NAME and no login at all."""
    base = {
        "id": 41,
        "created_on": "2026-09-04T16:49:54Z",
        "notes": "@buffbot почему падает экспорт?",
        "user": {"id": 6, "name": "Пётр Петров"},
    }
    base.update(kw)
    return base


class TestKeys(unittest.TestCase):
    def test_a_key_carries_the_project(self):
        self.assertEqual(main.issue_key("catalog", 12), "catalog#12")

    def test_and_reads_back(self):
        self.assertEqual(main.split_key("catalog#12"), ("catalog", 12))

    def test_a_key_that_names_no_project_is_refused(self):
        with self.assertRaises(RuntimeError):
            main.split_key("12")


class TestTheWindow(unittest.TestCase):
    """Found on a live Redmine: `updated_on` with fractional seconds is answered
    with «422 Updated is invalid», so every poll failed while the integration
    looked healthy."""

    def test_the_watermark_is_cut_to_whole_seconds(self):
        q = main.since_filter("2026-09-04T16:54:37.392970416Z")
        self.assertIn("2026-09-04T16%3A54%3A37Z", q)
        self.assertNotIn("392970", q)

    def test_no_watermark_asks_for_everything(self):
        self.assertEqual(main.since_filter(None), "")

    def test_an_unreadable_watermark_asks_for_everything(self):
        self.assertEqual(main.since_filter("позавчера"), "")


class TestEvents(unittest.TestCase):
    def setUp(self):
        as_bot()
        main.scope = Scope(["catalog", "ops"], fold_case=True)

    def test_a_mention_becomes_a_request(self):
        e = main.event_from_journal("catalog", issue(), journal(), None)
        self.assertEqual(e.action, "ask")
        self.assertEqual(e.body, "почему падает экспорт?")
        self.assertEqual(e.extra["issue"], "catalog#12")
        self.assertEqual(e.extra["context"], "issue")
        # The project travels so the answer is computed inside that module, and
        # Redmine has no branch to pin it to.
        self.assertEqual(e.extra["repo"], "catalog")
        self.assertNotIn("ref", e.extra)
        self.assertTrue(e.id.startswith("journal:41@"))

    def test_a_slash_command_is_a_direct_address_too(self):
        e = main.event_from_journal("catalog", issue(),
                                    journal(notes="/research почему тормозит"), None)
        self.assertEqual(e.action, "research")

    def test_the_bot_knows_its_own_replies_by_display_name(self):
        # The journal carries no login, so this is the ONLY way the bot can tell
        # its own note from a person's — and getting it wrong means answering
        # itself for ever.
        self.assertIsNone(main.event_from_journal(
            "catalog", issue(), journal(user={"id": 5, "name": "Buff Bot"}), None))

    def test_a_journal_without_an_address_is_not_a_request(self):
        self.assertIsNone(main.event_from_journal(
            "catalog", issue(), journal(notes="дело в кодировке"), None))

    def test_a_status_change_with_no_note_is_not_a_request(self):
        # Most journal entries carry no note at all: they record a field change.
        self.assertIsNone(main.event_from_journal(
            "catalog", issue(), journal(notes=""), None))

    def test_a_journal_older_than_the_watermark_is_not_reported_again(self):
        self.assertIsNone(main.event_from_journal(
            "catalog", issue(), journal(), "2026-09-04T20:00:00Z"))

    def test_a_project_outside_the_marked_list_produces_nothing(self):
        self.assertIsNone(main.event_from_journal("secret", issue(), journal(), None))


class TestAssignment(unittest.TestCase):
    def setUp(self):
        as_bot()
        main.scope = Scope(["catalog"], fold_case=True)

    def assigned(self, to="5"):
        return journal(notes="", details=[{"property": "attr", "name": "assigned_to_id",
                                           "old_value": None, "new_value": to}])

    def test_being_assigned_drafts_a_task(self):
        main.ACCEPT_ASSIGNMENT = True
        try:
            e = main.assignment_event("catalog", issue(), self.assigned(), None)
            self.assertEqual(e.action, "task")
            self.assertEqual(e.extra["trigger"], "assign")
        finally:
            main.ACCEPT_ASSIGNMENT = False

    def test_somebody_else_being_assigned_is_not_our_business(self):
        main.ACCEPT_ASSIGNMENT = True
        try:
            self.assertIsNone(main.assignment_event("catalog", issue(), self.assigned("9"), None))
        finally:
            main.ACCEPT_ASSIGNMENT = False

    def test_assignment_is_ignored_unless_the_operator_asked_for_it(self):
        self.assertIsNone(main.assignment_event("catalog", issue(), self.assigned(), None))


class TestWriting(unittest.TestCase):
    def setUp(self):
        main.scope = Scope(["catalog"], fold_case=True)

    def test_a_reply_outside_the_marked_projects_is_refused(self):
        with self.assertRaises(RuntimeError):
            main.reply(issue="secret#1", text="привет")

    def test_the_note_carries_the_link_it_was_given(self):
        note = main.note_text("Разобрался.", {"url": "https://buff.systems/ask/1",
                                              "title": "Обсуждение в Buff"}, "")
        self.assertIn("Разобрался.", note)
        self.assertIn("https://buff.systems/ask/1", note)
        self.assertIn("Обсуждение в Buff", note)

    def test_on_a_markdown_redmine_the_link_and_the_answer_are_markdown(self):
        # cloud-front#478: the link was Textile beside a Markdown answer, and one of
        # the two always showed raw.
        note = main.note_text("**Python**.", {"url": "https://buff.systems/ask/1", "title": "Обсуждение в Buff"}, "")
        self.assertEqual(note, "**Python**.\n\n[Обсуждение в Buff](https://buff.systems/ask/1)")

    def test_on_a_textile_redmine_both_are_textile(self):
        main.TEXTILE = True
        try:
            note = main.note_text("**Python**.", {"url": "https://buff.systems/ask/1", "title": "Обсуждение в Buff"}, "")
        finally:
            main.TEXTILE = False
        self.assertEqual(note, '*Python*.\n\n"Обсуждение в Buff":https://buff.systems/ask/1')

    def test_the_note_names_the_document_that_was_attached(self):
        note = main.note_text("Готово.", None, "Документ приложен: research.pdf")
        self.assertIn("research.pdf", note)

    def test_a_bare_answer_stays_bare(self):
        self.assertEqual(main.note_text("Готово.", None, ""), "Готово.")


class TestWhoAskedAndHow(unittest.TestCase):
    """The connection's list of who may ask matches on the login and the address
    Redmine knows the author by, and a private note is answered privately
    (cloud-front#476)."""

    def setUp(self):
        as_bot()
        main.scope = Scope(["catalog"], fold_case=True)
        main.users.clear()
        HTTP.clear()
        USERS.clear()

    def test_the_account_behind_the_display_name_travels_with_the_request(self):
        # GET /users/6.json as Redmine 6 answers an admin's token.
        USERS["6"] = {"id": 6, "login": "petrov", "firstname": "Пётр", "lastname": "Петров",
                      "mail": "petrov@corp.local", "admin": False}
        e = main.event_from_journal("catalog", issue(), journal(), None)
        self.assertEqual((e.author_login, e.author_email), ("petrov", "petrov@corp.local"))
        self.assertEqual(e.visibility, "")

    def test_an_account_the_token_may_not_see_is_empty_and_not_asked_again(self):
        first = main.event_from_journal("catalog", issue(), journal(), None)
        second = main.event_from_journal("catalog", issue(), journal(id=42), None)
        self.assertEqual((first.author_login, first.author_email), ("", ""))
        self.assertEqual((second.author_login, second.author_email), ("", ""))
        self.assertEqual(len([r for r in HTTP if "/users/6.json" in r[1]]), 1)

    def test_a_failed_read_is_tried_again_on_the_next_note(self):
        main.plugin.http = lambda *a, **k: {"status": 502, "body": ""}
        try:
            self.assertEqual(main.event_from_journal("catalog", issue(), journal(), None).author_login, "")
        finally:
            main.plugin.http = fake_http
        USERS["6"] = {"id": 6, "login": "petrov"}
        self.assertEqual(main.event_from_journal("catalog", issue(), journal(), None).author_login, "petrov")

    def test_a_private_note_is_a_private_request(self):
        e = main.event_from_journal("catalog", issue(), journal(private_notes=True), None)
        self.assertEqual(e.visibility, "private")

    def test_the_answer_to_a_private_note_is_private(self):
        main.reply(issue="catalog#12", text="Ответ.", visibility="private")
        method, url, body = HTTP[-1]
        self.assertEqual((method, url), ("PUT", "https://redmine.corp.local/issues/12.json"))
        self.assertEqual(json.loads(body), {"issue": {"notes": "Ответ.", "private_notes": True}})

    def test_a_private_answer_keeps_its_document_off_the_issue(self):
        # An upload is attached to the issue, which everyone who sees the issue sees.
        main.reply(issue="catalog#12", text="Ответ.", visibility="private",
                   link={"url": "https://buff.systems/app/research/r1", "title": "Исследование"},
                   attachment={"filename": "research.pdf", "base64": "JVBERg=="})
        self.assertFalse([r for r in HTTP if "/uploads.json" in r[1]], "nothing uploaded")
        issue_body = json.loads(HTTP[-1][2])["issue"]
        self.assertTrue(issue_body["private_notes"])
        self.assertNotIn("uploads", issue_body)
        self.assertIn("Документ — в Buff по ссылке", issue_body["notes"])

    def test_an_ordinary_answer_carries_no_flag(self):
        main.reply(issue="catalog#12", text="Ответ.")
        self.assertEqual(json.loads(HTTP[-1][2]), {"issue": {"notes": "Ответ."}})


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
