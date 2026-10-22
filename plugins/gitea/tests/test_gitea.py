"""Tests for the Gitea plugin's reading logic (#289).

The fixtures are real shapes, captured from a live Gitea 1.27 rather than read
off the documentation — against somebody else's API a fixture only proves it
agrees with itself, which is how the Jira integration shipped three bugs (#156).
Two of those shapes decide everything here:

  * a comment says whether it belongs to a pull request by `pull_request_url`
    being a NON-EMPTY string — not by a boolean and not by its absence;
  * exactly ONE of `issue_url` and `pull_request_url` is filled in, and the other
    is an empty string. A comment under a pull request therefore has NO
    issue_url at all, which is where the number was being read from until a live
    run showed every such comment being silently dropped.
"""

import json
import os
import sys
import unittest

HERE = os.path.dirname(__file__)
sys.path.insert(0, os.path.join(HERE, "..", "..", "..", "sdk"))
os.environ["BUFF_CONFIG"] = json.dumps({
    "base_url": "https://git.corp.local",
    "repos": ["acme/api", "acme/web"],
    "mention_alias": "buff",
})
sys.path.insert(0, os.path.join(HERE, ".."))

from buff_plugin import Scope  # noqa: E402

import main  # noqa: E402


def offline(method, path, body=None):
    """No test reaches a Gitea: a profile nobody stubbed is a profile that
    could not be read, which is a case the plugin has to survive anyway."""
    raise RuntimeError(f"Gitea ответила 503 на {path}")


main.api = offline


def as_bot(login="buffbot", display="Buff"):
    main.me.update({"login": login, "display": display})


def comment(**kw):
    """A comment as Gitea 1.27 actually returns it."""
    base = {
        "id": 26232,
        "html_url": "https://git.corp.local/acme/api/issues/7#issuecomment-26232",
        "issue_url": "https://git.corp.local/acme/api/issues/7",
        "pull_request_url": "",  # filled in ONLY for a pull request, where issue_url is empty
        "created_at": "2026-08-03T00:32:27+02:00",
        "updated_at": "2026-08-03T00:32:27+02:00",
        "body": "@buffbot почему падает экспорт?",
        "user": {"login": "petrov", "full_name": "Пётр"},
    }
    base.update(kw)
    return base


def pull_comment(**kw):
    """The same comment, written under a pull request instead of an issue."""
    return comment(issue_url="", pull_request_url="https://git.corp.local/acme/api/pulls/7",
                   html_url="https://git.corp.local/acme/api/pulls/7#issuecomment-26232", **kw)


# A pull request's head branch, as the plugin would fetch it. Injected so the
# reading logic stays testable without a Gitea.
def head(repo, index):
    return {"acme/api": {7: "feature/csv"}}.get(repo, {}).get(index, "")


class TestWhereACommentBelongs(unittest.TestCase):
    def test_the_issue_number_comes_from_the_url(self):
        self.assertEqual(main.issue_index(comment()), 7)

    def test_a_pull_request_comment_is_told_apart_by_a_non_empty_url(self):
        self.assertFalse(main.is_pull(comment()))
        self.assertTrue(main.is_pull(pull_comment()))

    def test_a_pull_request_comment_has_no_issue_url_at_all(self):
        # Caught by a live run: Gitea fills in exactly one of the two URLs and
        # leaves the other empty, so reading only issue_url dropped every request
        # ever written under a pull request.
        self.assertEqual(main.issue_index(pull_comment()), 7)


class TestEvents(unittest.TestCase):
    def setUp(self):
        as_bot()
        main.scope = Scope(["acme/api", "acme/web"], fold_case=True)

    def test_a_mention_in_an_issue_becomes_a_request(self):
        e = main.event_from_comment("acme/api", comment(), None, head)
        self.assertEqual(e.action, "ask")
        self.assertEqual(e.body, "почему падает экспорт?")
        self.assertEqual(e.extra["issue"], "acme/api#7")
        self.assertEqual(e.extra["context"], "issue")
        # The repository travels even without a branch: the answer is about the
        # code of THAT repository, on its default branch.
        self.assertEqual(e.extra["repo"], "acme/api")
        self.assertNotIn("ref", e.extra)
        self.assertTrue(e.id.startswith("comment:26232@"))

    def test_a_slash_command_is_a_direct_address_too(self):
        # This is the language GitHub, GitLab and Gitea users already speak.
        e = main.event_from_comment("acme/api", comment(body="/research почему тормозит"),
                                    None, head)
        self.assertEqual(e.action, "research")
        self.assertEqual(e.body, "почему тормозит")

    def test_a_question_in_a_pull_request_carries_its_branch(self):
        # The whole point of #287: the answer has to be computed on the code of
        # THAT pull request.
        e = main.event_from_comment("acme/api", pull_comment(), None, head)
        self.assertEqual(e.extra["issue"], "acme/api#7")
        self.assertEqual(e.extra["context"], "pull")
        self.assertEqual(e.extra["repo"], "acme/api")
        self.assertEqual(e.extra["ref"], "feature/csv")

    def test_a_pull_request_whose_branch_cannot_be_read_is_still_answered(self):
        # Losing the request is worse than answering on the default branch, and
        # the ticket says which branch the answer came from either way.
        e = main.event_from_comment(
            "acme/web",
            comment(issue_url="", pull_request_url="https://git.corp.local/acme/web/pulls/3"),
            None, head)
        self.assertEqual(e.extra["context"], "pull")
        self.assertNotIn("ref", e.extra)

    def test_the_bots_own_comment_is_not_a_request(self):
        self.assertIsNone(main.event_from_comment(
            "acme/api", comment(user={"login": "buffbot", "full_name": "Buff"}), None, head))

    def test_a_comment_without_an_address_is_not_a_request(self):
        self.assertIsNone(main.event_from_comment(
            "acme/api", comment(body="кажется, дело в кодировке"), None, head))

    def test_a_comment_older_than_the_watermark_is_not_reported_again(self):
        self.assertIsNone(main.event_from_comment(
            "acme/api", comment(), "2026-08-03T10:00:00Z", head))

    def test_a_repository_outside_the_marked_list_produces_nothing(self):
        # The list IS the access control, and it is checked here — in the one
        # function every path goes through — not in the query (#282).
        self.assertIsNone(main.event_from_comment("acme/secret", comment(), None, head))


class TestAssignment(unittest.TestCase):
    """Assignment is addressed at the bot, but it also happens by routing rules,
    so it is off unless the operator says their assignments are deliberate."""

    def setUp(self):
        as_bot()
        main.scope = Scope(["acme/api"], fold_case=True)

    def timeline(self, login="buffbot", kind="assignees"):
        return [{
            "type": kind,
            "created_at": "2026-08-03T00:40:00+02:00",
            "user": {"login": "petrov", "full_name": "Пётр"},
            "assignee": {"login": login, "full_name": "Buff" if login == "buffbot" else "Пётр"},
        }]

    def test_being_assigned_drafts_a_task(self):
        main.ACCEPT_ASSIGNMENT = True
        try:
            events = main.events_from_timeline("acme/api", 7, "Экспорт", self.timeline(), None)
            self.assertEqual(len(events), 1)
            self.assertEqual(events[0].action, "task")
            self.assertEqual(events[0].extra["trigger"], "assign")
        finally:
            main.ACCEPT_ASSIGNMENT = False

    def test_an_entry_naming_the_bot_counts_whatever_gitea_calls_it(self):
        # Gitea's own name for this timeline entry has changed between releases,
        # and the thing that matters is who was named, not the label on it.
        main.ACCEPT_ASSIGNMENT = True
        try:
            events = main.events_from_timeline(
                "acme/api", 7, "Экспорт", self.timeline(kind="assigned"), None)
            self.assertEqual(len(events), 1)
        finally:
            main.ACCEPT_ASSIGNMENT = False

    def test_a_namesake_is_not_the_bot(self):
        # Gitea always carries a login here, so that is what is matched: a display
        # name is not unique, and matching on it would let anyone who set their
        # full name to the bot's start paid work by being assigned a ticket.
        main.ACCEPT_ASSIGNMENT = True
        try:
            entries = self.timeline("petrov")
            entries[0]["assignee"]["full_name"] = "Buff"
            self.assertEqual(main.events_from_timeline("acme/api", 7, "Экспорт", entries, None), [])
        finally:
            main.ACCEPT_ASSIGNMENT = False

    def test_somebody_else_being_assigned_is_not_our_business(self):
        main.ACCEPT_ASSIGNMENT = True
        try:
            self.assertEqual(
                main.events_from_timeline("acme/api", 7, "Экспорт", self.timeline("petrov"), None),
                [])
        finally:
            main.ACCEPT_ASSIGNMENT = False

    def test_assignment_is_ignored_unless_the_operator_asked_for_it(self):
        self.assertEqual(
            main.events_from_timeline("acme/api", 7, "Экспорт", self.timeline(), None), [])


class TestIssueKeys(unittest.TestCase):
    def test_a_key_names_the_repository_and_the_number(self):
        self.assertEqual(main.issue_key("acme/api", 7), "acme/api#7")

    def test_and_reads_back(self):
        self.assertEqual(main.split_key("acme/api#7"), ("acme/api", 7))

    def test_a_key_that_names_no_repository_is_refused(self):
        with self.assertRaises(RuntimeError):
            main.split_key("7")


class TestWriting(unittest.TestCase):
    def setUp(self):
        main.scope = Scope(["acme/api"], fold_case=True)

    def test_a_reply_outside_the_marked_repositories_is_refused(self):
        # The connector is the customer's trust boundary in BOTH directions: the
        # ticket comes from the platform, which derived it from our own event,
        # and a boundary that only checks the direction it expects trouble from
        # is not one.
        with self.assertRaises(RuntimeError):
            main.reply(issue="acme/secret#1", text="привет")

    def test_the_comment_carries_the_link_it_was_given(self):
        body = main.comment_body("Разобрался.", {"url": "https://buff.systems/app/ask/1",
                                                 "title": "Обсуждение в Buff"}, "")
        self.assertIn("Разобрался.", body)
        self.assertIn("https://buff.systems/app/ask/1", body)
        self.assertIn("Обсуждение в Buff", body)

    def test_the_comment_points_at_the_document_that_was_attached(self):
        # Gitea keeps attachments beside the issue, not in the comment, so the
        # comment has to say where the document went or nobody finds it.
        body = main.comment_body("Готово.", None, "https://git.corp.local/attachments/abc")
        self.assertIn("https://git.corp.local/attachments/abc", body)

    def test_a_bare_answer_stays_bare(self):
        self.assertEqual(main.comment_body("Готово.", None, ""), "Готово.")


class TestCallback(unittest.TestCase):
    """The callback payload, as a live Gitea 1.27 actually sends it."""

    def setUp(self):
        as_bot()
        main.scope = Scope(["acme/api"], fold_case=True)

    def payload(self, **kw):
        base = {
            "action": "created",
            "is_pull": True,
            "repository": {"full_name": "acme/api"},
            "pull_request": {"head": {"ref": "feature/csv"}, "base": {"ref": "master"}},
            "issue": {"number": 7, "title": "Выгрузка в CSV"},
            "comment": {
                "id": 13,
                "body": "@buffbot перечисли изменения",
                "created_at": "2026-09-04T14:59:12Z",
                "issue_url": "",
                "pull_request_url": "https://git.corp.local/acme/api/pulls/7",
                "html_url": "https://git.corp.local/acme/api/pulls/7#issuecomment-13",
                "user": {"login": "petrov", "full_name": "Пётр"},
            },
        }
        base.update(kw)
        return base

    def test_a_callback_about_a_pull_request_needs_no_further_request(self):
        # The branch is in the payload, so the plugin does not go back to the API
        # for it — and the test proves that by passing a resolver that would fail.
        events = main.webhook({"X-Gitea-Event": "issue_comment"},
                              json.dumps(self.payload()))
        self.assertEqual(len(events), 1)
        self.assertEqual(events[0].extra["ref"], "feature/csv")
        self.assertEqual(events[0].extra["context"], "pull")
        self.assertEqual(events[0].extra["issue"], "acme/api#7")

    def test_a_callback_from_an_unmarked_repository_produces_nothing(self):
        events = main.webhook({}, json.dumps(self.payload(repository={"full_name": "acme/secret"})))
        self.assertEqual(events, [])

    def test_an_edit_is_not_a_new_request(self):
        self.assertEqual(main.webhook({}, json.dumps(self.payload(action="edited"))), [])

    def test_a_payload_we_do_not_recognise_is_not_guessed_at(self):
        self.assertEqual(main.webhook({}, "не json"), [])
        self.assertEqual(main.webhook({}, json.dumps({"action": "created"})), [])


class TestWhoAsked(unittest.TestCase):
    """The connection's "who may ask" rules match on the author's login and
    e-mail (#476). Gitea's user object carries both — but for a person who keeps
    the address private it carries a stand-in, `<login>@noreply.<host>`, which
    is nobody's mailbox and must not pass for one."""

    def setUp(self):
        as_bot()
        main.scope = Scope(["acme/api"], fold_case=True)
        main.emails.clear()
        self.profiles = {}
        self.reads = []
        self.real_api = main.api

        def api(method, path, body=None):
            self.reads.append(path)
            if path not in self.profiles:
                raise RuntimeError(f"Gitea ответила 404 на {path}")
            return self.profiles[path]
        main.api = api

    def tearDown(self):
        main.api = self.real_api

    def user(self, email):
        return {"id": 3, "login": "petrov", "login_name": "", "full_name": "Пётр",
                "email": email, "avatar_url": "", "username": "petrov"}

    def test_a_request_carries_the_login_and_the_email(self):
        e = main.event_from_comment("acme/api", comment(user=self.user("petrov@company.ru")), None, head)
        self.assertEqual((e.author_login, e.author_email), ("petrov", "petrov@company.ru"))

    def test_the_stand_in_for_a_private_address_is_not_passed_on(self):
        e = main.event_from_comment("acme/api", comment(user=self.user("petrov@noreply.git.corp.local")), None, head)
        self.assertEqual((e.author_login, e.author_email), ("petrov", ""))

    def test_a_comment_always_carries_the_stand_in_so_the_profile_is_read_once(self):
        # Seen on Gitea 1.27: a comment's poster comes as `<id>+<login>@noreply.<host>`
        # even for a person who shows their address; the profile shows the real one.
        self.profiles["/users/petrov"] = self.user("petrov@company.ru")
        stand_in = self.user("3+petrov@noreply.localhost")
        first = main.event_from_comment("acme/api", comment(user=stand_in), None, head)
        second = main.event_from_comment("acme/api", comment(id=99, user=stand_in), None, head)
        self.assertEqual((first.author_email, second.author_email), ("petrov@company.ru", "petrov@company.ru"))
        self.assertEqual(self.reads, ["/users/petrov"])

    def test_a_profile_that_hides_the_address_or_cannot_be_read_gives_none(self):
        self.profiles["/users/petrov"] = self.user("3+petrov@noreply.localhost")
        e = main.event_from_comment("acme/api", comment(user=self.user("3+petrov@noreply.localhost")), None, head)
        self.assertEqual(e.author_email, "")
        e = main.event_from_comment("acme/api", comment(user=dict(self.user(""), login="ivanov")), None, head)
        self.assertEqual((e.author_login, e.author_email), ("ivanov", ""))

    def test_an_author_without_an_address_still_asks(self):
        e = main.event_from_comment("acme/api", comment(), None, head)
        self.assertEqual((e.author_login, e.author_email), ("petrov", ""))
        self.assertEqual(e.action, "ask")

    def test_an_assignment_and_a_callback_name_the_author_too(self):
        main.ACCEPT_ASSIGNMENT = True
        try:
            [e] = main.events_from_timeline("acme/api", 7, "Экспорт", [{
                "type": "assignees", "created_at": "2026-08-03T00:40:00+02:00",
                "user": self.user("petrov@company.ru"),
                "assignee": {"login": "buffbot", "full_name": "Buff"},
            }], None)
        finally:
            main.ACCEPT_ASSIGNMENT = False
        self.assertEqual((e.author_login, e.author_email), ("petrov", "petrov@company.ru"))
        payload = {"action": "created", "is_pull": False, "repository": {"full_name": "acme/api"},
                   "issue": {"number": 7, "title": "Выгрузка в CSV"},
                   "comment": comment(user=self.user("petrov@company.ru"))}
        [e] = main.webhook({"X-Gitea-Event": "issue_comment"}, json.dumps(payload))
        self.assertEqual((e.author_login, e.author_email), ("petrov", "petrov@company.ru"))


class TestTheseTestsActuallyRun(unittest.TestCase):
    """unittest.main() collects what is defined ABOVE it and then exits.

    A class appended after that line never runs and is reported as passing,
    which is worse than not having it — it happened to this file while it was
    being written, and to the Jira one before it."""

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
