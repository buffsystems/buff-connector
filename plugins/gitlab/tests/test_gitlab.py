"""Tests for the GitLab plugin's reading logic (#290).

The fixtures are real shapes, captured from a live GitLab rather than read off
the documentation — against somebody else's API a fixture only proves it agrees
with itself, which is how the Jira integration shipped three bugs (#156) and how
the Gitea one nearly shipped a fourth (#289).

The shape that decides everything here is the EVENTS feed: GitLab has no "every
note in this project since a moment" endpoint, so the poller reads events with
`action=commented`, each carrying the note and what it hangs on.
"""

import json
import os
import sys
import unittest

HERE = os.path.dirname(__file__)
sys.path.insert(0, os.path.join(HERE, "..", "..", "..", "sdk"))
os.environ["BUFF_CONFIG"] = json.dumps({
    "base_url": "https://gitlab.corp.local",
    "projects": ["acme/api", "acme/web"],
    "mention_alias": "buff",
})
sys.path.insert(0, os.path.join(HERE, ".."))

from buff_plugin import Scope  # noqa: E402

import main  # noqa: E402


def as_bot(username="buffbot", name="Buff"):
    main.me.update({"username": username, "name": name})


def event(kind="Issue", **note):
    """An event as GitLab's feed returns one."""
    base_note = {
        "id": 41,
        "body": "@buffbot почему падает экспорт?",
        "created_at": "2026-09-04T14:32:27.000Z",
        "noteable_type": kind,
        "noteable_iid": 7,
    }
    base_note.update(note)
    return {
        "action_name": "commented on",
        "created_at": "2026-09-04T14:32:27.000Z",
        "author": {"username": "petrov", "name": "Пётр"},
        "target_iid": base_note.get("noteable_iid"),
        "note": base_note,
    }


def branch(project, iid):
    return {"acme/api": {7: "feature/csv"}}.get(project, {}).get(iid, "")


class TestKeys(unittest.TestCase):
    """GitLab's own notation is reused, so a key reads the way a person writes
    it: #7 is an issue, !7 a merge request, @<sha> a commit."""

    def test_a_key_says_what_it_points_at(self):
        self.assertEqual(main.issue_key("acme/api", "Issue", 7), "acme/api#7")
        self.assertEqual(main.issue_key("acme/api", "MergeRequest", 7), "acme/api!7")
        self.assertEqual(main.issue_key("acme/api", "Commit", "9b8a182"), "acme/api@9b8a182")

    def test_and_reads_back(self):
        self.assertEqual(main.split_key("acme/api!7"), ("acme/api", "MergeRequest", "7"))
        self.assertEqual(main.split_key("acme/api#7"), ("acme/api", "Issue", "7"))
        self.assertEqual(main.split_key("acme/api@9b8a182"), ("acme/api", "Commit", "9b8a182"))

    def test_a_key_that_names_nothing_is_refused(self):
        with self.assertRaises(RuntimeError):
            main.split_key("7")

    def test_the_text_of_a_comment_is_not_called_one_thing(self):
        # A live GitLab answered "body is missing" to a note posted as `note`:
        # issues and merge requests take `body`, a commit comment takes `note`.
        self.assertEqual(main.notes_field("Issue"), "body")
        self.assertEqual(main.notes_field("MergeRequest"), "body")
        self.assertEqual(main.notes_field("Commit"), "note")

    def test_a_reply_goes_where_the_question_was_asked(self):
        self.assertIn("/merge_requests/7/notes", main.notes_path("acme/api", "MergeRequest", 7))
        self.assertIn("/issues/7/notes", main.notes_path("acme/api", "Issue", 7))
        self.assertIn("/repository/commits/9b8a182/comments",
                      main.notes_path("acme/api", "Commit", "9b8a182"))


class TestEvents(unittest.TestCase):
    def setUp(self):
        as_bot()
        main.scope = Scope(["acme/api", "acme/web"], fold_case=True)

    def test_a_mention_in_an_issue_becomes_a_request(self):
        e = main.event_from("acme/api", event(), None, branch)
        self.assertEqual(e.action, "ask")
        self.assertEqual(e.body, "почему падает экспорт?")
        self.assertEqual(e.extra["issue"], "acme/api#7")
        self.assertEqual(e.extra["context"], "issue")
        # The repository travels even without a branch: the answer is about the
        # code of THAT project, on its default branch.
        self.assertEqual(e.extra["repo"], "acme/api")
        self.assertNotIn("ref", e.extra)

    def test_a_slash_command_is_a_direct_address_too(self):
        e = main.event_from("acme/api", event(body="/research почему тормозит"), None, branch)
        self.assertEqual(e.action, "research")
        self.assertEqual(e.body, "почему тормозит")

    def test_a_question_in_a_merge_request_carries_its_branch(self):
        e = main.event_from("acme/api", event(kind="MergeRequest"), None, branch)
        self.assertEqual(e.extra["issue"], "acme/api!7")
        self.assertEqual(e.extra["context"], "pull")
        self.assertEqual(e.extra["ref"], "feature/csv")

    def test_a_commit_comment_in_the_feed_cannot_be_routed_and_is_not_guessed_at(self):
        # Read off a live GitLab: a comment on a commit arrives in the events
        # feed with noteable_id AND noteable_iid null and no sha anywhere — only
        # the commit's message as a title. The callback carries the sha and
        # serves this case; the poller must not invent an address.
        self.assertIsNone(main.event_from(
            "acme/api", event(kind="Commit", noteable_iid=None), None, branch))

    def test_a_system_note_is_not_a_request(self):
        # GitLab writes its own bookkeeping ("assigned to …") as notes.
        self.assertIsNone(main.event_from(
            "acme/api", event(system=True, body="@buffbot assigned to somebody"), None, branch))

    def test_a_merge_request_whose_branch_cannot_be_read_is_still_answered(self):
        e = main.event_from("acme/web", event(kind="MergeRequest"), None, branch)
        self.assertEqual(e.extra["context"], "pull")
        self.assertNotIn("ref", e.extra)

    def test_the_bots_own_note_is_not_a_request(self):
        ev = event()
        ev["author"] = {"username": "buffbot", "name": "Buff"}
        self.assertIsNone(main.event_from("acme/api", ev, None, branch))

    def test_a_note_without_an_address_is_not_a_request(self):
        self.assertIsNone(main.event_from("acme/api", event(body="дело в кодировке"), None, branch))

    def test_a_note_older_than_the_watermark_is_not_reported_again(self):
        self.assertIsNone(main.event_from("acme/api", event(), "2026-09-04T20:00:00Z", branch))

    def test_a_project_outside_the_marked_list_produces_nothing(self):
        # The list IS the access control, and it is checked here — in the one
        # function every path goes through — not in the query (#282).
        self.assertIsNone(main.event_from("acme/secret", event(), None, branch))

    def test_an_event_about_something_we_do_not_answer_in_is_ignored(self):
        # A note on a snippet or a design is a note, but not a place the bot
        # works in — and guessing an address for it would produce a reply that
        # goes nowhere.
        self.assertIsNone(main.event_from("acme/api", event(kind="Snippet"), None, branch))


class TestTheEventWindow(unittest.TestCase):
    """Found on a live GitLab: `after` filters by DATE and excludes that date, so
    passing the watermark's own day returns nothing that happened that day — a
    poller that sees no events at all and says nothing about it."""

    def test_the_window_starts_the_day_before_the_watermark(self):
        self.assertIn("after=2026-09-03", main.events_query("2026-09-04T16:26:10.000Z"))

    def test_no_watermark_asks_for_everything(self):
        self.assertNotIn("after=", main.events_query(None))

    def test_an_unreadable_watermark_asks_for_everything(self):
        self.assertNotIn("after=", main.events_query("позавчера"))


class TestCallback(unittest.TestCase):
    """GitLab's Note Hook, as it actually arrives."""

    def setUp(self):
        as_bot()
        main.scope = Scope(["acme/api"], fold_case=True)

    def payload(self, **kw):
        base = {
            "object_kind": "note",
            "user": {"username": "petrov", "name": "Пётр"},
            "project": {"path_with_namespace": "acme/api"},
            "object_attributes": {
                "id": 42,
                "note": "@buffbot что меняет этот merge request?",
                "created_at": "2026-09-04 14:59:12 UTC",
                "noteable_type": "MergeRequest",
            },
            "merge_request": {"iid": 7, "source_branch": "feature/csv", "target_branch": "main"},
        }
        base.update(kw)
        return base

    def test_a_callback_about_a_commit_carries_the_sha_the_feed_lacks(self):
        events = main.webhook({}, json.dumps(self.payload(
            object_attributes={"id": 43, "note": "@buffbot что тут изменилось?",
                               "created_at": "2026-09-04 14:59:12 UTC",
                               "noteable_type": "Commit"},
            commit={"id": "9b8a1823b6bea3d77002ac591e00c1289f1ea19d"})))
        self.assertEqual(len(events), 1)
        self.assertEqual(events[0].extra["context"], "commit")
        self.assertEqual(events[0].extra["ref"], "9b8a1823b6bea3d77002ac591e00c1289f1ea19d")
        self.assertTrue(events[0].extra["issue"].endswith("@9b8a1823b6bea3d77002ac591e00c1289f1ea19d"))

    def test_a_callback_about_a_merge_request_needs_no_further_request(self):
        events = main.webhook({"X-Gitlab-Event": "Note Hook"}, json.dumps(self.payload()))
        self.assertEqual(len(events), 1)
        self.assertEqual(events[0].extra["issue"], "acme/api!7")
        self.assertEqual(events[0].extra["ref"], "feature/csv")

    def test_a_callback_from_an_unmarked_project_produces_nothing(self):
        events = main.webhook({}, json.dumps(
            self.payload(project={"path_with_namespace": "acme/secret"})))
        self.assertEqual(events, [])

    def test_a_hook_that_is_not_a_note_produces_nothing(self):
        self.assertEqual(main.webhook({}, json.dumps({"object_kind": "push"})), [])
        self.assertEqual(main.webhook({}, "не json"), [])


class TestWriting(unittest.TestCase):
    def setUp(self):
        main.scope = Scope(["acme/api"], fold_case=True)

    def test_a_reply_outside_the_marked_projects_is_refused(self):
        with self.assertRaises(RuntimeError):
            main.reply(issue="acme/secret#1", text="привет")

    def test_the_note_carries_the_link_it_was_given(self):
        body = main.note_body("Разобрался.", {"url": "https://buff.systems/ask/1",
                                              "title": "Обсуждение в Buff"}, "")
        self.assertIn("Разобрался.", body)
        self.assertIn("https://buff.systems/ask/1", body)

    def test_the_note_points_at_the_document_that_was_attached(self):
        body = main.note_body("Готово.", None, "[report.pdf](/uploads/abc/report.pdf)")
        self.assertIn("/uploads/abc/report.pdf", body)

    def test_a_bare_answer_stays_bare(self):
        self.assertEqual(main.note_body("Готово.", None, ""), "Готово.")


class TestWhoAskedAndHow(unittest.TestCase):
    """The connection's list of who may ask matches on the login and the address
    GitLab knows the author by, and an internal note is answered internally
    (cloud-front#476)."""

    def setUp(self):
        as_bot()
        main.scope = Scope(["acme/api"], fold_case=True)
        main.emails.clear()
        self.calls = []
        self.users = {}
        self.real_api = main.api

        def fake_api(method, path, body=None):
            self.calls.append((method, path, body))
            if path.startswith("/users/"):
                uid = int(path.rsplit("/", 1)[1])
                if uid not in self.users:
                    raise RuntimeError("GitLab ответила 404")
                return self.users[uid]
            return {}

        main.api = fake_api

    def tearDown(self):
        main.api = self.real_api

    def feed_event(self, **note):
        ev = event(**note)
        ev["author"] = {"id": 12, "username": "petrov", "name": "Пётр", "state": "active"}
        return ev

    def test_the_login_and_the_public_address_travel_with_the_request(self):
        # GET /users/:id as the API returns it to a non-admin: `public_email`.
        self.users[12] = {"id": 12, "username": "petrov", "name": "Пётр", "public_email": "petrov@corp.local"}
        e = main.event_from("acme/api", self.feed_event(), None, branch)
        self.assertEqual(e.author_login, "petrov")
        self.assertEqual(e.author_email, "petrov@corp.local")
        self.assertEqual(e.visibility, "")

    def test_an_author_is_read_once_per_run(self):
        self.users[12] = {"id": 12, "public_email": "petrov@corp.local"}
        main.event_from("acme/api", self.feed_event(), None, branch)
        main.event_from("acme/api", self.feed_event(id=42), None, branch)
        self.assertEqual([c for c in self.calls if c[1] == "/users/12"], [("GET", "/users/12", None)])

    def test_an_address_gitlab_does_not_show_is_empty_not_guessed(self):
        e = main.event_from("acme/api", self.feed_event(), None, branch)  # 404 on the user
        self.assertEqual(e.author_email, "")
        self.users[12] = {"id": 12, "public_email": ""}
        self.assertEqual(main.event_from("acme/api", self.feed_event(), None, branch).author_email, "")

    def test_an_internal_note_is_an_internal_request(self):
        self.users[12] = {"id": 12}
        self.assertEqual(main.event_from("acme/api", self.feed_event(internal=True), None, branch).visibility,
                         "internal")
        self.assertEqual(main.event_from("acme/api", self.feed_event(confidential=True), None, branch).visibility,
                         "internal")

    def test_the_note_hook_redacts_the_address_so_it_is_read_off_the_user(self):
        # Note Hook as current GitLab sends it: user.email is "[REDACTED]".
        self.users[12] = {"id": 12, "public_email": "petrov@corp.local"}
        payload = {
            "object_kind": "note",
            "user": {"id": 12, "username": "petrov", "name": "Пётр", "email": "[REDACTED]"},
            "project": {"path_with_namespace": "acme/api"},
            "object_attributes": {"id": 44, "note": "@buffbot почему?", "internal": True,
                                  "created_at": "2026-09-04 14:59:12 UTC", "noteable_type": "Issue"},
            "issue": {"iid": 7},
        }
        [e] = main.webhook({}, json.dumps(payload))
        self.assertEqual((e.author_login, e.author_email, e.visibility), ("petrov", "petrov@corp.local", "internal"))

    def test_the_answer_to_an_internal_note_is_internal(self):
        main.reply(issue="acme/api#7", text="Ответ.", visibility="internal")
        self.assertEqual(self.calls[-1], ("POST", "/projects/acme%2Fapi/issues/7/notes",
                                          {"body": "Ответ.", "internal": True}))

    def test_an_ordinary_answer_carries_no_flag(self):
        main.reply(issue="acme/api!7", text="Ответ.")
        self.assertEqual(self.calls[-1], ("POST", "/projects/acme%2Fapi/merge_requests/7/notes",
                                          {"body": "Ответ."}))


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
