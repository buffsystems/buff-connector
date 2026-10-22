"""The three tools, driven the way the model calls them, over a folder of the
real fixtures: what glob, grep and read say — opencode's contract, with the
files of this plugin behind it."""

import json
import os
import shutil
import sys
import tempfile
import time
import unittest

HERE = os.path.dirname(__file__)
sys.path.insert(0, os.path.join(HERE, "..", "..", "..", "sdk"))
sys.path.insert(0, os.path.join(HERE, ".."))

FIX = os.path.join(HERE, "fixtures")


class ToolTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.mkdtemp(prefix="files-plugin-")
        docs = os.path.join(cls.tmp, "docs")
        os.makedirs(os.path.join(docs, "2026", "contracts"))
        os.makedirs(os.path.join(docs, "old"))
        os.makedirs(os.path.join(cls.tmp, "outside"))
        shutil.copy(os.path.join(FIX, "regulations.docx"), os.path.join(docs, "regulations.docx"))
        shutil.copy(os.path.join(FIX, "budget.xlsx"), os.path.join(docs, "2026", "budget.xlsx"))
        shutil.copy(os.path.join(FIX, "contract.pdf"), os.path.join(docs, "2026", "contracts", "contract.pdf"))
        shutil.copy(os.path.join(FIX, "roadmap.pptx"), os.path.join(docs, "2026", "roadmap.pptx"))
        shutil.copy(os.path.join(FIX, "instruction.doc"), os.path.join(docs, "old", "instruction.doc"))
        shutil.copy(os.path.join(FIX, "limits-2019.xls"), os.path.join(docs, "old", "limits-2019.xls"))
        shutil.copy(os.path.join(FIX, "kickoff-2018.ppt"), os.path.join(docs, "old", "kickoff-2018.ppt"))
        with open(os.path.join(docs, "readme.md"), "w") as f:
            f.write("# Папка документов\n\nЗдесь регламенты и договоры.\nПальмира — не здесь, это в регламенте.\n")
        with open(os.path.join(docs, "notes.bak"), "w") as f:
            f.write("ПАЛЬМИРА в бэкапе\n")
        with open(os.path.join(cls.tmp, "outside", "secret.txt"), "w") as f:
            f.write("ПАЛЬМИРА снаружи\n")
        with open(os.path.join(docs, "photo.png"), "wb") as f:
            f.write(b"\x89PNG\r\n\x1a\n" + bytes(range(256)))
        # A known order for the mtime sort: the newest file first.
        now = time.time()
        os.utime(os.path.join(docs, "regulations.docx"), (now - 300, now - 300))
        os.utime(os.path.join(docs, "readme.md"), (now + 60, now + 60))
        os.environ["BUFF_CONFIG"] = json.dumps({"roots": [docs], "excludes": ["*.bak"]})
        import importlib
        import main
        cls.main = importlib.reload(main)
        cls.docs = docs

    @classmethod
    def tearDownClass(cls):
        shutil.rmtree(cls.tmp, ignore_errors=True)

    # ---- glob -------------------------------------------------------------

    def test_glob_finds_by_name_at_any_depth_newest_first(self):
        out = self.main.glob(pattern="*.md")
        self.assertEqual(out, f"{self.docs}/readme.md")
        out = self.main.glob(pattern="*.xls*").splitlines()
        self.assertEqual(sorted(out), [f"{self.docs}/2026/budget.xlsx", f"{self.docs}/old/limits-2019.xls"])
        out = self.main.glob(pattern="**/*.pdf", path=self.docs + "/2026").splitlines()
        self.assertEqual(out, [f"{self.docs}/2026/contracts/contract.pdf"])
        self.assertEqual(self.main.glob(pattern="*.zzz"), "Файлы не найдены")
        everything = self.main.glob(pattern="*").splitlines()
        self.assertEqual(everything[0], f"{self.docs}/readme.md", "sorted by modification time, newest first")
        self.assertNotIn(f"{self.docs}/notes.bak", everything, "an excluded file is invisible")

    def test_a_root_may_name_one_file(self):
        import importlib
        import main
        saved = os.environ["BUFF_CONFIG"]
        os.environ["BUFF_CONFIG"] = json.dumps({"roots": [self.docs + "/regulations.docx", self.docs + "/old"]})
        try:
            m = importlib.reload(main)
            self.assertEqual(m.glob(pattern="*.docx"), f"{self.docs}/regulations.docx")
            self.assertIn("ПАЛЬМИРА", m.grep(pattern="пальмира"))
            self.assertIn("ЛАСТОЧКА", m.grep(pattern="ласточка"))
            with self.assertRaises(ValueError):
                m.read(filePath=self.docs + "/readme.md")
        finally:
            os.environ["BUFF_CONFIG"] = saved
            importlib.reload(main)

    def test_glob_outside_the_area_is_refused(self):
        with self.assertRaises(ValueError) as ctx:
            self.main.glob(pattern="*", path=self.tmp + "/outside")
        self.assertIn("вне области", str(ctx.exception))

    # ---- grep -------------------------------------------------------------

    def test_grep_searches_the_text_of_office_and_pdf_files(self):
        out = self.main.grep(pattern="пальмира")
        self.assertTrue(out.startswith("Найдено 2 совпадения"), out)
        self.assertIn(f"{self.docs}/regulations.docx:\n  Строка 3: Документ описывает порядок выпуска. Кодовое слово релиза — ПАЛЬМИРА.", out)
        self.assertIn(f"{self.docs}/readme.md:\n  Строка 4: Пальмира — не здесь", out)
        self.assertNotIn("notes.bak", out)
        self.assertNotIn("secret.txt", out)
        out = self.main.grep(pattern=r"77-АБ/\d{4}", include="*.pdf")
        self.assertIn("contract.pdf:\n  Строка 4: Номер договора 77-АБ/2026", out)
        self.assertIn("ЛАСТОЧКА", self.main.grep(pattern="ласточка", path=self.docs + "/old"))
        self.assertIn("| SAP | 120 |", self.main.grep(pattern="SAP"))
        self.assertIn("Воронцов", self.main.grep(pattern="спонсор", include="*.ppt"))
        self.assertEqual(self.main.grep(pattern="нетакогослова"), "Ничего не найдено")

    def test_grep_needs_a_pattern_and_a_valid_regex(self):
        with self.assertRaises(ValueError):
            self.main.grep(pattern="")
        with self.assertRaises(ValueError) as ctx:
            self.main.grep(pattern="(unclosed")
        self.assertIn("регулярное выражение", str(ctx.exception))

    # ---- read -------------------------------------------------------------

    def test_read_gives_numbered_lines_of_the_text_a_person_reads(self):
        out = self.main.read(filePath=self.docs + "/regulations.docx")
        self.assertTrue(out.startswith(f"<path>{self.docs}/regulations.docx</path>\n<type>file</type>\n<kind>документ Word</kind>\n<content>\n1: # Регламент выпуска релизов\n2: \n3: "), out)
        self.assertIn("| Пятница | Кунгур |", out)
        self.assertRegex(out, r"\(Конец файла — всего \d+ строк\)\n</content>$")
        out = self.main.read(filePath=self.docs + "/old/kickoff-2018.ppt")
        self.assertIn("<kind>презентация PowerPoint 97–2003</kind>", out)
        self.assertIn("Спонсор проекта — Воронцов", out)

    def test_read_pages_with_offset_and_limit_only_when_asked(self):
        out = self.main.read(filePath=self.docs + "/readme.md", offset=3, limit=1)
        self.assertIn("<content>\n3: Здесь регламенты и договоры.\n\n(Показаны строки 3–3 из 4. Продолжить с offset=4)\n</content>", out)
        out = self.main.read(filePath=self.docs + "/readme.md", offset=4)
        self.assertIn("4: Пальмира — не здесь, это в регламенте.\n\n(Конец файла — всего 4 строк)", out)
        with self.assertRaises(ValueError):
            self.main.read(filePath=self.docs + "/readme.md", offset=9)

    def test_read_a_folder_lists_its_entries(self):
        out = self.main.read(filePath=self.docs)
        self.assertIn("<type>directory</type>", out)
        self.assertIn("2026/\nold/\nphoto.png\nreadme.md\nregulations.docx\n", out)
        self.assertNotIn("notes.bak", out)
        self.assertIn("(5 записей)", out)

    def test_read_names_a_binary_file_instead_of_reading_it(self):
        out = self.main.read(filePath=self.docs + "/photo.png")
        self.assertIn("<type>binary</type>", out)
        self.assertIn("двоичный файл .png, 264 байт", out)

    def test_read_missing_file_suggests_neighbours(self):
        with self.assertRaises(ValueError) as ctx:
            self.main.read(filePath=self.docs + "/regulation.docx")
        self.assertIn("Файл не найден", str(ctx.exception))
        self.assertIn("regulations.docx", str(ctx.exception))
        with self.assertRaises(ValueError) as ctx:
            self.main.read(filePath=self.tmp + "/outside/secret.txt")
        self.assertIn("вне области", str(ctx.exception))
        with self.assertRaises(ValueError):
            self.main.read(filePath="relative.txt")

    # ---- check and descriptions -------------------------------------------

    def test_check_counts_reachable_roots(self):
        res = self.main.check()
        self.assertTrue(res["ok"])
        self.assertIn("Бот видит указанную папку.", res["detail"])

    def test_tool_descriptions_name_the_roots(self):
        for name in ("glob", "grep", "read"):
            self.assertIn(self.docs, self.main.plugin._tools[name]["description"], name)
            self.assertTrue(self.main.plugin._tools[name]["read_only"])


if __name__ == "__main__":
    unittest.main()
