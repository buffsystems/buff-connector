"""Office 97–2003 formats are OLE compound files with a binary stream each.
The fixtures were written by LibreOffice (tests/fixtures/src → make-fixtures.sh),
so what is checked is the real layout: a piece table in Word, BIFF8 records
in Excel, text atoms in PowerPoint — and the facts a person would read."""

import os
import sys
import unittest

HERE = os.path.dirname(__file__)
sys.path.insert(0, os.path.join(HERE, ".."))

import doc  # noqa: E402
import ole  # noqa: E402
import ppt  # noqa: E402
import xls  # noqa: E402


def fixture(name: str) -> bytes:
    with open(os.path.join(HERE, "fixtures", name), "rb") as f:
        return f.read()


class OleTests(unittest.TestCase):
    def test_streams_are_listed_and_read_whole(self):
        c = ole.Compound(fixture("instruction.doc"))
        self.assertIn("WordDocument", c.names())
        self.assertIn("1Table", c.names())
        word = c.stream("WordDocument")
        self.assertEqual(word[:2], b"\xec\xa5", "a Word stream starts with the FIB magic")
        self.assertEqual(len(word), 4655)
        self.assertEqual(len(c.stream("\x01Ole")), 20, "a small stream lives in the mini stream")

    def test_not_a_compound_file_is_refused_by_name(self):
        with self.assertRaises(ValueError):
            ole.Compound(b"%PDF-1.4 not an ole file")
        with self.assertRaises(KeyError):
            ole.Compound(fixture("instruction.doc")).stream("Workbook")


class DocTests(unittest.TestCase):
    def test_word_97_text_with_paragraphs_list_and_table(self):
        text = doc.text_of(fixture("instruction.doc"))
        self.assertIn("Инструкция по архиву 2019 года", text)
        self.assertIn("Пароль от архива 2019 года — ЛАСТОЧКА.", text)
        self.assertIn("Скопировать архив\n", text)
        self.assertIn("Год | Размер\n2019 | 14 ГБ\n", text)
        self.assertNotIn("\x07", text)
        self.assertNotIn("\r", text)


class XlsTests(unittest.TestCase):
    def test_excel_97_sheets_in_order_with_numbers_and_dates(self):
        text = xls.text_of(fixture("limits-2019.xls"))
        self.assertEqual(text.splitlines()[:6], [
            "## Лимиты 2019", "", "| Система | Лимит |", "| --- | --- |", "| SAP | 120 |", "| 1С | 45.5 |",
        ])
        self.assertIn("## Ответственные", text)
        self.assertIn("| Воронцов | SAP |", text)

    def test_excel_97_dates_read_as_dates(self):
        text = xls.text_of(fixture("budget-2019.xls"))
        self.assertIn("| Хостинг | 1250.5 | 2026-10-01 |", text)


class PptTests(unittest.TestCase):
    def test_powerpoint_97_slides_with_text_and_notes(self):
        text = ppt.text_of(fixture("kickoff-2018.ppt"))
        self.assertIn("## Слайд 1", text)
        self.assertIn("Старт проекта 2018", text)
        self.assertIn("Спонсор проекта — Воронцов", text)
        self.assertIn("Бюджет утверждён\n\nЗаметки: Поблагодарить спонсора\n\n## Слайд 2", text,
                      "notes belong to their slide, and the notes master is not a slide's notes")
        self.assertNotIn("* * *", text)
        self.assertLess(text.index("Старт проекта 2018"), text.index("Сроки поставки"))


if __name__ == "__main__":
    unittest.main()
