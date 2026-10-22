"""Tests for the Jira Cloud plugin's reading logic (#295).

The fixtures are real shapes, captured from a public Jira CLOUD instance — three
of them decide everything here:

  * a comment's body is an ADF DOCUMENT, a tree of nodes, not wiki markup;
  * `POST /rest/api/3/search` answers 410 on Cloud, so searching is
    `GET /rest/api/3/search/jql`;
  * a person is an accountId and a displayName — no login anywhere — and a
    mention is a NODE carrying that id rather than text.
"""

import json
import os
import sys
import unittest

HERE = os.path.dirname(__file__)
sys.path.insert(0, os.path.join(HERE, "..", "..", "..", "sdk"))
os.environ["BUFF_CONFIG"] = json.dumps({
    "base_url": "https://acme.atlassian.net",
    "project_keys": ["PROJ", "OPS"],
    "mention_alias": "buff",
})
sys.path.insert(0, os.path.join(HERE, ".."))

from buff_plugin import Scope  # noqa: E402

import main  # noqa: E402

BOT_ID = "557058:0000-bot"


def as_bot(display="Buff", account_id=BOT_ID):
    main.me.update({"display": display, "account_id": account_id})


def doc(*paragraphs):
    """An ADF document, the way Cloud actually sends one."""
    return {"type": "doc", "version": 1,
            "content": [{"type": "paragraph", "content": p} for p in paragraphs]}


def text(s):
    return {"type": "text", "text": s}


def mention(name=" Buff", ident=BOT_ID):
    return {"type": "mention", "attrs": {"id": ident, "text": f"@{name.strip()}"}}


def comment(body=None, **kw):
    base = {
        "id": "10001",
        "created": "2026-09-04T08:34:00.170-0700",
        "updated": "2026-09-04T08:34:00.170-0700",
        "author": {"accountId": "557058:71e31052", "displayName": "Пётр Петров",
                   "accountType": "atlassian"},
        "body": body if body is not None else doc([text("@Buff почему падает экспорт?")]),
    }
    base.update(kw)
    return base


def issue(key="PROJ-7", summary="Экспорт ломается"):
    return {"key": key, "fields": {"summary": summary}}


class TestAdf(unittest.TestCase):
    def test_the_words_come_out_of_the_tree(self):
        d = doc([text("Кириллица ломается, "), text("разберись")])
        self.assertIn("Кириллица ломается", main.text_of(d))
        self.assertIn("разберись", main.text_of(d))

    def test_a_mention_node_reads_as_an_address(self):
        # The parser looks for `@Имя`; the editor writes a node instead, and this
        # is where the two are reconciled.
        d = doc([mention(), text(" посмотри, пожалуйста")])
        self.assertIn("@Buff", main.text_of(d))

    def test_an_answer_goes_back_as_a_document(self):
        adf = main.to_adf("первая\nвторая")
        self.assertEqual(adf["type"], "doc")
        self.assertEqual(adf["content"][0]["content"][0]["text"], "первая")
        self.assertEqual(adf["content"][1]["content"][0]["text"], "вторая")

    def test_an_empty_line_is_a_paragraph_without_content(self):
        # An ADF paragraph with an empty text node is refused by Jira; an empty
        # paragraph is how a blank line is spelled.
        adf = main.to_adf("одна\n\nдве")
        self.assertEqual(adf["content"][1]["content"], [])


class TestEvents(unittest.TestCase):
    def setUp(self):
        as_bot()
        main.scope = Scope(["PROJ", "OPS"])

    def test_a_mention_becomes_a_request(self):
        e = main.event_from_comment(issue(), comment(), None)
        self.assertEqual(e.action, "ask")
        self.assertEqual(e.body, "почему падает экспорт?")
        self.assertEqual(e.extra["issue"], "PROJ-7")
        self.assertTrue(e.id.startswith("comment:10001@"))

    def test_a_mention_node_alone_is_still_a_request(self):
        # Addressed the way the editor writes it, with no command word: the
        # default is to answer the question.
        e = main.event_from_comment(
            issue(), comment(doc([mention(), text(" почему падает экспорт?")])), None)
        self.assertIsNotNone(e)
        self.assertEqual(e.action, "ask")
        self.assertIn("почему падает", e.body)

    def test_a_command_after_the_mention_chooses_the_work(self):
        e = main.event_from_comment(
            issue(), comment(doc([text("@Buff research откуда задержка")])), None)
        self.assertEqual(e.action, "research")

    def test_the_bots_own_comment_is_not_a_request(self):
        own = comment(author={"accountId": BOT_ID, "displayName": "Buff"})
        self.assertIsNone(main.event_from_comment(issue(), own, None))

    def test_a_comment_without_an_address_is_not_a_request(self):
        self.assertIsNone(main.event_from_comment(
            issue(), comment(doc([text("кажется, дело в кодировке")])), None))

    def test_a_comment_older_than_the_watermark_is_not_reported_again(self):
        self.assertIsNone(main.event_from_comment(issue(), comment(), "2026-09-05T00:00:00Z"))

    def test_a_project_outside_the_marked_list_produces_nothing(self):
        self.assertIsNone(main.event_from_comment(issue("SECRET-1"), comment(), None))

    def test_the_project_is_the_prefix_of_the_key(self):
        self.assertEqual(main.project_of("PROJ-7"), "PROJ")


class TestCallback(unittest.TestCase):
    def setUp(self):
        as_bot()
        main.scope = Scope(["PROJ"])

    def test_a_callback_carries_the_issue_and_the_comment(self):
        events = main.webhook({}, json.dumps({
            "webhookEvent": "comment_created",
            "issue": issue(),
            "comment": comment(),
        }))
        self.assertEqual(len(events), 1)
        self.assertEqual(events[0].extra["issue"], "PROJ-7")

    def test_a_payload_we_do_not_recognise_is_not_guessed_at(self):
        self.assertEqual(main.webhook({}, "не json"), [])
        self.assertEqual(main.webhook({}, json.dumps({"webhookEvent": "jira:issue_updated"})), [])


class TestWriting(unittest.TestCase):
    def setUp(self):
        main.scope = Scope(["PROJ"])

    def test_a_reply_outside_the_marked_projects_is_refused(self):
        with self.assertRaises(RuntimeError):
            main.reply(issue="SECRET-1", text="привет")


class TestWhoAskedAndHow(unittest.TestCase):
    """The connection's list of who may ask matches on the address Jira Cloud
    shows, and a restricted or internal comment is answered the same way
    (cloud-front#476)."""

    def setUp(self):
        as_bot()
        main.scope = Scope(["PROJ"])
        self.calls = []
        self.real_api = main.api
        main.api = lambda method, path, body=None: self.calls.append((method, path, body)) or {}

    def tearDown(self):
        main.api = self.real_api

    def test_the_address_travels_and_no_account_id_poses_as_a_login(self):
        c = comment(author={"accountId": "557058:71e31052", "displayName": "Пётр Петров",
                            "emailAddress": "petrov@corp.local", "accountType": "atlassian"})
        e = main.event_from_comment(issue(), c, None)
        self.assertEqual((e.author_login, e.author_email, e.visibility), ("", "petrov@corp.local", ""))

    def test_a_hidden_address_is_empty(self):
        self.assertEqual(main.event_from_comment(issue(), comment(), None).author_email, "")

    def test_restricted_and_internal_comments_are_read_as_such(self):
        # REST v3 comment `visibility`: a role by name, a group by id (and name).
        role = comment(visibility={"type": "role", "value": "Developers", "identifier": "Developers"})
        group = comment(visibility={"type": "group", "value": "jira-staff",
                                    "identifier": "276f955c-63d7-42c8-9520-92d01dca0625"})
        jsm = comment(jsdPublic=False)
        self.assertEqual(main.event_from_comment(issue(), role, None).visibility, "role:Developers")
        self.assertEqual(main.event_from_comment(issue(), group, None).visibility,
                         "group-id:276f955c-63d7-42c8-9520-92d01dca0625")
        self.assertEqual(main.event_from_comment(issue(), jsm, None).visibility, "internal")
        self.assertEqual(main.event_from_comment(issue(), comment(jsdPublic=True), None).visibility, "")

    def test_the_answer_to_a_group_restricted_comment_is_restricted_to_that_group(self):
        main.reply(issue="PROJ-7", text="Ответ.", link={"url": "https://buff.systems/ask/1", "title": "Обсуждение в Buff"},
                   visibility="group-id:276f955c-63d7-42c8-9520-92d01dca0625")
        [(method, path, body)] = self.calls
        self.assertEqual((method, path), ("POST", "/rest/api/3/issue/PROJ-7/comment"))
        self.assertEqual(body["visibility"], {"type": "group", "identifier": "276f955c-63d7-42c8-9520-92d01dca0625"})
        link = body["body"]["content"][-1]["content"][0]
        self.assertEqual(link["marks"], [{"type": "link", "attrs": {"href": "https://buff.systems/ask/1"}}])

    def test_the_answer_to_an_internal_comment_stays_internal(self):
        main.reply(issue="PROJ-7", text="Ответ.", attachment={"filename": "r.pdf", "base64": ""},
                   visibility="internal")
        [(_, _, body)] = self.calls
        self.assertEqual(body["properties"], [{"key": "sd.public.comment", "value": {"internal": True}}])
        self.assertIn("Документ — в Buff", main.text_of(body["body"]))

    def test_an_ordinary_answer_is_as_before(self):
        main.reply(issue="PROJ-7", text="Ответ.", link={"url": "https://buff.systems/ask/1", "title": "Обсуждение"})
        self.assertEqual(self.calls, [
            ("POST", "/rest/api/3/issue/PROJ-7/comment", {"body": main.to_adf("Ответ.")}),
            ("POST", "/rest/api/3/issue/PROJ-7/remotelink",
             {"object": {"url": "https://buff.systems/ask/1", "title": "Обсуждение"}}),
        ])


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
