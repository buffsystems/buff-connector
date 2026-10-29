"""Tests for the Google Drive plugin's reading and writing logic (#349).

The fixtures follow the shapes Drive API v3 and Sheets API v4 document: a file
with `parents`, `mimeType` and `webViewLink`; a comment with `author {displayName,
emailAddress, me}`, `content`, `createdTime` and its `replies`; a spreadsheet's
`sheets[].properties.title` and `values:batchGet`'s `valueRanges`.

The plugin's HTTP goes through a fake host here: one small world — a root
folder with a subfolder, a document, a sheet, a deck, and a document NOBODY
marked — answered in Google's own JSON, so the area check, the sweep and the
writes are exercised end to end without a process.
"""

import json
import os
import re
import sys
import unittest
from unittest import mock
from urllib.parse import parse_qs, unquote, urlparse

HERE = os.path.dirname(__file__)
sys.path.insert(0, os.path.join(HERE, "..", "..", "..", "sdk"))

ROOT = "1RootFolderAAAAAAAAAAAAAAAAAAAAAA"
SUB = "1SubFolderBBBBBBBBBBBBBBBBBBBBBBB"
DOC = "1DocRulesCCCCCCCCCCCCCCCCCCCCCCCC"
SHEET = "1SheetTasksDDDDDDDDDDDDDDDDDDDDDD"
DECK = "1DeckPlanEEEEEEEEEEEEEEEEEEEEEEEE"
OTHER = "1OtherDocFFFFFFFFFFFFFFFFFFFFFFFF"
SA_EMAIL = "buff-bot@acme-project.iam.gserviceaccount.com"
HUMAN_EMAIL = "petrov@example.com"

os.environ["BUFF_CONFIG"] = json.dumps({
    "drive_api": "https://www.googleapis.com",
    "sheets_api": "https://sheets.googleapis.com",
    "roots": [f"https://drive.google.com/drive/folders/{ROOT}"],
    "mention_alias": "buff",
})
sys.path.insert(0, os.path.join(HERE, ".."))

import content  # noqa: E402
import main  # noqa: E402

G = "application/vnd.google-apps."


def file(fid, name, mime, parents):
    return {"id": fid, "name": name, "mimeType": mime, "parents": parents,
            "webViewLink": f"https://docs.google.com/document/d/{fid}/edit" if mime == content.DOC
            else f"https://drive.google.com/drive/folders/{fid}", "modifiedTime": "2026-09-10T12:00:00.000Z"}


def author(email=HUMAN_EMAIL, name="Пётр Петров", me=False):
    return {"displayName": name, "emailAddress": email, "me": me}


def comment(cid="AAAAcomment1", text="@buff почему падает экспорт?", at="2026-09-16T05:45:59.123Z",
            who=None, replies=None, **kw):
    """A comment as `comments.list` returns it."""
    c = {"id": cid, "author": who or author(), "content": text, "createdTime": at, "modifiedTime": at,
         "resolved": False, "deleted": False, "replies": replies or []}
    c.update(kw)
    return c


class FakeDrive:
    """The customer's Drive and Sheets, answered in their own JSON."""

    def __init__(self, user_email=SA_EMAIL, user_name="buff-bot"):
        self.user = {"displayName": user_name, "emailAddress": user_email}
        self.files = {
            ROOT: file(ROOT, "База знаний", content.FOLDER, []),
            SUB: file(SUB, "Регламенты", content.FOLDER, [ROOT]),
            DOC: file(DOC, "Регламент выпуска релизов", content.DOC, [SUB]),
            SHEET: file(SHEET, "Задачи", content.SHEET, [ROOT]),
            DECK: file(DECK, "План на квартал", content.SLIDES, [ROOT]),
            OTHER: file(OTHER, "Не наш документ", content.DOC, []),
        }
        self.comments = {}
        self.posted = []
        self.uploads = []
        self.calls = []
        self.markdown = "# Регламент\n\nСхема миграций — **Верхотурье**.\n"

    def http(self, method, url, credential="", headers=None, body="", binary=False, body_bytes=None):
        u = urlparse(url)
        q = {k: v[0] if len(v) == 1 else v for k, v in parse_qs(u.query).items()}
        self.calls.append((method, u.path, q))
        assert credential == "api", credential
        data = json.loads(body) if body and not body_bytes else {}
        return self.route(method, u.netloc, unquote(u.path), q, data, body_bytes, headers or {})

    def ok(self, obj):
        return {"status": 200, "headers": {}, "body": obj if isinstance(obj, str) else json.dumps(obj)}

    def gone(self, what="File"):
        return {"status": 404, "headers": {}, "body": json.dumps({"error": {"code": 404, "message": f"{what} not found: x.",
                                                                              "errors": [{"reason": "notFound"}]}})}

    def route(self, method, host, path, q, data, raw, headers):
        if host == "sheets.googleapis.com":
            return self.sheets(path, q)
        if path == "/drive/v3/about":
            return self.ok({"user": self.user})
        if path == "/upload/drive/v3/files":
            assert q.get("uploadType") == "multipart"
            self.uploads.append((headers.get("Content-Type", ""), raw))
            meta = json.loads(raw.split(b"\r\n\r\n")[1].split(b"\r\n--")[0])
            made = file("1MadeDocGGGGGGGGGGGGGGGGGGGGGGGGG", meta["name"], meta["mimeType"], meta.get("parents", []))
            if meta["mimeType"] == "application/pdf":
                made["id"] = "1MadePdfHHHHHHHHHHHHHHHHHHHHHHHHH"
                made["webViewLink"] = f"https://drive.google.com/file/d/{made['id']}/view"
            self.files[made["id"]] = made
            return self.ok(made)
        if path == "/drive/v3/files" and method == "GET":
            return self.ok({"files": self.list(q.get("q", ""))})
        m = re.match(r"^/drive/v3/files/([^/]+)(/.*)?$", path)
        if not m:
            raise AssertionError(f"unrouted {method} {path}")
        fid, rest = m.group(1), m.group(2) or ""
        f = self.files.get(fid)
        if not f:
            return self.gone()
        if rest == "" and q.get("alt") == "media":
            return self.ok("plain text of the file")
        if rest == "":
            return self.ok(f)
        if rest == "/export":
            return self.ok(self.markdown if q["mimeType"] == "text/markdown" else "Slide 1\nПлан: выпуск в октябре\n")
        if rest == "/comments" and method == "GET":
            assert "fields" in q, "Drive refuses comments calls without fields"
            return self.ok({"comments": self.comments.get(fid, [])})
        if rest == "/comments" and method == "POST":
            self.posted.append(("comment", fid, "", data["content"]))
            return self.ok({"id": "AAAAposted1"})
        r = re.match(r"^/comments/([^/]+)/replies$", rest)
        if r and method == "POST":
            self.posted.append(("reply", fid, r.group(1), data["content"]))
            return self.ok({"id": "AAAAreply1"})
        raise AssertionError(f"unrouted {method} {path}")

    def list(self, qs):
        parent = re.search(r"'([^']+)' in parents", qs)
        # Drive's way: a clause wants every word of its phrase; clauses joined
        # by `or` want any one of them. The text is the name plus the body.
        clauses = re.findall(r"(?:fullText|name) contains '((?:[^'\\]|\\.)*)'", qs)
        found = []
        for f in self.files.values():
            if parent and parent.group(1) not in f["parents"]:
                continue
            hay = (f["name"] + " " + (self.markdown if f["mimeType"] == content.DOC else "")).lower()
            if clauses and not any(all(w in hay.split() or w in hay for w in c.lower().split()) for c in clauses):
                continue
            if "mimeType = " in qs and not any(f["mimeType"] == m for m in re.findall(r"mimeType = '([^']+)'", qs)):
                continue
            found.append(f)
        return found

    def sheets(self, path, q):
        if path.endswith("/values:batchGet"):
            ranges = q["ranges"] if isinstance(q["ranges"], list) else [q["ranges"]]
            self.last_ranges = ranges
            return self.ok({"valueRanges": [
                {"range": f"{r}!A1:C3", "majorDimension": "ROWS",
                 "values": [["Задача", "Статус", "Оценка"], ["Экспорт в CSV", "В работе", 3], ["Кодировка", "Готово"]]}
                for r in ranges]})
        if re.search(r"/values/", path):
            return self.ok({"range": "'Бюджет'!A1:B2", "values": [["Статья", "Сумма"], ["Хостинг", 120]]})
        return self.ok({"sheets": [{"properties": {"title": "Задачи"}}, {"properties": {"title": "Бюджет"}}]})


def fresh(**kw):
    """A new world and a plugin that has forgotten what it read."""
    world = FakeDrive(**kw)
    main.plugin.http = world.http
    main.plugin.log = lambda *a, **k: None
    main._meta.clear()
    main._bodies.clear()
    main._seen.clear()
    main._area.update({"at": 0.0, "files": []})
    main._cursor["i"] = 0
    main._floor["at"] = ""
    main.me.update({"name": world.user["displayName"], "email": world.user["emailAddress"]})
    return world


class Addressing(unittest.TestCase):
    def test_an_id_is_read_out_of_every_spelling(self):
        for spelling in (DOC, f"https://docs.google.com/document/d/{DOC}/edit?usp=sharing",
                         f"https://docs.google.com/spreadsheets/d/{DOC}/edit#gid=0",
                         f"https://drive.google.com/drive/folders/{DOC}",
                         f"https://drive.google.com/file/d/{DOC}/view",
                         f"https://drive.google.com/open?id={DOC}"):
            self.assertEqual(content.bare_id(spelling), DOC, spelling)
        with self.assertRaises(ValueError):
            content.bare_id("https://docs.google.com/document/")
        with self.assertRaises(ValueError):
            content.bare_id("")

    def test_the_thread_is_the_comment(self):
        self.assertEqual(content.thread_key(DOC, "AAAAc1"), f"{DOC}#AAAAc1")
        self.assertEqual(content.split_key(f"{DOC}#AAAAc1"), (DOC, "AAAAc1"))
        self.assertEqual(content.split_key(DOC), (DOC, ""))
        with self.assertRaises(RuntimeError):
            content.split_key("документ")

    def test_a_discussion_opens_by_its_anchor(self):
        self.assertEqual(content.comment_url("https://docs.google.com/document/d/x/edit", "c1"),
                         "https://docs.google.com/document/d/x/edit?disco=c1")
        self.assertEqual(content.comment_url("https://x/edit?usp=s", "c1"), "https://x/edit?usp=s&disco=c1")
        self.assertEqual(content.comment_url("https://x", ""), "https://x")


class Content(unittest.TestCase):
    def test_kinds_read_as_a_person_names_them(self):
        self.assertEqual(content.kind_of(content.DOC), "документ")
        self.assertEqual(content.kind_of(content.SHEET), "таблица")
        self.assertEqual(content.kind_of(content.FOLDER), "папка")
        self.assertEqual(content.kind_of("image/png"), "изображение")
        self.assertEqual(content.kind_of("application/zip"), "файл")

    def test_a_query_value_cannot_break_out_of_its_quotes(self):
        self.assertEqual(content.q_string("O'Neil \\ x"), "'O\\'Neil \\\\ x'")

    def test_a_sheet_is_a_markdown_table_never_cut(self):
        rows = [["A", "B"], ["1"], ["x|y", "line\nbreak"]] + [[str(i)] for i in range(500)]
        md = content.sheet_markdown("Лист", rows)
        self.assertTrue(md.startswith("## Лист\n\n| A | B |\n|---|---|\n| 1 |  |\n| x\\|y | line break |\n"))
        self.assertEqual(md.count("\n"), 4 + 2 + 500)
        self.assertEqual(content.sheet_markdown("Пусто", []), "## Пусто\n\n(пусто)\n")

    def test_multipart_is_metadata_then_bytes(self):
        ctype, body = content.multipart_related({"name": "Отчёт", "mimeType": content.DOC}, "text/markdown", "# Привет".encode())
        boundary = ctype.split("boundary=")[1]
        self.assertTrue(ctype.startswith("multipart/related; boundary="))
        parts = body.split(f"--{boundary}".encode())
        self.assertEqual(len(parts), 4)  # empty, metadata, media, closing "--"
        self.assertIn(b'"name": "\xd0\x9e\xd1\x82\xd1\x87\xd1\x91\xd1\x82"', parts[1])
        self.assertTrue(parts[2].startswith(b"\r\nContent-Type: text/markdown\r\n\r\n# \xd0\x9f"))
        self.assertEqual(parts[3], b"--\r\n")

    def test_the_window_opens_a_minute_before_the_floor(self):
        self.assertEqual(content.a_minute_before("2026-09-16T05:45:59.123Z"), "2026-09-16T05:44:59.000Z")
        self.assertEqual(content.a_minute_before("2026-09-16T08:45:00+03:00"), "2026-09-16T05:44:00.000Z")
        self.assertEqual(content.a_minute_before("garbage"), "")

    def test_words_stems_and_the_search_query(self):
        self.assertEqual(content.words_of("Как у нас называется схема миграций? Ответь."), ["называется", "схема", "миграций", "ответь"])
        self.assertEqual(content.stem("миграций"), "миграц")
        self.assertEqual(content.stem("схема"), "схем")  # never shorter than four letters
        self.assertEqual(content.match_score("Внутреннее имя схемы миграций", ["схема", "миграции"]), 2)
        self.assertTrue(content.excerpt("a " * 300 + "схема миграций — Верхотурье", ["миграции"]).startswith("…"))
        self.assertEqual(content.search_q([]), "trashed = false")

    def test_an_author_is_named_by_name_and_address(self):
        self.assertEqual(content.author_text(author()), "Пётр Петров (petrov@example.com)")
        self.assertEqual(content.author_text({"displayName": "x"}), "x")
        self.assertEqual(content.author_text(None), "")


class Area(unittest.TestCase):
    def test_a_file_belongs_when_a_root_is_up_its_parents(self):
        fresh()
        self.assertTrue(main.in_area(DOC))     # root → sub → doc
        self.assertTrue(main.in_area(SHEET))   # root → sheet
        self.assertTrue(main.in_area(ROOT))
        self.assertFalse(main.in_area(OTHER))  # no parents at all
        self.assertFalse(main.in_area("1Unknown000000000000000000000000"))

    def test_parents_are_looked_up_once(self):
        world = fresh()
        main.in_area(DOC)
        main.in_area(DOC)
        gets = [c for c in world.calls if c[1] == f"/drive/v3/files/{DOC}"]
        self.assertEqual(len(gets), 1)

    def test_no_roots_means_everything_the_account_sees(self):
        fresh()
        saved = set(main.ROOTS)
        main.ROOTS.clear()
        try:
            self.assertTrue(main.in_area(OTHER))
        finally:
            main.ROOTS.update(saved)

    def test_the_area_is_walked_folder_by_folder_and_holds_only_what_people_write_on(self):
        fresh()
        ids = [f["id"] for f in main.area_files()]
        self.assertEqual(sorted(ids), sorted([DOC, SHEET, DECK]))
        self.assertNotIn(OTHER, ids)
        self.assertNotIn(SUB, ids)

    def test_shared_drives_are_always_in_view(self):
        world = fresh()
        main.area_files()
        for _, path, q in world.calls:
            if path == "/drive/v3/files":
                self.assertEqual(q.get("supportsAllDrives"), "true")
                self.assertEqual(q.get("includeItemsFromAllDrives"), "true")


class Sweep(unittest.TestCase):
    def test_a_mention_in_a_comment_becomes_a_request_in_that_discussion(self):
        world = fresh()
        world.comments[DOC] = [comment()]
        events = main.poll("2026-09-16T05:00:00.000Z")
        self.assertEqual(len(events), 1)
        e = events[0]
        self.assertEqual(e.action, "ask")
        self.assertEqual(e.body, "почему падает экспорт?")
        self.assertEqual(e.title, "Регламент выпуска релизов")
        self.assertEqual(e.author, "Пётр Петров (petrov@example.com)")
        self.assertEqual(e.extra["issue"], f"{DOC}#AAAAcomment1")
        self.assertEqual(e.url, f"https://docs.google.com/document/d/{DOC}/edit?disco=AAAAcomment1")
        self.assertEqual(e.id, "comment:AAAAcomment1@2026-09-16T05:45:59.123Z")
        # Asked once: the next pass reports nothing new.
        self.assertEqual(main.poll("2026-09-16T05:00:00.000Z"), [])

    def test_the_asker_is_known_by_their_e_mail_when_drive_shows_it(self):
        # Drive has no login (#476); a hidden address is simply absent.
        world = fresh()
        hidden = {"displayName": "Анна Смирнова", "me": False}
        world.comments[DOC] = [comment(), comment(cid="AAAAcomment2", who=hidden)]
        got = [(e.author_login, e.author_email) for e in main.poll("2026-09-16T05:00:00.000Z")]
        self.assertEqual(got, [("", HUMAN_EMAIL), ("", "")])

    def test_a_reply_in_a_discussion_is_a_request_in_the_same_thread(self):
        world = fresh()
        world.comments[DOC] = [comment(text="Обсуждение", replies=[
            comment(cid="AAAAreply9", text="+buff-bot@acme-project.iam.gserviceaccount.com research как устроен экспорт?",
                    at="2026-09-16T05:50:00.000Z")])]
        events = main.poll("2026-09-16T05:00:00.000Z")
        self.assertEqual([(e.action, e.extra["issue"]) for e in events], [("research", f"{DOC}#AAAAcomment1")])
        self.assertEqual(events[0].body, "как устроен экспорт?")

    def test_the_bot_answers_to_its_display_name_and_address(self):
        world = fresh()
        world.comments[DOC] = [comment(text="@buff-bot ask что тут?"),
                               comment(cid="AAAAcomment2", text="@buff-bot@acme-project.iam.gserviceaccount.com что тут?")]
        self.assertEqual(len(main.poll(None)), 2)

    def test_a_service_accounts_own_words_are_not_requests(self):
        world = fresh()
        world.comments[DOC] = [comment(text="@buff вот ответ", who=author(SA_EMAIL, "buff-bot", me=True))]
        self.assertEqual(main.poll(None), [])

    def test_a_persons_own_account_asks_and_the_bots_replies_are_told_apart_by_id(self):
        # Signed in as the person: their comments ARE requests (author.me is
        # true for every one of them), and what the bot wrote is known by id.
        world = fresh(user_email=HUMAN_EMAIL, user_name="Пётр Петров")
        world.comments[DOC] = [comment(text="@buff ask что записано?", who=author(HUMAN_EMAIL, "Пётр Петров", me=True))]
        events = main.poll(None)
        self.assertEqual(len(events), 1)
        main.reply(issue=events[0].extra["issue"], text="Записано: @buff — так меня зовут")
        world.comments[DOC][0]["replies"].append(
            comment(cid="AAAAreply1", text="Записано: @buff — так меня зовут", at="2026-09-16T06:00:00.000Z",
                    who=author(HUMAN_EMAIL, "Пётр Петров", me=True)))
        self.assertEqual(main.poll(None), [], "the bot's own reply came back as a request")

    def test_a_comment_outside_the_area_starts_nothing(self):
        world = fresh()
        world.comments[OTHER] = [comment(text="@buff ask что тут?")]
        self.assertEqual(main.poll(None), [])
        # …even when handed directly, not through the sweep.
        self.assertEqual(main.comment_events(world.files[OTHER], comment(text="@buff ask что?"), None), [])

    def test_only_what_is_newer_than_the_floor_counts_and_the_window_is_widened(self):
        world = fresh()
        world.comments[DOC] = [comment(at="2026-09-16T04:00:00.000Z"),
                               comment(cid="AAAAcomment2", at="2026-09-16T05:30:00.000Z")]
        events = main.poll("2026-09-16T05:00:00.000Z")
        self.assertEqual([e.id.split(":")[1].split("@")[0] for e in events], ["AAAAcomment2"])
        lists = [q for _, path, q in world.calls if path.endswith("/comments")]
        self.assertEqual(lists[0].get("startModifiedTime"), "2026-09-16T04:59:00.000Z")

    def test_a_deleted_comment_and_a_comment_without_an_address_are_nothing(self):
        world = fresh()
        world.comments[DOC] = [comment(text="просто заметка"), comment(cid="AAAAc3", deleted=True)]
        self.assertEqual(main.poll(None), [])

    def test_the_sweep_goes_round_the_area(self):
        world = fresh()
        main.FILES_PER_PASS, saved = 2, main.FILES_PER_PASS
        try:
            main.poll(None)
            first = [path for _, path, _ in world.calls if path.endswith("/comments")]
            main.poll(None)
            second = [path for _, path, _ in world.calls if path.endswith("/comments")][len(first):]
            self.assertEqual(len(first), 2)
            self.assertEqual(len(second), 2)
            self.assertNotEqual(set(first), set(second))
        finally:
            main.FILES_PER_PASS = saved

    def test_an_unreadable_file_does_not_stop_the_pass(self):
        world = fresh()
        world.comments[SHEET] = [comment(cid="AAAAonsheet", text="@buff ask что в таблице?")]
        del world.files[DECK]
        main._area.update({"at": 0.0, "files": [world.files[SHEET], file(DECK, "gone", content.SLIDES, [ROOT])]})
        main._area["at"] = 9e12  # keep this list
        events = main.poll(None)
        self.assertEqual([e.extra["issue"] for e in events], [f"{SHEET}#AAAAonsheet"])


class Writing(unittest.TestCase):
    def test_an_answer_goes_back_into_the_discussion(self):
        world = fresh()
        main.reply(issue=f"{DOC}#AAAAcomment1", text="Экспорт падает на кодировке.",
                   link={"title": "Открыть в Buff", "url": "https://buff/t/1"})
        self.assertEqual(world.posted, [("reply", DOC, "AAAAcomment1",
                                         "Экспорт падает на кодировке.\n\nОткрыть в Buff: https://buff/t/1")])
        self.assertIn("AAAAreply1", main._seen)

    def test_a_key_without_a_discussion_opens_one(self):
        world = fresh()
        main.reply(issue=DOC, text="Заметка")
        self.assertEqual(world.posted, [("comment", DOC, "", "Заметка")])

    def test_a_report_becomes_a_document_beside_the_one_it_was_asked_on(self):
        world = fresh()
        main.reply(issue=f"{DOC}#AAAAcomment1", text="Готово",
                   document={"title": "Исследование экспорта", "markdown": "# Экспорт\n\nЛомается на кириллице."},
                   attachment={"filename": "report.pdf", "base64": "JVBERi0xLjQK"})
        self.assertEqual(len(world.uploads), 2)
        doc_meta = json.loads(world.uploads[0][1].split(b"\r\n\r\n")[1].split(b"\r\n--")[0])
        self.assertEqual(doc_meta, {"name": "Исследование экспорта", "mimeType": content.DOC, "parents": [SUB]})
        self.assertIn(b"Content-Type: text/markdown\r\n\r\n# \xd0\xad\xd0\xba\xd1\x81\xd0\xbf\xd0\xbe\xd1\x80\xd1\x82",
                      world.uploads[0][1])
        pdf_meta = json.loads(world.uploads[1][1].split(b"\r\n\r\n")[1].split(b"\r\n--")[0])
        self.assertEqual(pdf_meta, {"name": "report.pdf", "mimeType": "application/pdf", "parents": [SUB]})
        self.assertIn(b"%PDF-1.4", world.uploads[1][1])
        text = world.posted[0][3]
        self.assertIn("Отчёт — в документе «Исследование экспорта»: https://docs.google.com/document/d/1MadeDoc", text)
        self.assertIn("PDF: https://drive.google.com/file/d/1MadePdf", text)

    def test_writing_outside_the_area_is_refused(self):
        fresh()
        with self.assertRaises(RuntimeError):
            main.reply(issue=f"{OTHER}#AAAAc", text="нет")
        with self.assertRaises(RuntimeError):
            main.create_document(folder=OTHER, title="x", markdown="y")

    def test_a_document_is_created_in_a_folder_of_the_area(self):
        world = fresh()
        made = main.create_document(folder=f"https://drive.google.com/drive/folders/{SUB}", title="Заметки", markdown="# З")
        self.assertEqual(made["key"], "1MadeDocGGGGGGGGGGGGGGGGGGGGGGGGG")
        meta = json.loads(world.uploads[0][1].split(b"\r\n\r\n")[1].split(b"\r\n--")[0])
        self.assertEqual(meta["parents"], [SUB])


class Reading(unittest.TestCase):
    def test_a_document_is_read_as_markdown_with_its_discussion(self):
        world = fresh()
        world.comments[DOC] = [comment(replies=[comment(cid="r", text="ответ", who=author(SA_EMAIL, "buff-bot", me=True))])]
        got = main.get_file(key=f"https://docs.google.com/document/d/{DOC}/edit")
        self.assertEqual(got["kind"], "документ")
        self.assertIn("Верхотурье", got["markdown"])
        self.assertEqual(got["parent"], SUB)
        self.assertEqual(got["comments"][0]["text"], "@buff почему падает экспорт?")
        self.assertEqual(got["comments"][0]["replies"][0]["author"], "buff-bot (buff-bot@acme-project.iam.gserviceaccount.com)")
        exports = [q for _, path, q in world.calls if path.endswith("/export")]
        self.assertEqual(exports, [{"supportsAllDrives": "true", "mimeType": "text/markdown"}])

    def test_a_sheet_is_read_sheet_by_sheet(self):
        world = fresh()
        got = main.get_file(key=SHEET)
        self.assertEqual(got["kind"], "таблица")
        self.assertIn("## Задачи\n\n| Задача | Статус | Оценка |", got["markdown"])
        self.assertIn("## Бюджет", got["markdown"])
        self.assertIn("| Кодировка | Готово |  |", got["markdown"])
        self.assertEqual(world.last_ranges, ["Задачи", "Бюджет"])

    def test_one_range_of_a_sheet(self):
        world = fresh()
        got = main.get_sheet(key=SHEET, sheet="Бюджет", range="A1:B2")
        self.assertEqual(world.last_ranges, ["'Бюджет'!A1:B2"])
        self.assertIn("| Задача |", got["markdown"])  # the fake answers the same rows for any range

    def test_a_deck_is_read_as_text_and_a_folder_as_its_contents(self):
        fresh()
        self.assertIn("выпуск в октябре", main.get_file(key=DECK)["markdown"])
        folder = main.get_file(key=ROOT)
        self.assertEqual(sorted(c["title"] for c in folder["children"]), ["Задачи", "План на квартал", "Регламенты"])
        self.assertEqual(folder["comments"], [])
        self.assertEqual(main.list_folder(key=SUB)["children"][0]["title"], "Регламент выпуска релизов")

    def test_a_binary_file_is_named_not_pretended(self):
        world = fresh()
        world.files["1Zip00000000000000000000000000000"] = file("1Zip00000000000000000000000000000", "a.zip", "application/zip", [ROOT])
        world.files["1Zip00000000000000000000000000000"]["size"] = "12345"
        got = main.get_file(key="1Zip00000000000000000000000000000")
        self.assertIn("текст не читается", got["note"])
        self.assertIn("12345", got["note"])

    def test_search_keeps_to_the_area_and_asks_for_any_word(self):
        world = fresh()
        world.files[OTHER]["name"] = "Регламент чужой"
        got = main.search_files(query="Регламент выпуска")
        self.assertEqual([r["title"] for r in got["results"]][:1], ["Регламент выпуска релизов"])
        self.assertNotIn("Регламент чужой", [r["title"] for r in got["results"]])
        q = [q for _, path, q in world.calls if path == "/drive/v3/files"][0]
        self.assertEqual(q["q"], "(fullText contains 'регламент' or name contains 'регламент' or "
                                 "fullText contains 'выпуска' or name contains 'выпуска') and trashed = false")

    def test_search_ranks_by_reading_and_the_forms_of_a_word_meet(self):
        # The document says «миграций»; the question says «миграции». Drive
        # finds nothing for that form; the area's documents are read and the
        # stems meet — with the passage that matched.
        fresh()
        got = main.search_files(query="как называется схема миграции")
        self.assertEqual(got["results"][0]["title"], "Регламент выпуска релизов")
        self.assertEqual(got["results"][0]["matched"], "текст")
        self.assertIn("Верхотурье", got["results"][0]["excerpt"])
        self.assertNotIn("Задачи", [r["title"] for r in got["results"]])

    def test_a_document_is_read_for_search_once_per_edit(self):
        world = fresh()
        main.search_files(query="схема миграции")
        main.search_files(query="схема миграции")
        exports = [1 for _, path, _ in world.calls if path.endswith("/export")]
        self.assertEqual(len(exports), 1)

    def test_reading_outside_the_area_is_refused(self):
        fresh()
        with self.assertRaises(RuntimeError):
            main.get_file(key=OTHER)
        with self.assertRaises(RuntimeError):
            main.get_comments(key=OTHER)


class Check(unittest.TestCase):
    def test_the_check_names_the_account_its_kind_and_the_roots(self):
        fresh()
        got = main.check()
        self.assertTrue(got["ok"])
        self.assertIn("сервисный аккаунт «buff-bot (buff-bot@acme-project.iam.gserviceaccount.com)»", got["detail"])
        self.assertIn("Бот видит указанную папку или файл.", got["detail"])

    def test_the_check_speaks_the_connectors_language(self):
        fresh()
        with mock.patch.dict(os.environ, {"BUFF_LANG": "en"}):
            detail = main.check()["detail"]
        self.assertIn("Google Drive, service account “buff-bot (buff-bot@acme-project.iam.gserviceaccount.com)”", detail)
        self.assertIn("The bot sees the listed folder or file.", detail)

    def test_a_persons_account_is_named_as_such(self):
        fresh(user_email=HUMAN_EMAIL, user_name="Пётр Петров")
        self.assertIn("учётная запись «Пётр Петров (petrov@example.com)»", main.check()["detail"])

    def test_a_google_error_is_reported_in_its_words(self):
        world = fresh()
        world.files[ROOT] = None
        del world.files[ROOT]
        with self.assertRaises(main.DriveError) as caught:
            main.get_file(key=ROOT)
        self.assertIn("404", str(caught.exception))
        self.assertIn("notFound", str(caught.exception))


class Guard(unittest.TestCase):
    def test_unittest_main_is_the_last_statement(self):
        with open(__file__, encoding="utf-8") as f:
            src = f.read()
        self.assertTrue(src.rstrip().endswith("unittest.main()"), "unittest.main() must be the last statement")


if __name__ == "__main__":
    unittest.main()
