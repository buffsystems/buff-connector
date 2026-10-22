"""Tests for turning a wiki page into text a model can read.

The fixtures are real: they were taken from a live public Confluence
(confluence.atlassian.com, Data Center 9.2) — its storage format is XHTML with
Confluence's own `ac:`/`ri:` elements mixed in, and that mixture is the whole
difficulty.
"""

import os
import sys
import unittest

HERE = os.path.dirname(__file__)
sys.path.insert(0, os.path.join(HERE, ".."))

from buff_plugin import html_to_text  # noqa: E402


class Paragraphs(unittest.TestCase):
    def test_paragraphs_are_separated_by_blank_lines(self):
        self.assertEqual(
            html_to_text("<p>Первый.</p><p>Второй.</p>"),
            "Первый.\n\nВторой.",
        )

    def test_line_breaks_are_kept(self):
        self.assertEqual(html_to_text("<p>Раз<br/>Два</p>"), "Раз\nДва")

    def test_entities_are_decoded(self):
        self.assertEqual(html_to_text("<p>&lt;property&gt; &amp; &quot;x&quot;</p>"),
                         '<property> & "x"')


class Structure(unittest.TestCase):
    def test_headings_keep_their_level(self):
        self.assertEqual(
            html_to_text("<h1>Раздел</h1><p>Текст</p><h2>Подраздел</h2>"),
            "# Раздел\n\nТекст\n\n## Подраздел",
        )

    def test_lists_become_lines(self):
        self.assertEqual(
            html_to_text("<ul><li>раз</li><li>два</li></ul>"),
            "- раз\n- два",
        )

    def test_numbered_lists_are_numbered(self):
        self.assertEqual(
            html_to_text("<ol><li>раз</li><li>два</li></ol>"),
            "1. раз\n2. два",
        )

    def test_a_table_keeps_its_rows_and_columns(self):
        html = ("<table><tbody>"
                "<tr><th>Поле</th><th>Значение</th></tr>"
                "<tr><td>Таймаут</td><td>30 с</td></tr>"
                "</tbody></table>")
        self.assertEqual(html_to_text(html), "Поле | Значение\nТаймаут | 30 с")

    def test_a_quote_is_marked(self):
        self.assertEqual(
            html_to_text("<blockquote><p>цитата</p></blockquote>"),
            "> цитата",
        )


class Links(unittest.TestCase):
    def test_a_link_keeps_its_address(self):
        self.assertEqual(
            html_to_text('<p>см. <a href="https://x.example/doc">документ</a></p>'),
            "см. документ (https://x.example/doc)",
        )

    def test_a_link_whose_text_is_the_address_is_not_doubled(self):
        self.assertEqual(
            html_to_text('<p><a href="https://x.example">https://x.example</a></p>'),
            "https://x.example",
        )


class ConfluenceMacros(unittest.TestCase):
    """Storage format is XHTML with Confluence's own elements inside it. A macro
    body is content a person wrote and must survive; the markup around it is
    plumbing and must not."""

    def test_a_code_macro_keeps_its_code(self):
        html = ('<ac:structured-macro ac:name="code">'
                '<ac:parameter ac:name="language">bash</ac:parameter>'
                '<ac:plain-text-body><![CDATA[systemctl restart app]]></ac:plain-text-body>'
                '</ac:structured-macro>')
        self.assertEqual(html_to_text(html), "```\nsystemctl restart app\n```")

    def test_a_rich_macro_keeps_its_text(self):
        html = ('<ac:structured-macro ac:name="info"><ac:rich-text-body>'
                '<p>Важно: перезапуск обязателен.</p>'
                '</ac:rich-text-body></ac:structured-macro>')
        self.assertEqual(html_to_text(html), "Важно: перезапуск обязателен.")

    def test_layout_wrappers_add_nothing(self):
        html = ("<ac:layout><ac:layout-section ac:type=\"single\"><ac:layout-cell>"
                "<p>Текст страницы.</p>"
                "</ac:layout-cell></ac:layout-section></ac:layout>")
        self.assertEqual(html_to_text(html), "Текст страницы.")

    def test_a_link_to_another_page_names_it(self):
        html = ('<p>см. <ac:link><ri:page ri:content-title="Настройка базы"/>'
                '</ac:link></p>')
        self.assertEqual(html_to_text(html), "см. «Настройка базы»")

    def test_an_image_is_named_not_dropped(self):
        html = '<p><ac:image><ri:attachment ri:filename="schema.png"/></ac:image></p>'
        self.assertEqual(html_to_text(html), "[изображение: schema.png]")


class Tables(unittest.TestCase):
    """A live page keeps each cell in a paragraph of its own. Read naively, one
    row becomes five lines with stray pipes between them — a table that says
    nothing."""

    def test_a_cell_with_a_paragraph_inside_stays_on_its_row(self):
        html = ("<table><tbody><tr>"
                "<td><p>5 августа</p></td>"
                "<td><p>11.0.0-m221</p></td>"
                "</tr></tbody></table>")
        self.assertEqual(html_to_text(html), "5 августа | 11.0.0-m221")

    def test_an_empty_list_item_is_not_a_line(self):
        # Confluence macros (a table of contents, for instance) render as list
        # items with nothing in them.
        self.assertEqual(html_to_text("<ul><li></li><li>раз</li></ul>"), "- раз")


class RealPage(unittest.TestCase):
    """A fragment taken verbatim from the live server."""

    def test_reads_like_the_page(self):
        html = ('<ac:layout><ac:layout-section ac:type="single"><ac:layout-cell>'
                '<p>This documentation is intended for Confluence developers who want to '
                'ensure that their existing plugins and apps are compatible with '
                '<strong>Confluence 11.0</strong>.</p>'
                '<p>Watch this page to find out when a new milestone is available</p>'
                '</ac:layout-cell></ac:layout-section></ac:layout>')
        text = html_to_text(html)
        self.assertIn("compatible with Confluence 11.0.", text)
        self.assertNotIn("<", text)
        self.assertEqual(len(text.splitlines()), 3)  # два абзаца через пустую строку


class Nothing(unittest.TestCase):
    def test_empty_input_is_empty_output(self):
        self.assertEqual(html_to_text(""), "")
        self.assertEqual(html_to_text(None), "")

    def test_script_and_style_are_dropped(self):
        self.assertEqual(html_to_text("<p>да</p><script>alert(1)</script>"), "да")


if __name__ == "__main__":
    unittest.main()
