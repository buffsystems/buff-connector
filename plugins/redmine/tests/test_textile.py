"""Markdown → Textile for a Redmine that formats text with Textile (cloud-front#478).

The answer comes from the model as Markdown. On a Redmine set to Textile it showed
as raw stars and brackets, and on one set to Markdown the link we wrote in Textile
showed as a raw address. The connection says which one the server uses; on a Textile
server the answer is turned into Textile before it is posted.
"""

import os
import sys
import unittest

HERE = os.path.dirname(__file__)
sys.path.insert(0, os.path.join(HERE, ".."))

from textile import to_textile  # noqa: E402


class TestToTextile(unittest.TestCase):
    def test_emphasis_code_and_links(self):
        self.assertEqual(
            to_textile("**Python** и *немного* `catalog.py`, см. [README](https://git.corp.local/r)"),
            '*Python* и _немного_ @catalog.py@, см. "README":https://git.corp.local/r')

    def test_headings_and_lists(self):
        md = "## Итог\n\n- первый\n- второй\n  - вложенный\n\n1. шаг\n2. ещё шаг"
        self.assertEqual(to_textile(md), "h2. Итог\n\n* первый\n* второй\n** вложенный\n\n# шаг\n# ещё шаг")

    def test_a_code_block_is_kept_verbatim(self):
        md = "Пример:\n\n```python\nprint(**kw)  # *не курсив*\n```"
        self.assertEqual(to_textile(md), 'Пример:\n\n<pre><code class="python">\nprint(**kw)  # *не курсив*\n</code></pre>')

    def test_inline_code_is_not_formatted_inside(self):
        self.assertEqual(to_textile("`a*b*c` и **жирный**"), "@a*b*c@ и *жирный*")

    def test_a_table(self):
        md = "| Файл | Строк |\n|---|---:|\n| catalog.py | 16 |"
        self.assertEqual(to_textile(md), "|_. Файл |_. Строк |\n| catalog.py | 16 |")

    def test_plain_text_is_left_alone(self):
        self.assertEqual(to_textile("Принял — разбираюсь и отвечу здесь."), "Принял — разбираюсь и отвечу здесь.")


if __name__ == "__main__":
    unittest.main()
