"""Russian number agreement for what a plugin says to a person (cloud-front#478).

«из 1 проектов», «корней в области: 1» — a check that reads like a form. The words
agree with the number, the way a person would say it.
"""

import os
import sys
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from buff_plugin import english, plural, plural_en, say, visible  # noqa: E402
from unittest import mock  # noqa: E402


class TestPlural(unittest.TestCase):
    def test_the_three_forms(self):
        forms = ("проект", "проекта", "проектов")
        self.assertEqual([plural(n, *forms) for n in (1, 2, 5, 11, 12, 21, 22, 25, 111, 0)],
                         ["проект", "проекта", "проектов", "проектов", "проектов",
                          "проект", "проекта", "проектов", "проектов", "проектов"])

    def test_a_case_that_has_two_forms_takes_the_same_rule(self):
        where = ("проекте", "проектах", "проектах")
        self.assertEqual(f"в 1 {plural(1, *where)}", "в 1 проекте")
        self.assertEqual(f"в 3 {plural(3, *where)}", "в 3 проектах")


class TestVisible(unittest.TestCase):
    """«корней в области: 3, из них доступно: 2» — said as a person would say it."""

    WORDS = dict(one="указанную папку", many="указанные папки", many_of="указанных папок",
                 fix="Откройте их учётной записи бота.")

    def test_every_case_reads_as_a_sentence(self):
        self.assertEqual(visible(1, 1, **self.WORDS), "Бот видит указанную папку.")
        self.assertEqual(visible(0, 1, **self.WORDS), "Бот не видит указанную папку. Откройте их учётной записи бота.")
        self.assertEqual(visible(3, 3, **self.WORDS), "Бот видит все указанные папки (3).")
        self.assertEqual(visible(2, 3, **self.WORDS), "Бот видит 2 из 3 указанных папок. Откройте их учётной записи бота.")
        self.assertEqual(visible(0, 3, **self.WORDS), "Бот не видит ни одной из указанных папок (3). Откройте их учётной записи бота.")


class TestEnglish(unittest.TestCase):
    """What a plugin says to a person follows the connector's language (#565): the
    connector passes it as BUFF_LANG; anything but English is Russian."""

    EN = dict(one="the listed folder", many="listed folders", fix="Give the bot's account access.")

    def test_the_language_comes_from_the_connector(self):
        for value, want in (("en", True), ("EN", True), ("en_GB.UTF-8", True), ("ru", False), ("", False), ("de", False)):
            with mock.patch.dict(os.environ, {"BUFF_LANG": value}):
                self.assertEqual(english(), want, value)
        with mock.patch.dict(os.environ, {}, clear=True):
            self.assertFalse(english())

    def test_say_picks_the_line(self):
        with mock.patch.dict(os.environ, {"BUFF_LANG": "en"}):
            self.assertEqual(say("работает", "works"), "works")
        with mock.patch.dict(os.environ, {"BUFF_LANG": "ru"}):
            self.assertEqual(say("работает", "works"), "работает")

    def test_english_counts_two_ways(self):
        self.assertEqual([plural_en(n, "project", "projects") for n in (0, 1, 2, 21)],
                         ["projects", "project", "projects", "projects"])

    def test_visible_in_english(self):
        words = dict(TestVisible.WORDS, en=self.EN)
        with mock.patch.dict(os.environ, {"BUFF_LANG": "en"}):
            self.assertEqual(visible(1, 1, **words), "The bot sees the listed folder.")
            self.assertEqual(visible(0, 1, **words), "The bot does not see the listed folder. Give the bot's account access.")
            self.assertEqual(visible(3, 3, **words), "The bot sees all the listed folders (3).")
            self.assertEqual(visible(2, 3, **words), "The bot sees 2 of 3 listed folders. Give the bot's account access.")
            self.assertEqual(visible(0, 3, **words), "The bot sees none of the listed folders (3). Give the bot's account access.")
        with mock.patch.dict(os.environ, {"BUFF_LANG": "ru"}):
            self.assertEqual(visible(1, 1, **words), "Бот видит указанную папку.")


if __name__ == "__main__":
    unittest.main()
