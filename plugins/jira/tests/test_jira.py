"""Tests for the Jira plugin's reading logic.

They cover the three things a live Jira contradicted the first time round, which
is why each has a comment saying what actually happens rather than what the API
docs suggest.
"""

import json
import os
import sys
import unittest

HERE = os.path.dirname(__file__)
sys.path.insert(0, os.path.join(HERE, "..", "..", "..", "sdk"))
os.environ["BUFF_CONFIG"] = json.dumps({
    "base_url": "https://jira.example.com",
    "project_keys": ["PROJ"],
    "mention_alias": "buff",
})
sys.path.insert(0, os.path.join(HERE, ".."))

from buff_plugin import Scope  # noqa: E402

import main  # noqa: E402


def as_bot(name="buffbot", key="JIRAUSER10100", display="Buff"):
    main.me.update({"name": name, "key": key, "display": display})


class TestMentions(unittest.TestCase):
    def setUp(self):
        as_bot()

    def test_wiki_markup_is_the_real_mention(self):
        # This is what Jira's autocomplete inserts and what renders as a chip.
        got = main.parse_command("[~buffbot] ask что тут по срокам?")
        self.assertEqual(got, ("ask", "что тут по срокам?"))

    def test_plain_at_form_also_works(self):
        self.assertEqual(main.parse_command("@buff task сделай импорт")[0], "task")

    def test_internal_key_form_is_recognised(self):
        self.assertIsNotNone(main.parse_command("[~JIRAUSER10100] ask?"))

    def test_text_on_both_sides_is_kept(self):
        # People give the context first and the instruction after; dropping
        # either half loses the question.
        action, instruction = main.parse_command(
            "У нас падает импорт на больших файлах. [~buffbot] research почему?")
        self.assertEqual(action, "research")
        self.assertIn("падает импорт", instruction)
        self.assertIn("почему", instruction)

    def test_default_action_is_ask(self):
        self.assertEqual(main.parse_command("[~buffbot] что это значит?")[0], "ask")

    def test_a_mention_with_nothing_to_do_is_not_a_command(self):
        self.assertIsNone(main.parse_command("[~buffbot]"))

    def test_someone_elses_mention_is_ignored(self):
        self.assertIsNone(main.parse_command("[~someone] ask это не нам"))


class TestSelfIdentification(unittest.TestCase):
    def test_the_bot_answers_to_all_three_identifiers(self):
        # Jira's changelog has no login: `to` is an internal key and `toString`
        # is a display name. Only a comment's author carries the login.
        as_bot()
        self.assertTrue(main.is_me("buffbot"))
        self.assertTrue(main.is_me("JIRAUSER10100"))
        self.assertTrue(main.is_me("Buff"))
        self.assertFalse(main.is_me("someone-else"))


class TestEventExtraction(unittest.TestCase):
    def setUp(self):
        as_bot()

    def issue(self, **kw):
        base = {
            "key": "PROJ-1",
            "fields": {"summary": "Импорт падает", "comment": {"comments": []}},
            "changelog": {"histories": []},
        }
        base.update(kw)
        return base

    def test_comment_becomes_an_event_with_a_time_bearing_id(self):
        # The id carries the moment as well as Jira's own id: a recreated Jira
        # numbers comments from one again, and a plain id would make real events
        # look like repeats upstream.
        iss = self.issue(fields={
            "summary": "Импорт падает",
            "comment": {"comments": [{
                "id": "10001", "created": "2026-01-02T10:00:00.000+0000",
                "body": "[~buffbot] ask почему падает?",
                "author": {"name": "petrov", "displayName": "Петров"},
            }]},
        })
        events = main.events_from(iss, None)
        self.assertEqual(len(events), 1)
        self.assertTrue(events[0].id.startswith("comment:10001@"))
        self.assertIn("2026-01-02", events[0].id)
        self.assertEqual(events[0].action, "ask")

    def test_the_bots_own_comments_are_not_requests(self):
        iss = self.issue(fields={
            "summary": "x",
            "comment": {"comments": [{
                "id": "1", "created": "2026-01-02T10:00:00.000+0000",
                "body": "[~buffbot] ask …", "author": {"name": "buffbot", "displayName": "Buff"},
            }]},
        })
        self.assertEqual(main.events_from(iss, None), [])

    # Labels no longer start anything — see TestOnlyADirectAddressStartsWork.

    def test_assignment_to_the_bot_is_recognised_without_a_login(self):
        # Jira's changelog gives no login for an assignee, only an internal key
        # and a display name, so the bot has to know all three of its own names.
        main.ACCEPT_ASSIGNMENT = True
        try:
            iss = self.issue(changelog={"histories": [{
                "created": "2026-01-02T12:00:00.000+0000", "author": {"displayName": "Петров"},
                "items": [{"field": "assignee", "to": "JIRAUSER10100", "toString": "Buff"}],
            }]})
            events = main.events_from(iss, None)
            self.assertEqual(len(events), 1)
            self.assertEqual(events[0].action, "task")
        finally:
            main.ACCEPT_ASSIGNMENT = False

    def test_assignment_to_somebody_else_is_ignored(self):
        main.ACCEPT_ASSIGNMENT = True
        try:
            iss = self.issue(changelog={"histories": [{
                "created": "2026-01-02T12:00:00.000+0000", "author": {},
                "items": [{"field": "assignee", "to": "JIRAUSER99", "toString": "Кто-то"}],
            }]})
            self.assertEqual(main.events_from(iss, None), [])
        finally:
            main.ACCEPT_ASSIGNMENT = False


class TestQueryWindow(unittest.TestCase):
    def test_the_window_is_wider_than_the_watermark(self):
        # Jira compares dates to the MINUTE, so a query starting exactly at the
        # watermark drops events; the overlap is removed by de-duplication.
        q = main.jql("2026-01-02T10:00:30+00:00")
        self.assertIn("2026-01-02 09:58", q)
        self.assertIn('project in ("PROJ")', q)

    def test_without_a_watermark_everything_in_scope_is_read(self):
        self.assertNotIn("updated >=", main.jql(None))


class TestWebhookDelivery(unittest.TestCase):
    """A callback carries the issue, so a request can become an event without
    going back to the API. The HOST validated it; this only parses it."""

    def test_a_comment_callback_becomes_the_same_event_a_poll_would_make(self):
        as_bot()
        body = json.dumps({
            "webhookEvent": "comment_created",
            "issue": {"key": "PROJ-1", "fields": {"summary": "Импорт падает"}},
            "comment": {
                "id": "555", "created": "2026-09-04T12:00:00.000+0000",
                "body": "[~buffbot] ask почему падает?",
                "author": {"name": "petrov", "displayName": "Пётр"},
            },
        })
        events = main.webhook({}, body)
        self.assertEqual(len(events), 1)
        self.assertEqual(events[0].action, "ask")
        self.assertEqual(events[0].extra["issue"], "PROJ-1")
        # The same id a poll would produce, so the two paths dedup against each
        # other instead of answering twice.
        self.assertTrue(events[0].id.startswith("comment:555@"))

    def test_a_payload_we_do_not_recognise_is_left_to_the_poll(self):
        as_bot()
        self.assertEqual(main.webhook({}, "не json"), [])
        self.assertEqual(main.webhook({}, json.dumps({"webhookEvent": "jira:worklog_updated"})), [])




class TestMarkedProjectsAreTheAccessControl(unittest.TestCase):
    """The list of marked projects is what decides where the bot may be called.

    The poll path enforced it through its JQL — which looked sufficient until a
    CALLBACK started producing events too. A tracker's webhooks are configured
    globally; a missing filter on their side delivers every project, and a
    mention anywhere would start paid work in a project nobody approved."""

    def commented(self, key):
        return {
            "key": key,
            "fields": {"summary": "…", "comment": {"comments": [{
                "id": "1", "created": "2026-09-04T12:00:00.000+0000",
                "body": "[~buffbot] ask разберись",
                "author": {"name": "petrov", "displayName": "Пётр"},
            }]}},
        }

    def test_a_ticket_from_a_marked_project_is_accepted(self):
        as_bot()
        self.assertEqual(len(main.events_from(self.commented("PROJ-1"), None)), 1)

    def test_a_ticket_from_anywhere_else_is_ignored(self):
        as_bot()
        self.assertEqual(main.events_from(self.commented("SECRET-1"), None), [])

    def test_a_callback_cannot_route_around_the_project_list(self):
        as_bot()
        body = json.dumps({
            "webhookEvent": "comment_created",
            "issue": {"key": "SECRET-9", "fields": {"summary": "чужой проект"}},
            "comment": {"id": "77", "created": "2026-09-04T12:00:00.000+0000",
                        "body": "[~buffbot] ask сделай", "author": {"name": "x", "displayName": "X"}},
        })
        self.assertEqual(main.webhook({}, body), [])

    def test_an_answer_cannot_be_written_outside_the_marked_projects(self):
        # The ticket comes from the platform, which derived it from our own
        # event. The connector is still the customer's trust boundary, and one
        # that only checks the direction it expects trouble from is not one.
        as_bot()
        with self.assertRaises(RuntimeError):
            main.reply(issue="SECRET-1", text="привет")

    def test_no_projects_configured_means_nothing_is_in_scope(self):
        # Refusing everything is the safe direction: a connection with no project
        # list has not been told where it may act.
        as_bot()
        saved, main.scope = main.scope, Scope([])
        try:
            self.assertEqual(main.events_from(self.commented("PROJ-1"), None), [])
        finally:
            main.scope = saved


class TestOnlyADirectAddressStartsWork(unittest.TestCase):
    """The bot takes work when it is ADDRESSED, and not otherwise.

    A label used to start a run. It was removed because a label is addressed at a
    process, not at anyone: triage rules, bulk JQL edits and the tracker's own
    automation all add labels, and every one of them became work somebody was
    billed for and nobody ordered."""

    def changelog(self, field, to_value):
        return {
            "key": "PROJ-1",
            "fields": {"summary": "Импорт падает", "comment": {"comments": []}},
            "changelog": {"histories": [{
                "created": "2026-09-04T12:00:00.000+0000",
                "author": {"displayName": "Пётр"},
                "items": [{"field": field, "toString": to_value, "fromString": "",
                           "to": to_value}],
            }]},
        }

    def test_a_label_no_longer_starts_anything(self):
        as_bot()
        for label in ("buff-research", "buff-ask", "buff-task", "нужно-исследование"):
            self.assertEqual(main.events_from(self.changelog("labels", label), None), [],
                             f"метка {label} запустила работу")

    def test_assignment_is_ignored_unless_the_operator_opted_in(self):
        as_bot()
        main.ACCEPT_ASSIGNMENT = False
        self.assertEqual(main.events_from(self.changelog("assignee", "Buff"), None), [])

    def test_assignment_works_when_the_operator_opted_in(self):
        as_bot()
        main.ACCEPT_ASSIGNMENT = True
        try:
            events = main.events_from(self.changelog("assignee", "Buff"), None)
            self.assertEqual(len(events), 1)
            self.assertEqual(events[0].extra["trigger"], "assign")
            self.assertEqual(events[0].action, "task")
        finally:
            main.ACCEPT_ASSIGNMENT = False


class TestWatermark(unittest.TestCase):
    """Jira writes "2026-09-04T10:00:00.000+0000"; the connector's watermark is
    "2026-09-04T10:00:00Z". They are the same instant in two spellings, and
    comparing them as strings decides the wrong way — silently, and for ever."""

    def test_timestamps_compare_across_spellings(self):
        self.assertEqual(main.moment("2026-09-04T10:00:00.000+0000"),
                         main.moment("2026-09-04T10:00:00Z"))
        self.assertTrue(main.after("2026-09-04T10:05:00.000+0000", "2026-09-04T10:00:00Z"))
        self.assertFalse(main.after("2026-09-04T09:55:00.000+0000", "2026-09-04T10:00:00Z"))

    def test_no_watermark_means_everything_is_new(self):
        self.assertTrue(main.after("2026-09-04T09:55:00.000+0000", None))

    def test_an_unreadable_timestamp_counts_as_new(self):
        # Acting twice on a request is recoverable; losing it is not.
        self.assertTrue(main.after("не дата", "2026-09-04T10:00:00Z"))

    def test_an_answered_comment_is_not_reported_again(self):
        # The search window is by TICKET, and a ticket keeps matching long after
        # the comment in it was answered — so without this filter the same
        # request is re-reported on every poll, for ever.
        as_bot()
        issue = {
            "key": "PROJ-1",
            "fields": {
                "summary": "Импорт падает",
                "comment": {"comments": [
                    {"id": "1", "created": "2026-09-04T10:00:00.000+0000",
                     "body": "[~buffbot] ask почему падает?",
                     "author": {"name": "petrov", "displayName": "Пётр"}},
                    {"id": "2", "created": "2026-09-04T12:00:00.000+0000",
                     "body": "[~buffbot] ask а теперь почему медленно?",
                     "author": {"name": "petrov", "displayName": "Пётр"}},
                ]},
            },
        }
        fresh = main.events_from(issue, "2026-09-04T11:00:00Z")
        self.assertEqual([e.id.split("@")[0] for e in fresh], ["comment:2"])


class TestWhoAskedAndHow(unittest.TestCase):
    """The connection's list of who may ask matches on the login and the address
    Jira knows the author by, and a restricted comment is answered restricted
    (cloud-front#476)."""

    def setUp(self):
        as_bot()
        main.scope = Scope(["PROJ"])
        self.calls = []
        self.real_api = main.api
        main.api = lambda method, path, body=None: self.calls.append((method, path, body)) or {}

    def tearDown(self):
        main.api = self.real_api

    def commented(self, **comment):
        # A comment as Jira DC's REST API v2 returns it: the author carries the
        # login (`name`) and `emailAddress`; a restricted one has `visibility`.
        base = {"id": "10001", "created": "2026-09-04T12:00:00.000+0000",
                "body": "[~buffbot] ask почему падает?",
                "author": {"name": "petrov", "key": "JIRAUSER10200", "displayName": "Пётр",
                           "emailAddress": "petrov@corp.local", "active": True}}
        base.update(comment)
        return {"key": "PROJ-1", "fields": {"summary": "…", "comment": {"comments": [base]}}}

    def test_the_login_and_the_address_travel_with_the_request(self):
        [e] = main.events_from(self.commented(), None)
        self.assertEqual((e.author_login, e.author_email, e.visibility), ("petrov", "petrov@corp.local", ""))

    def test_a_comment_restricted_to_a_role_or_a_group_is_a_restricted_request(self):
        [role] = main.events_from(self.commented(visibility={"type": "role", "value": "Developers"}), None)
        [group] = main.events_from(self.commented(visibility={"type": "group", "value": "jira-staff"}), None)
        self.assertEqual(role.visibility, "role:Developers")
        self.assertEqual(group.visibility, "group:jira-staff")

    def test_an_address_jira_hides_is_empty(self):
        c = self.commented()
        del c["fields"]["comment"]["comments"][0]["author"]["emailAddress"]
        [e] = main.events_from(c, None)
        self.assertEqual(e.author_email, "")

    def test_the_answer_to_a_restricted_comment_is_restricted_the_same_way(self):
        main.reply(issue="PROJ-1", text="Ответ.", link={"url": "https://buff.systems/ask/1",
                                                         "title": "Обсуждение в Buff"},
                   visibility="role:Developers")
        self.assertEqual(self.calls, [("POST", "/rest/api/2/issue/PROJ-1/comment", {
            "body": "Ответ.\n\n[Обсуждение в Buff|https://buff.systems/ask/1]",
            "visibility": {"type": "role", "value": "Developers"}})],
            "no remote link: everyone who sees the ticket sees it")

    def test_a_document_is_not_attached_where_everyone_would_see_it(self):
        main.reply(issue="PROJ-1", text="Готово.", attachment={"filename": "r.pdf", "base64": ""},
                   visibility="group:jira-staff")
        [(_, path, body)] = self.calls
        self.assertEqual(path, "/rest/api/2/issue/PROJ-1/comment")
        self.assertIn("Документ — в Buff", body["body"])
        self.assertEqual(body["visibility"], {"type": "group", "value": "jira-staff"})

    def test_an_ordinary_answer_is_as_before(self):
        main.reply(issue="PROJ-1", text="Ответ.", link={"url": "https://buff.systems/ask/1", "title": "Обсуждение"})
        self.assertEqual(self.calls, [
            ("POST", "/rest/api/2/issue/PROJ-1/comment", {"body": "Ответ."}),
            ("POST", "/rest/api/2/issue/PROJ-1/remotelink",
             {"object": {"url": "https://buff.systems/ask/1", "title": "Обсуждение"}}),
        ])


class TestTheseTestsActuallyRun(unittest.TestCase):
    """unittest.main() collects what is defined ABOVE it and then exits.

    This file once carried it in the middle, so eleven tests below that line
    never ran — and were reported as passing, which is worse than not having
    them. Nothing may be defined after the entry point."""

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
