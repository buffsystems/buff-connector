"""Russian number agreement for what a plugin says to a person (cloud-front#478).

«из 1 проектов», «корней в области: 1» — a check that reads like a form. The words
agree with the number, the way a person would say it.
"""

import os
import sys
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from buff_plugin import plural, visible  # noqa: E402


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


if __name__ == "__main__":
    unittest.main()
