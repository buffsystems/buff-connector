"""Tests for the GitHub plugin's reading logic (#293).

The fixtures are real shapes, captured from github.com's public API — the one
part of GitHub this machine can reach without an account. Two of them decide
everything here:

  * a comment on a PULL REQUEST is told from an issue's only by its `html_url`
    carrying `/pull/`; the API's `issue_url` says `/issues/N` for both, because
    to GitHub a pull request IS an issue;
  * a comment on a COMMIT lives in its own list and carries `commit_id`, so
    unlike GitLab's feed it can be routed from polling alone.
"""

import json
import os
import sys
import unittest

HERE = os.path.dirname(__file__)
sys.path.insert(0, os.path.join(HERE, "..", "..", "..", "sdk"))
os.environ["BUFF_CONFIG"] = json.dumps({
    "base_url": "https://api.github.com",
    "repos": ["acme/api", "acme/web"],
    "mention_alias": "buff",
})
sys.path.insert(0, os.path.join(HERE, ".."))

from buff_plugin import Scope  # noqa: E402

import main  # noqa: E402

# GitHub's GET /users/{login}: `email` is the PUBLIC address, null when the person
# keeps it private. The only call the reading logic makes on its own (#476).
USERS = {
    "petrov": {"login": "petrov", "type": "User", "name": "Пётр Петров", "email": "petrov@company.ru"},
    "ivanova": {"login": "ivanova", "type": "User", "name": "Анна Иванова", "email": None},
}


def users_api(method, path, body=None, accept=""):
    login = path.rsplit("/", 1)[-1]
    if method == "GET" and path.startswith("/users/") and login in USERS:
        return USERS[login]
    raise RuntimeError(f"GitHub ответил 404 на {path}")


main.api = users_api


def as_bot(login="buffbot", name="Buff"):
    main.me.update({"login": login, "name": name})


def comment(**kw):
    """A comment as github.com actually returns one."""
    base = {
        "id": 539473254,
        "issue_url": "https://api.github.com/repos/acme/api/issues/7",
        "html_url": "https://github.com/acme/api/issues/7#issuecomment-539473254",
        "created_at": "2026-09-04T11:38:31Z",
        "updated_at": "2026-09-04T11:38:31Z",
        "body": "@buffbot почему падает экспорт?",
        "user": {"login": "petrov", "type": "User"},
    }
    base.update(kw)
    return base


def pull_comment(**kw):
    """The same comment under a pull request: the API url is unchanged and only
    the browser address says so."""
    kw.setdefault("html_url", "https://github.com/acme/api/pull/7#issuecomment-539473254")
    return comment(**kw)


def commit_comment(**kw):
    base = {
        "id": 37262667,
        "commit_id": "ff2d5fa6d07c533e2575cf06f95501fbb24ef254",
        "created_at": "2026-09-04T21:59:58Z",
        "html_url": "https://github.com/acme/api/commit/ff2d5fa#commitcomment-37262667",
        "body": "@buffbot что тут изменилось?",
        "user": {"login": "petrov", "type": "User"},
    }
    base.update(kw)
    return base


def head(repo, number):
    return {"acme/api": {7: "feature/csv"}}.get(repo, {}).get(number, "")


class TestKeys(unittest.TestCase):
    def test_issues_and_pull_requests_share_one_numbering(self):
        self.assertEqual(main.issue_key("acme/api", "issue", 7), "acme/api#7")
        self.assertEqual(main.issue_key("acme/api", "commit", "ff2d5fa"), "acme/api@ff2d5fa")

    def test_and_read_back(self):
        self.assertEqual(main.split_key("acme/api#7"), ("acme/api", "issue", "7"))
        self.assertEqual(main.split_key("acme/api@ff2d5fa"), ("acme/api", "commit", "ff2d5fa"))

    def test_a_key_that_names_nothing_is_refused(self):
        with self.assertRaises(RuntimeError):
            main.split_key("7")


class TestWhereACommentBelongs(unittest.TestCase):
    def test_the_number_comes_from_the_api_url(self):
        self.assertEqual(main.issue_number(comment()), 7)
        self.assertEqual(main.issue_number(pull_comment()), 7)

    def test_only_the_browser_address_says_pull_request(self):
        self.assertFalse(main.is_pull(comment()))
        self.assertTrue(main.is_pull(pull_comment()))


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
        self.assertEqual(e.extra["repo"], "acme/api")
        self.assertNotIn("ref", e.extra)

    def test_a_slash_command_is_a_direct_address_too(self):
        e = main.event_from_comment("acme/api", comment(body="/research почему тормозит"), None, head)
        self.assertEqual(e.action, "research")

    def test_a_question_in_a_pull_request_carries_its_branch(self):
        e = main.event_from_comment("acme/api", pull_comment(), None, head)
        self.assertEqual(e.extra["context"], "pull")
        self.assertEqual(e.extra["ref"], "feature/csv")

    def test_a_pull_request_whose_branch_cannot_be_read_is_still_answered(self):
        e = main.event_from_comment(
            "acme/web",
            pull_comment(issue_url="https://api.github.com/repos/acme/web/issues/3",
                         html_url="https://github.com/acme/web/pull/3#issuecomment-1"),
            None, head)
        self.assertEqual(e.extra["context"], "pull")
        self.assertNotIn("ref", e.extra)

    def test_a_comment_under_a_commit_is_answered_on_that_commit(self):
        e = main.event_from_commit_comment("acme/api", commit_comment(), None)
        self.assertEqual(e.extra["context"], "commit")
        self.assertEqual(e.extra["ref"], "ff2d5fa6d07c533e2575cf06f95501fbb24ef254")
        self.assertTrue(e.extra["issue"].endswith("@ff2d5fa6d07c533e2575cf06f95501fbb24ef254"))

    def test_the_bots_own_comment_is_not_a_request(self):
        self.assertIsNone(main.event_from_comment(
            "acme/api", comment(user={"login": "buffbot"}), None, head))

    def test_a_comment_without_an_address_is_not_a_request(self):
        self.assertIsNone(main.event_from_comment(
            "acme/api", comment(body="дело в кодировке"), None, head))

    def test_a_comment_older_than_the_watermark_is_not_reported_again(self):
        self.assertIsNone(main.event_from_comment("acme/api", comment(), "2026-09-05T00:00:00Z", head))

    def test_a_repository_outside_the_marked_list_produces_nothing(self):
        self.assertIsNone(main.event_from_comment("acme/secret", comment(), None, head))
        self.assertIsNone(main.event_from_commit_comment("acme/secret", commit_comment(), None))


class TestCallback(unittest.TestCase):
    """GitHub's `issue_comment` and `commit_comment` hooks."""

    def setUp(self):
        as_bot()
        main.scope = Scope(["acme/api"], fold_case=True)

    def payload(self, **kw):
        base = {
            "action": "created",
            "repository": {"full_name": "acme/api"},
            "issue": {"number": 7, "title": "Выгрузка в CSV"},
            "pull_request": {"head": {"ref": "feature/csv"}},
            "comment": pull_comment(id=42),
        }
        base.update(kw)
        return base

    def test_a_callback_about_a_pull_request_needs_no_further_request(self):
        events = main.webhook({"X-Github-Event": "issue_comment"}, json.dumps(self.payload()))
        self.assertEqual(len(events), 1)
        self.assertEqual(events[0].extra["ref"], "feature/csv")
        self.assertEqual(events[0].extra["issue"], "acme/api#7")

    def test_a_callback_about_a_commit_carries_its_sha(self):
        events = main.webhook({"X-Github-Event": "commit_comment"}, json.dumps({
            "action": "created", "repository": {"full_name": "acme/api"},
            "comment": commit_comment(id=43),
        }))
        self.assertEqual(len(events), 1)
        self.assertEqual(events[0].extra["context"], "commit")

    def test_a_callback_from_an_unmarked_repository_produces_nothing(self):
        self.assertEqual(
            main.webhook({}, json.dumps(self.payload(repository={"full_name": "acme/secret"}))), [])

    def test_an_edit_is_not_a_new_request(self):
        self.assertEqual(main.webhook({}, json.dumps(self.payload(action="edited"))), [])
        self.assertEqual(main.webhook({}, "не json"), [])


class TestWriting(unittest.TestCase):
    def setUp(self):
        main.scope = Scope(["acme/api"], fold_case=True)

    def test_a_reply_outside_the_marked_repositories_is_refused(self):
        with self.assertRaises(RuntimeError):
            main.reply(issue="acme/secret#1", text="привет")

    def test_the_comment_carries_the_link_it_was_given(self):
        body = main.comment_body("Разобрался.", {"url": "https://buff.systems/ask/1",
                                                 "title": "Обсуждение в Buff"}, "")
        self.assertIn("Разобрался.", body)
        self.assertIn("https://buff.systems/ask/1", body)

    def test_a_document_is_named_rather_than_promised(self):
        # GitHub takes no attachment through its API — only the browser can
        # upload one — so the reply says where the document is instead.
        body = main.comment_body("Готово.", None,
                                 "Документ приложен в обсуждении в Buff — GitHub не принимает файлы через API.")
        self.assertIn("не принимает файлы", body)

    def test_a_bare_answer_stays_bare(self):
        self.assertEqual(main.comment_body("Готово.", None, ""), "Готово.")


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



class SignedIn(unittest.TestCase):
    """The check has to say WHOSE account this is — the rule itself lives in the
    SDK now; what is checked here is that GitHub's identity feeds it."""

    def test_names_both_when_they_differ(self):
        as_bot(login="buffbot", name="Buff Bot")
        self.assertEqual(main.identity().signed_in(), "Buff Bot (buffbot)")

    def test_names_the_login_once_when_there_is_no_other_name(self):
        as_bot(login="buffbot", name="buffbot")
        self.assertEqual(main.identity().signed_in(), "buffbot")

    def test_falls_back_to_the_login_when_the_account_has_no_name(self):
        as_bot(login="buffbot", name="")
        self.assertEqual(main.identity().signed_in(), "buffbot")


class ReviewComments(unittest.TestCase):
    """A comment left on a LINE of a pull request is still a question about that
    pull request. GitHub sends it as its own event, and its comment carries no
    `issue_url` at all — only `pull_request_url` — so the number has to come from
    there. Read the other way it looks like a commit comment (there IS a
    `commit_id`), and the answer would land on the commit instead of the review
    it was asked in."""

    def review_comment(self, **kw):
        base = {
            "id": 1806193474,
            "pull_request_url": "https://api.github.com/repos/acme/api/pulls/7",
            "html_url": "https://github.com/acme/api/pull/7#discussion_r1806193474",
            "commit_id": "9d1f2c0a" + "0" * 32,
            "path": "export.py",
            "created_at": "2026-09-04T11:38:31Z",
            "body": "@buffbot почему здесь нет charset?",
            "user": {"login": "petrov", "type": "User"},
        }
        base.update(kw)
        return base

    def test_number_comes_from_the_pull_request_url(self):
        self.assertEqual(main.issue_number(self.review_comment()), 7)

    def test_a_review_comment_is_a_pull_request_request(self):
        as_bot()
        payload = json.dumps({
            "action": "created",
            "repository": {"full_name": "acme/api"},
            "pull_request": {"number": 7, "title": "Выгрузка в CSV",
                             "head": {"ref": "feature/csv"}},
            "comment": self.review_comment(),
        })
        events = main.webhook({"X-Github-Event": "pull_request_review_comment"}, payload)
        self.assertEqual(len(events), 1)
        self.assertEqual(events[0].extra["issue"], "acme/api#7")
        self.assertEqual(events[0].extra["context"], "pull")
        self.assertEqual(events[0].extra["ref"], "feature/csv")

class WhoAsked(unittest.TestCase):
    """The connection's "who may ask" rules match on the author's login and
    e-mail (#476). A comment names the login; the e-mail is the account's public
    one, looked up once per person."""

    def setUp(self):
        as_bot()
        main.scope = Scope(["acme/api"], fold_case=True)
        main.emails.clear()

    def test_a_request_carries_the_login_and_the_public_email(self):
        e = main.event_from_comment("acme/api", comment(), None, head)
        self.assertEqual((e.author_login, e.author_email), ("petrov", "petrov@company.ru"))
        e = main.event_from_commit_comment("acme/api", commit_comment(), None)
        self.assertEqual((e.author_login, e.author_email), ("petrov", "petrov@company.ru"))

    def test_a_private_email_is_not_made_up(self):
        e = main.event_from_comment("acme/api", comment(user={"login": "ivanova", "type": "User"}), None, head)
        self.assertEqual((e.author_login, e.author_email), ("ivanova", ""))

    def test_a_failed_lookup_does_not_cost_the_request(self):
        e = main.event_from_comment("acme/api", comment(user={"login": "ghost", "type": "User"}), None, head)
        self.assertEqual((e.author_login, e.author_email), ("ghost", ""))
        self.assertEqual(e.action, "ask")

    def test_each_person_is_looked_up_once(self):
        calls = []

        def counting(method, path, body=None, accept=""):
            calls.append(path)
            return users_api(method, path, body, accept)

        main.api = counting
        try:
            main.event_from_comment("acme/api", comment(), None, head)
            main.event_from_comment("acme/api", comment(id=2), None, head)
        finally:
            main.api = users_api
        self.assertEqual(calls, ["/users/petrov"])

    def test_an_assignment_names_who_assigned(self):
        timeline = [{"event": "assigned", "created_at": "2026-09-04T11:38:31Z",
                     "actor": {"login": "petrov", "type": "User"},
                     "assignee": {"login": "buffbot", "type": "User"}}]

        def api(method, path, body=None, accept=""):
            return timeline if "/timeline" in path else users_api(method, path, body, accept)

        main.api, main.ACCEPT_ASSIGNMENT = api, True
        try:
            [e] = main.assignment_events("acme/api", {"number": 7, "title": "Экспорт"}, None)
        finally:
            main.api, main.ACCEPT_ASSIGNMENT = users_api, False
        self.assertEqual((e.author_login, e.author_email), ("petrov", "petrov@company.ru"))

    def test_a_callback_carries_the_author_too(self):
        events = main.webhook({"X-Github-Event": "issue_comment"}, json.dumps({
            "action": "created", "repository": {"full_name": "acme/api"},
            "issue": {"number": 7, "title": "Выгрузка в CSV"}, "comment": comment(id=44),
        }))
        self.assertEqual((events[0].author_login, events[0].author_email), ("petrov", "petrov@company.ru"))


if __name__ == "__main__":
    unittest.main()

