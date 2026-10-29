"""Which reader a file gets, decided by its bytes and its name, and what the
real fixtures (LibreOffice output) read as."""

import os
import sys
import unittest

HERE = os.path.dirname(__file__)
sys.path.insert(0, os.path.join(HERE, "..", "..", "..", "sdk"))
sys.path.insert(0, os.path.join(HERE, ".."))

import extract  # noqa: E402


def fixture(name: str) -> bytes:
    with open(os.path.join(HERE, "fixtures", name), "rb") as f:
        return f.read()


class FixtureTests(unittest.TestCase):
    FACTS = {
        "regulations.docx": "Кодовое слово релиза — ПАЛЬМИРА",
        "budget.xlsx": "| Jira | 750 |",
        "roadmap.pptx": "Кодовое имя Q4 — ЛАВИНА",
        "contract.pdf": "Номер договора 77-АБ/2026",
        "instruction.doc": "Пароль от архива 2019 года — ЛАСТОЧКА",
        "limits-2019.xls": "| SAP | 120 |",
        "kickoff-2018.ppt": "Спонсор проекта — Воронцов",
    }

    def test_every_format_gives_up_its_fact(self):
        for name, fact in self.FACTS.items():
            with self.subTest(name):
                self.assertIn(fact, extract.text_of(name, fixture(name)))

    def test_kind_is_named_by_extension(self):
        self.assertEqual(extract.kind_of("a.docx"), "документ Word")
        self.assertEqual(extract.kind_of("a.XLS"), "таблица Excel 97–2003")
        self.assertEqual(extract.kind_of("a.pdf"), "PDF")
        self.assertEqual(extract.kind_of("notes.md"), "текст")
        self.assertEqual(extract.kind_of("photo.jpg"), "jpg")


class SniffTests(unittest.TestCase):
    def test_a_renamed_office_file_is_still_read(self):
        self.assertIn("ПАЛЬМИРА", extract.text_of("weird.bin", fixture("regulations.docx")))
        self.assertIn("ЛАСТОЧКА", extract.text_of("archive.dat", fixture("instruction.doc")))
        self.assertIn("77-АБ/2026", extract.text_of("scan", fixture("contract.pdf")))

    def test_text_encodings(self):
        self.assertEqual(extract.text_of("a.txt", "привет".encode("utf-8")), "привет")
        self.assertEqual(extract.text_of("a.txt", "﻿привет".encode("utf-8")), "привет")
        self.assertEqual(extract.text_of("a.txt", "привет".encode("cp1251")), "привет")
        self.assertEqual(extract.text_of("a.txt", "привет".encode("utf-16")), "привет")
        self.assertEqual(extract.text_of("noext", b"plain ascii\n"), "plain ascii\n")

    def test_binary_and_broken_files_are_named_not_read(self):
        with self.assertRaises(extract.Unreadable) as ctx:
            extract.text_of("photo.png", b"\x89PNG\r\n\x1a\n" + bytes(range(256)))
        self.assertIn("двоичный файл .png, 264 байт", str(ctx.exception))
        with self.assertRaises(extract.Unreadable) as ctx:
            extract.text_of("broken.docx", b"PK\x03\x04garbage")
        self.assertIn("документ Word", str(ctx.exception))
        with self.assertRaises(extract.Unreadable) as ctx:
            extract.text_of("plain.zip", fixture("regulations.docx")[:0] + _zip_without_office())
        self.assertIn("архив zip", str(ctx.exception))


def _zip_without_office() -> bytes:
    import io
    import zipfile
    buf = io.BytesIO()
    with zipfile.ZipFile(buf, "w") as z:
        z.writestr("readme.txt", "hi")
    return buf.getvalue()


if __name__ == "__main__":
    unittest.main()
