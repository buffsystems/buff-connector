"""Tests for the Notion plugin's reading and writing logic (#348).

The fixtures follow the shapes Notion documents for API version 2026-03-11:
a comment with `parent.page_id`, `discussion_id`, `created_by` as a partial
user and `display_name.resolved_name`; a page with its title in whichever
property is of type `title`; a database that is a container of data sources;
the webhook event with `entity` and `data.page_id`.

The plugin's HTTP goes through a fake host here: one small world — a root page
with a child, a database with rows, a page NOBODY marked — answered in Notion's
own JSON, so the area check, the sweep and the callback are exercised end to
end without a process.
"""

import json
import os
import sys
import unittest

HERE = os.path.dirname(__file__)
sys.path.insert(0, os.path.join(HERE, "..", "..", "..", "sdk"))

ROOT = "2a3b4c5d6e7f8091a2b3c4d5e6f7a8b9"
CHILD = "3b4c5d6e7f8091a2b3c4d5e6f7a8b9c0"
GRANDCHILD = "4c5d6e7f8091a2b3c4d5e6f7a8b9c0d1"
DB = "5d6e7f8091a2b3c4d5e6f7a8b9c0d1e2"
SOURCE = "6e7f8091a2b3c4d5e6f7a8b9c0d1e2f3"
ROW = "7f8091a2b3c4d5e6f7a8b9c0d1e2f3a4"
SECRET = "8091a2b3c4d5e6f7a8b9c0d1e2f3a4b5"
BOT = "9091a2b3c4d5e6f7a8b9c0d1e2f3a4b6"
HUMAN = "a091a2b3c4d5e6f7a8b9c0d1e2f3a4b7"
DISC = "b091a2b3c4d5e6f7a8b9c0d1e2f3a4b8"

os.environ["BUFF_CONFIG"] = json.dumps({
    "base_url": "https://api.notion.com",
    "roots": [f"https://www.notion.so/acme/База-знаний-{ROOT}", DB],
    "mention_alias": "buff",
})
sys.path.insert(0, os.path.join(HERE, ".."))

import content  # noqa: E402
import main  # noqa: E402


def dashed(b):
    return f"{b[:8]}-{b[8:12]}-{b[12:16]}-{b[16:20]}-{b[20:]}"


def rich(text):
    return [{"type": "text", "text": {"content": text, "link": None}, "plain_text": text, "href": None,
             "annotations": {"bold": False, "italic": False, "strikethrough": False, "underline": False,
                             "code": False, "color": "default"}}]


def page(pid, title, parent):
    return {"object": "page", "id": dashed(pid), "created_time": "2026-09-01T10:00:00.000Z",
            "last_edited_time": "2026-09-10T12:00:00.000Z", "parent": parent, "in_trash": False,
            "properties": {"title": {"id": "title", "type": "title", "title": rich(title)}},
            "url": f"https://www.notion.so/{title.replace(' ', '-')}-{pid}"}


def comment(cid="c091a2b3c4d5e6f7a8b9c0d1e2f3a4b9", text="@buff почему падает экспорт?",
            author=HUMAN, page_id=CHILD, at="2026-09-16T05:45:59.000Z", **kw):
    """A comment as `GET /v1/comments` returns it."""
    c = {"object": "comment", "id": dashed(cid), "parent": {"type": "page_id", "page_id": dashed(page_id)},
         "discussion_id": dashed(DISC), "created_time": at, "last_edited_time": at,
         "created_by": {"object": "user", "id": dashed(author)}, "rich_text": rich(text),
         "display_name": {"type": "user", "resolved_name": "Пётр Петров"},
         "original_content_deleted": False, "attachments": []}
    c.update(kw)
    return c


class FakeNotion:
    """The customer's Notion, answered in its own JSON."""

    def __init__(self):
        self.pages = {
            ROOT: page(ROOT, "База знаний", {"type": "workspace", "workspace": True}),
            CHILD: page(CHILD, "Регламент выпуска релизов", {"type": "page_id", "page_id": dashed(ROOT)}),
            GRANDCHILD: page(GRANDCHILD, "Дежурство", {"type": "page_id", "page_id": dashed(CHILD)}),
            ROW: {**page(ROW, "Экспорт в CSV", {"type": "data_source_id", "data_source_id": dashed(SOURCE),
                                                  "database_id": dashed(DB)}),
                  "properties": {"Название": {"type": "title", "title": rich("Экспорт в CSV")},
                                 "Статус": {"type": "status", "status": {"name": "В работе", "color": "blue"}},
                                 "Оценка": {"type": "number", "number": 3}}},
            SECRET: page(SECRET, "Не наша страница", {"type": "workspace", "workspace": True}),
        }
        self.comments = {}
        self.posted = []
        self.created = []
        self.calls = []
        self.uploads = []
        self.users_denied = False

    def http(self, method, url, credential="", headers=None, body="", binary=False, body_bytes=None):
        path = url.replace("https://api.notion.com", "")
        self.calls.append((method, path))
        assert (headers or {}).get("Notion-Version") == "2026-03-11" or "/send" in path, headers
        data = json.loads(body) if body else {}
        return self.route(method, path, data, body_bytes)

    def ok(self, obj):
        return {"status": 200, "headers": {}, "body": json.dumps(obj)}

    def gone(self):
        return {"status": 404, "headers": {},
                "body": json.dumps({"object": "error", "status": 404, "code": "object_not_found",
                                    "message": "Could not find page"})}

    def route(self, method, path, data, body_bytes):
        if path == "/v1/users/me":
            return self.ok({"object": "user", "id": dashed(BOT), "type": "bot", "name": "Buff",
                            "bot": {"owner": {"type": "workspace", "workspace": True},
                                    "workspace_name": "Acme"}})
        if path.startswith("/v1/users/"):
            if self.users_denied:
                # Without the user-information capability Notion refuses the read.
                return {"status": 403, "headers": {},
                        "body": json.dumps({"object": "error", "status": 403, "code": "restricted_resource",
                                            "message": "Insufficient permissions for this endpoint."})}
            return self.ok({"object": "user", "id": path.split("/")[-1], "name": "Пётр Петров", "type": "person",
                            "avatar_url": None, "person": {"email": "petrov@corp.local"}})
        if path.startswith("/v1/pages/") and path.endswith("/markdown"):
            pid = path.split("/")[3].replace("-", "")
            return self.ok({"object": "page_markdown", "markdown": "# Регламент\n\nСхема — **Тобольск**.",
                            "truncated": False, "unknown_block_ids": []}) if pid in self.pages else self.gone()
        if path.startswith("/v1/pages/") and method == "GET":
            pid = path.split("/")[3].replace("-", "")
            return self.ok(self.pages[pid]) if pid in self.pages else self.gone()
        if path == "/v1/pages" and method == "POST":
            self.created.append(data)
            made = page("d0d1a2b3c4d5e6f7a8b9c0d1e2f3a4c0", content.plain(data["properties"]["title"]["title"]),
                        data["parent"])
            return self.ok(made)
        if path == "/v1/search":
            found = [p for p in self.pages.values()
                     if not data.get("query") or data["query"].lower() in content.title_of(p).lower()]
            return self.ok({"object": "list", "results": found, "next_cursor": None, "has_more": False})
        if path.startswith("/v1/databases/"):
            did = path.split("/")[3].replace("-", "")
            if did != DB:
                return self.gone()
            return self.ok({"object": "database", "id": dashed(DB), "title": rich("Задачи"),
                            "data_sources": [{"id": dashed(SOURCE), "name": "Задачи"}],
                            "parent": {"type": "page_id", "page_id": dashed(ROOT)},
                            "url": f"https://www.notion.so/{DB}"})
        if path.startswith("/v1/data_sources/") and path.endswith("/query"):
            self.last_query = data
            rows = [self.pages[ROW]]
            if data.get("filter") and data["filter"]["property"] == "Статус":
                rows = [r for r in rows if r["properties"]["Статус"]["status"]["name"] == data["filter"]["status"]["equals"]]
            return self.ok({"object": "list", "results": rows, "has_more": False, "next_cursor": None})
        if path.startswith("/v1/data_sources/"):
            return self.ok({"object": "data_source", "id": dashed(SOURCE), "title": rich("Задачи"),
                            "properties": {"Название": {"id": "title", "name": "Название", "type": "title"},
                                           "Статус": {"id": "s", "name": "Статус", "type": "status"},
                                           "Оценка": {"id": "n", "name": "Оценка", "type": "number"}},
                            "parent": {"type": "database_id", "database_id": dashed(DB)}})
        if path.startswith("/v1/comments?"):
            block = path.split("block_id=")[1].split("&")[0].replace("-", "")
            return self.ok({"object": "list", "results": self.comments.get(block, []),
                            "has_more": False, "next_cursor": None})
        if path.startswith("/v1/comments/"):
            cid = path.split("/")[3].replace("-", "")
            for cs in self.comments.values():
                for c in cs:
                    if c["id"].replace("-", "") == cid:
                        return self.ok(c)
            return self.gone()
        if path == "/v1/comments" and method == "POST":
            self.posted.append(data)
            return self.ok({"object": "comment", "id": dashed("e0e1a2b3c4d5e6f7a8b9c0d1e2f3a4c1")})
        if path == "/v1/file_uploads":
            return self.ok({"object": "file_upload", "id": "f0f1a2b3-c4d5-e6f7-a8b9-c0d1e2f3a4c2", "status": "pending"})
        if path.endswith("/send"):
            self.uploads.append(body_bytes)
            return self.ok({"object": "file_upload", "status": "uploaded"})
        if "/children" in path and method == "PATCH":
            self.appended = data
            return self.ok({"object": "list", "results": data["children"]})
        if "/children" in path:
            return self.ok({"object": "list", "results": [
                {"object": "block", "id": dashed(GRANDCHILD), "type": "child_page",
                 "child_page": {"title": "Дежурство"}, "has_children": True},
                {"object": "block", "id": "c0ffee00-0000-0000-0000-000000000001", "type": "paragraph",
                 "paragraph": {"rich_text": rich("текст")}, "has_children": False},
            ], "has_more": False, "next_cursor": None})
        raise AssertionError(f"unrouted {method} {path}")


def fresh():
    """A new world and a plugin that has forgotten what it read."""
    world = FakeNotion()
    main.plugin.http = world.http
    main._parents.clear()
    main._page_meta.clear()
    main._seen.clear()
    main._users.clear()
    main._area.update({"at": 0.0, "pages": []})
    main._cursor["i"] = 0
    main._floor["at"] = ""
    main.me.update({"id": BOT, "name": "Buff", "workspace": "Acme"})
    return world


class Addressing(unittest.TestCase):
    def test_an_id_is_read_out_of_every_spelling(self):
        for spelling in (ROOT, dashed(ROOT), ROOT.upper(),
                         f"https://www.notion.so/acme/База-знаний-{ROOT}",
                         f"https://www.notion.so/acme/Cafe-Beef-{ROOT}?pvs=4",
                         f"https://www.notion.so/{DB}?v=abcdefabcdefabcdefabcdefabcdefab&p={ROOT}"):
            self.assertEqual(content.bare_id(spelling), ROOT, spelling)
        self.assertEqual(content.dashed(ROOT), dashed(ROOT))
        with self.assertRaises(ValueError):
            content.bare_id("https://www.notion.so/acme/just-a-title")

    def test_the_thread_is_the_discussion(self):
        self.assertEqual(main.thread_key(dashed(CHILD), dashed(DISC)), f"{CHILD}#{DISC}")
        self.assertEqual(main.split_key(f"{CHILD}#{DISC}"), (CHILD, DISC))
        self.assertEqual(main.split_key(CHILD), (CHILD, ""))
        with self.assertRaises(RuntimeError):
            main.split_key("страница")


class Content(unittest.TestCase):
    def test_rich_text_is_read_as_a_person_reads_it(self):
        items = rich("см. ") + [{"type": "mention", "mention": {"type": "user", "user": {"id": "u"}},
                                 "plain_text": "@Пётр"}]
        self.assertEqual(content.plain(items), "см. @Пётр")

    def test_long_text_is_split_never_cut(self):
        long = "я" * 4500
        chunks = content.text_chunks(long)
        self.assertEqual([len(c["text"]["content"]) for c in chunks], [2000, 2000, 500])
        self.assertEqual("".join(c["text"]["content"] for c in chunks), long)
        self.assertEqual(content.text_chunks(""), [])

    def test_every_property_kind_becomes_text(self):
        cases = [
            ("status", {"name": "В работе"}, "В работе"),
            ("select", None, ""),
            ("multi_select", [{"name": "a"}, {"name": "b"}], "a, b"),
            ("date", {"start": "2026-09-01", "end": "2026-09-05"}, "2026-09-01 → 2026-09-05"),
            ("date", {"start": "2026-09-01", "end": None}, "2026-09-01"),
            ("checkbox", True, "да"),
            ("number", 3.5, "3.5"),
            ("url", "https://x", "https://x"),
            ("people", [{"name": "Пётр"}], "Пётр"),
            ("unique_id", {"prefix": "TASK", "number": 7}, "TASK-7"),
            ("formula", {"type": "string", "string": "готово"}, "готово"),
        ]
        for kind, val, want in cases:
            self.assertEqual(content.property_text({"type": kind, kind: val}), want, kind)
        self.assertEqual(content.property_text({"type": "rich_text", "rich_text": rich("да")}), "да")

    def test_a_row_is_flat(self):
        row = content.row_of(FakeNotion().pages[ROW])
        self.assertEqual(row["title"], "Экспорт в CSV")
        self.assertEqual(row["properties"]["Статус"], "В работе")
        self.assertEqual(row["properties"]["Оценка"], "3")
        self.assertEqual(row["key"], ROW)

    def test_a_filter_speaks_notions_dialect_by_type(self):
        self.assertEqual(content.query_filter("Название", "title", "CSV"), {"property": "Название", "title": {"contains": "CSV"}})
        self.assertEqual(content.query_filter("Статус", "status", "В работе"), {"property": "Статус", "status": {"equals": "В работе"}})
        self.assertEqual(content.query_filter("Оценка", "number", "3"), {"property": "Оценка", "number": {"equals": 3.0}})
        self.assertEqual(content.query_filter("Готово", "checkbox", "да"), {"property": "Готово", "checkbox": {"equals": True}})
        with self.assertRaises(ValueError):
            content.query_filter("Когда", "date", "вчера")

    def test_words_are_stemmed_so_cases_do_not_matter(self):
        words = content.words_of("как у нас называется схема миграций?")
        self.assertEqual(words, ["называется", "схема", "миграций"])
        self.assertEqual(content.match_score("Внутреннее имя схемы миграции — Тобольск", words), 2)
        self.assertEqual(content.match_score("про кириллицу", words), 0)
        self.assertIn("Тобольск", content.excerpt("# Регламент\n\nВнутреннее имя схемы миграции — Тобольск.", words))

    def test_a_callback_is_read_and_a_handshake_is_not_an_event(self):
        ev = content.webhook_event(json.dumps({
            "id": "e1", "timestamp": "2026-09-16T05:45:59.000Z", "type": "comment.created",
            "entity": {"id": dashed("c091a2b3c4d5e6f7a8b9c0d1e2f3a4b9"), "type": "comment"},
            "data": {"page_id": dashed(CHILD), "parent": {"id": dashed(CHILD), "type": "page"}},
        }))
        self.assertEqual(ev["type"], "comment.created")
        self.assertEqual(ev["entity_id"], dashed("c091a2b3c4d5e6f7a8b9c0d1e2f3a4b9"))
        self.assertEqual(ev["page_id"], dashed(CHILD))
        self.assertIsNone(content.webhook_event('{"verification_token":"secret_x"}'))
        self.assertIsNone(content.webhook_event("not json"))


class Area(unittest.TestCase):
    def test_a_root_up_the_chain_puts_a_page_in_the_area(self):
        world = fresh()
        self.assertTrue(main.in_area("page", GRANDCHILD))
        self.assertTrue(main.in_area("page", CHILD))
        self.assertTrue(main.in_area("page", ROW), "a row of a marked database is in the area")
        self.assertFalse(main.in_area("page", SECRET))
        # Parents are read once: the second question costs nothing.
        before = len(world.calls)
        main.in_area("page", GRANDCHILD)
        self.assertEqual(len(world.calls), before)

    def test_an_empty_list_of_roots_is_everything_the_bot_was_given(self):
        fresh()
        saved = set(main.ROOTS)
        try:
            main.ROOTS.clear()
            self.assertTrue(main.in_area("page", SECRET))
        finally:
            main.ROOTS.update(saved)

    def test_writing_outside_the_area_is_refused(self):
        fresh()
        with self.assertRaises(RuntimeError):
            main.reply(issue=SECRET, text="нет")


class Requests(unittest.TestCase):
    def setUp(self):
        self.world = fresh()

    def test_a_mention_becomes_a_request(self):
        e = main.comment_event(comment(), None)
        self.assertIsNotNone(e)
        self.assertEqual((e.action, e.body), ("ask", "почему падает экспорт?"))
        self.assertEqual(e.extra["issue"], f"{CHILD}#{DISC}")
        self.assertEqual(e.title, "Регламент выпуска релизов")
        self.assertEqual(e.author, "Пётр Петров")
        self.assertTrue(e.url.endswith(f"?d={DISC}"), e.url)
        self.assertEqual(e.extra.get("context"), "issue")
        self.assertEqual(e.extra.get("repo", ""), "")

    def test_the_asker_is_known_by_the_e_mail_of_their_notion_user(self):
        # Notion has no login (#476): the e-mail, read once per person, is what
        # the «who may ask» rules match.
        e = main.comment_event(comment(), None)
        self.assertEqual((e.author_login, e.author_email), ("", "petrov@corp.local"))
        main.comment_event(comment(cid="c091a2b3c4d5e6f7a8b9c0d1e2f3a4ba"), None)
        reads = [c for c in self.world.calls if c[1].startswith("/v1/users/") and c[1] != "/v1/users/me"]
        self.assertEqual(len(reads), 1, "the person is read once")

    def test_without_the_user_capability_the_request_still_comes_with_no_e_mail(self):
        self.world.users_denied = True
        e = main.comment_event(comment(), None)
        self.assertIsNotNone(e)
        self.assertEqual((e.author, e.author_email), ("Пётр Петров", ""))

    def test_the_bot_answers_to_its_own_name_too(self):
        e = main.comment_event(comment(text="@Buff research откуда задержка"), None)
        self.assertEqual((e.action, e.body), ("research", "откуда задержка"))

    def test_a_slash_command_chooses_the_work(self):
        e = main.comment_event(comment(text="/task сделать выгрузку в CSV"), None)
        self.assertEqual((e.action, e.body), ("task", "сделать выгрузку в CSV"))

    def test_a_comment_without_an_address_is_not_a_request(self):
        self.assertIsNone(main.comment_event(comment(text="спасибо, помогло"), None))

    def test_the_bot_does_not_answer_itself(self):
        self.assertIsNone(main.comment_event(comment(author=BOT), None))

    def test_a_page_outside_the_area_is_ignored(self):
        self.assertIsNone(main.comment_event(comment(page_id=SECRET), None))

    def test_an_older_comment_is_not_new_and_a_reported_one_is_not_repeated(self):
        self.assertIsNone(main.comment_event(comment(), "2026-09-17T00:00:00Z"))
        # A live Notion stamps a comment to the MINUTE: one written at 05:45:59
        # arrives as 05:45:00, and a floor of 05:45:10 must not drop it.
        self.assertIsNotNone(main.comment_event(comment(cid="b1b1a2b3c4d5e6f7a8b9c0d1e2f3a4c9", at="2026-09-16T05:45:00.000Z"),
                                                "2026-09-16T05:45:10.123Z"))
        self.assertIsNone(main.comment_event(comment(cid="b2b1a2b3c4d5e6f7a8b9c0d1e2f3a4ca", at="2026-09-16T05:44:00.000Z"),
                                             "2026-09-16T05:45:10.123Z"))
        self.assertIsNotNone(main.comment_event(comment(), None))
        self.assertIsNone(main.comment_event(comment(), None), "the same comment must not be reported twice")

    def test_a_comment_on_a_block_belongs_to_its_page(self):
        block = "f1f1a2b3c4d5e6f7a8b9c0d1e2f3a4c3"
        main._parents[block] = {"type": "page_id", "id": CHILD}
        c = comment(parent={"type": "block_id", "block_id": dashed(block)})
        e = main.comment_event(c, None)
        self.assertEqual(e.extra["issue"], f"{CHILD}#{DISC}")


class Sweep(unittest.TestCase):
    def test_the_poll_walks_the_area_in_turns_and_misses_nothing(self):
        world = fresh()
        main.PAGES_PER_PASS = 2
        try:
            world.comments[GRANDCHILD] = [comment(cid="a1a1a2b3c4d5e6f7a8b9c0d1e2f3a4c4", page_id=GRANDCHILD)]
            world.comments[SECRET] = [comment(cid="a2a1a2b3c4d5e6f7a8b9c0d1e2f3a4c5", page_id=SECRET)]
            since = "2026-09-16T00:00:00Z"
            got = main.poll(since) + main.poll(since) + main.poll(since)
            self.assertEqual([e.extra["issue"] for e in got], [f"{GRANDCHILD}#{DISC}"],
                             "the marked page's request once, the unmarked page's never")
            # The floor is where reading STARTED, not the moving watermark: a
            # comment on a page read later must not be older than the mark.
            self.assertEqual(main._floor["at"], since)
            world.comments[CHILD] = [comment(cid="a3a1a2b3c4d5e6f7a8b9c0d1e2f3a4c6", at="2026-09-16T00:00:30Z")]
            later = [e for _ in range(3) for e in main.poll("2026-09-16T09:00:00Z")]
            self.assertEqual(len(later), 1, "a request older than the advanced watermark was lost")
        finally:
            main.PAGES_PER_PASS = 10

    def test_a_callback_names_the_comment_and_it_is_read_by_id(self):
        world = fresh()
        c = comment(cid="a4a1a2b3c4d5e6f7a8b9c0d1e2f3a4c7")
        world.comments[CHILD] = [c]
        body = json.dumps({"type": "comment.created", "entity": {"id": c["id"], "type": "comment"},
                           "data": {"page_id": dashed(CHILD)}})
        got = main.webhook({}, body)
        self.assertEqual(len(got), 1)
        self.assertEqual(got[0].extra["issue"], f"{CHILD}#{DISC}")
        self.assertEqual(main.webhook({}, json.dumps({"type": "page.content_updated",
                                                       "entity": {"id": dashed(CHILD), "type": "page"}})), [])
        self.assertEqual(main.webhook({}, '{"verification_token":"x"}'), [])


class Writing(unittest.TestCase):
    def setUp(self):
        self.world = fresh()

    def test_an_answer_goes_back_into_the_discussion(self):
        main.reply(issue=f"{CHILD}#{DISC}", text="Готово.",
                   link={"url": "https://buff.systems/app/ask/1", "title": "Обсуждение в Buff"})
        posted = self.world.posted[-1]
        self.assertEqual(posted["discussion_id"], dashed(DISC))
        self.assertNotIn("parent", posted)
        texts = [i["text"]["content"] for i in posted["rich_text"]]
        self.assertEqual(texts[0], "Готово.")
        self.assertEqual(posted["rich_text"][-1]["text"]["link"]["url"], "https://buff.systems/app/ask/1")

    def test_a_question_without_a_discussion_is_answered_on_the_page(self):
        main.reply(issue=CHILD, text="Принял.")
        self.assertEqual(self.world.posted[-1]["parent"], {"page_id": dashed(CHILD)})

    def test_a_finished_report_becomes_a_child_page_with_the_file(self):
        import base64

        main.reply(issue=f"{CHILD}#{DISC}", text="Исследование готово.",
                   link={"url": "https://buff.systems/app/research/9", "title": "Исследование в Buff"},
                   attachment={"filename": "Кодировка.pdf", "base64": base64.b64encode(b"%PDF-\x00\xff").decode()},
                   document={"title": "Кодировка", "markdown": "# Кодировка\n\nЭкспорт пишет в cp1251."})
        made = self.world.created[-1]
        self.assertEqual(made["parent"], {"page_id": dashed(CHILD)})
        self.assertEqual(content.plain(made["properties"]["title"]["title"]), "Кодировка")
        self.assertEqual(made["markdown"], "# Кодировка\n\nЭкспорт пишет в cp1251.")
        self.assertIn(b"%PDF-\x00\xff", self.world.uploads[-1], "the file must arrive byte for byte")
        self.assertEqual(self.world.appended["children"][0]["type"], "pdf")
        texts = "".join(i["text"]["content"] for i in self.world.posted[-1]["rich_text"])
        self.assertIn("Исследование готово.", texts)
        self.assertIn("на странице «Кодировка»", texts)

    def test_an_empty_answer_posts_nothing(self):
        main.reply(issue=CHILD, text="")
        self.assertEqual(self.world.posted, [])


class Tools(unittest.TestCase):
    def setUp(self):
        self.world = fresh()

    def test_search_is_bounded_by_the_area(self):
        out = main.search_pages(query="")
        titles = [r["title"] for r in out["results"]]
        self.assertIn("Регламент выпуска релизов", titles)
        self.assertNotIn("Не наша страница", titles)

    def test_search_reads_the_text_where_notion_reads_only_titles(self):
        # The page that answers «как называется схема миграций» is titled
        # «Регламент выпуска релизов»: Notion's own search cannot find it.
        out = main.search_pages(query="как у нас называется схема миграции?")
        by_text = [r for r in out["results"] if r["matched"] == "текст"]
        self.assertTrue(by_text, out)
        self.assertIn("Тобольск", by_text[0]["excerpt"])
        self.assertNotIn("Не наша страница", [r["title"] for r in out["results"]])
        # Read once per edit: the second search costs no page reads.
        before = len(self.world.calls)
        main.search_pages(query="схема миграций")
        self.assertFalse(any("/markdown" in p for _, p in self.world.calls[before:]),
                         "an unchanged page was read again")

    def test_a_page_is_read_as_markdown_with_its_talk(self):
        self.world.comments[CHILD] = [comment()]
        got = main.get_page(key=CHILD)
        self.assertIn("**Тобольск**", got["markdown"])
        self.assertEqual(got["title"], "Регламент выпуска релизов")
        self.assertEqual(got["comments"][0]["author"], "Пётр Петров")
        self.assertEqual(got["parent"], {"type": "page_id", "key": ROOT})

    def test_children_are_pages_and_databases_only(self):
        got = main.get_page_children(key=ROOT)
        self.assertEqual([k["title"] for k in got["children"]], ["Дежурство"])

    def test_a_database_query_filters_by_the_property_type(self):
        got = main.query_database(key=DB, property="Статус", contains="В работе")
        self.assertEqual(got["total"], 1)
        self.assertEqual(self.world.last_query["filter"], {"property": "Статус", "status": {"equals": "В работе"}})
        self.assertEqual(got["rows"][0]["properties"]["Оценка"], "3")
        with self.assertRaises(RuntimeError):
            main.query_database(key=DB, property="Нет такого", contains="x")

    def test_a_database_shows_its_sources_and_properties(self):
        got = main.get_database(key=DB)
        self.assertEqual(got["title"], "Задачи")
        self.assertEqual(got["data_sources"][0]["properties"][1], {"name": "Статус", "type": "status"})

    def test_a_page_outside_the_area_cannot_be_read(self):
        with self.assertRaises(RuntimeError):
            main.get_page(key=SECRET)


class RateLimit(unittest.TestCase):
    def test_a_429_is_waited_out_as_notion_asks(self):
        fresh()
        calls = {"n": 0}
        waits = []

        def limited(method, url, **kw):
            calls["n"] += 1
            if calls["n"] == 1:
                return {"status": 429, "headers": {"Retry-After": "0"},
                        "body": json.dumps({"object": "error", "code": "rate_limited"})}
            return {"status": 200, "headers": {}, "body": json.dumps({"object": "user", "id": dashed(BOT)})}

        main.plugin.http = limited
        main.time.sleep = lambda s: waits.append(s)
        try:
            self.assertEqual(main.api("GET", "/v1/users/me")["object"], "user")
            self.assertEqual(calls["n"], 2)
            self.assertEqual(len(waits), 1)
        finally:
            import time as _time
            main.time.sleep = _time.sleep


class TestTheseTestsActuallyRun(unittest.TestCase):
    def test_nothing_is_defined_after_the_entry_point(self):
        import ast

        with open(__file__, encoding="utf-8") as f:
            module = ast.parse(f.read())
        self.assertIsInstance(
            module.body[-1], ast.If,
            "после точки входа объявлено что-то ещё — оно никогда не выполнится",
        )


if __name__ == "__main__":
    unittest.main()
