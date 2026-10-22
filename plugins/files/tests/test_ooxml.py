"""Office 2007+ formats are zip archives of XML; these tests build the smallest
archives that follow ECMA-376 and check what a person would read out of them.

Every fixture is built here rather than captured, so the test says which
structure it proves: shared vs inline strings, a date serial, a heading style,
a list, a table, a text box, a slide's notes."""

import io
import os
import sys
import unittest
import zipfile

HERE = os.path.dirname(__file__)
sys.path.insert(0, os.path.join(HERE, ".."))

import docx  # noqa: E402
import pptx  # noqa: E402
import xlsx  # noqa: E402

W = 'xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"'
R = 'xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"'
A = 'xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"'
P = 'xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"'
S = 'xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"'
PKG = "http://schemas.openxmlformats.org/package/2006/relationships"


def archive(files: dict[str, str]) -> bytes:
    buf = io.BytesIO()
    with zipfile.ZipFile(buf, "w", zipfile.ZIP_DEFLATED) as z:
        for name, body in files.items():
            z.writestr(name, body)
    return buf.getvalue()


def rels(pairs, prefix=""):
    items = "".join(
        f'<Relationship Id="{rid}" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/{typ}" Target="{target}"/>'
        for rid, typ, target in pairs)
    return f'<Relationships xmlns="{PKG}">{items}</Relationships>'


class DocxTests(unittest.TestCase):
    def build(self, body: str) -> bytes:
        return archive({
            "[Content_Types].xml": "<Types/>",
            "word/document.xml": f'<w:document {W} {R}><w:body>{body}</w:body></w:document>',
        })

    def test_headings_lists_runs_and_breaks_read_as_markdown(self):
        body = (
            '<w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t>Регламент</w:t></w:r></w:p>'
            '<w:p><w:r><w:t xml:space="preserve">Кодовое слово: </w:t></w:r><w:r><w:rPr><w:b/></w:rPr><w:t>ПАЛЬМИРА</w:t></w:r></w:p>'
            '<w:p><w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="1"/></w:numPr></w:pPr><w:r><w:t>первый пункт</w:t></w:r></w:p>'
            '<w:p><w:pPr><w:numPr><w:ilvl w:val="1"/><w:numId w:val="1"/></w:numPr></w:pPr><w:r><w:t>вложенный</w:t></w:r></w:p>'
            '<w:p><w:r><w:t>строка</w:t><w:br/><w:t>после разрыва</w:t><w:tab/><w:t>после таба</w:t></w:r></w:p>'
            '<w:p><w:pPr><w:pStyle w:val="Heading2"/></w:pPr><w:r><w:t>Раздел</w:t></w:r></w:p>'
        )
        text = docx.text_of(self.build(body))
        self.assertEqual(text.splitlines(), [
            "# Регламент", "", "Кодовое слово: ПАЛЬМИРА", "", "- первый пункт", "  - вложенный", "",
            "строка", "после разрыва\tпосле таба", "", "## Раздел", "",
        ])

    def test_a_table_becomes_a_markdown_table_and_a_text_box_is_not_lost(self):
        body = (
            '<w:tbl><w:tr><w:tc><w:p><w:r><w:t>Система</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>Лимит</w:t></w:r></w:p></w:tc></w:tr>'
            '<w:tr><w:tc><w:p><w:r><w:t>Jira</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>750 | в минуту</w:t></w:r></w:p><w:p><w:r><w:t>вторая строка</w:t></w:r></w:p></w:tc></w:tr></w:tbl>'
            '<w:p><w:r><w:pict><v:shape xmlns:v="urn:schemas-microsoft-com:vml"><v:textbox><w:txbxContent>'
            '<w:p><w:r><w:t>текст во врезке</w:t></w:r></w:p></w:txbxContent></v:textbox></v:shape></w:pict></w:r></w:p>'
            '<w:sdt><w:sdtContent><w:p><w:r><w:t>внутри поля содержимого</w:t></w:r></w:p></w:sdtContent></w:sdt>'
        )
        text = docx.text_of(self.build(body))
        self.assertEqual(text.splitlines(), [
            "| Система | Лимит |", "| --- | --- |", "| Jira | 750 \\| в минуту вторая строка |", "",
            "текст во врезке", "",
            "внутри поля содержимого", "",
        ])

    def test_a_hyperlink_keeps_its_address(self):
        body = ('<w:p><w:hyperlink r:id="rId5"><w:r><w:t>регламент</w:t></w:r></w:hyperlink></w:p>')
        data = archive({
            "word/document.xml": f'<w:document {W} {R}><w:body>{body}</w:body></w:document>',
            "word/_rels/document.xml.rels": rels([("rId5", "hyperlink", "https://wiki.example.com/rel")]),
        })
        self.assertEqual(docx.text_of(data).strip(), "[регламент](https://wiki.example.com/rel)")


class XlsxTests(unittest.TestCase):
    def build(self, sheets: dict[str, str], shared=(), styles="") -> bytes:
        names = list(sheets)
        wb = (f'<workbook {S} {R}><sheets>' + "".join(
            f'<sheet name="{n}" sheetId="{i+1}" r:id="rId{i+1}"/>' for i, n in enumerate(names)) + "</sheets></workbook>")
        files = {
            "xl/workbook.xml": wb,
            "xl/_rels/workbook.xml.rels": rels(
                [(f"rId{i+1}", "worksheet", f"worksheets/sheet{i+1}.xml") for i in range(len(names))]
                + [("rId9", "sharedStrings", "sharedStrings.xml"), ("rId8", "styles", "styles.xml")]),
            "xl/sharedStrings.xml": f'<sst {S}>' + "".join(f"<si>{s}</si>" for s in shared) + "</sst>",
            "xl/styles.xml": styles or f'<styleSheet {S}><cellXfs><xf numFmtId="0"/></cellXfs></styleSheet>',
        }
        for i, n in enumerate(names):
            files[f"xl/worksheets/sheet{i+1}.xml"] = f'<worksheet {S}><sheetData>{sheets[n]}</sheetData></worksheet>'
        return archive(files)

    def test_sheets_come_out_as_markdown_tables_in_workbook_order(self):
        data = self.build({
            "Бюджет": ('<row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c></row>'
                       '<row r="2"><c r="A2" t="s"><v>2</v></c><c r="B2"><v>1250.5</v></c></row>'
                       '<row r="3"><c r="A3" t="inlineStr"><is><t>Итого</t></is></c><c r="B3"><f>SUM(B2)</f><v>1250.5</v></c></row>'),
            "Лимиты": ('<row r="1"><c r="A1" t="str"><v>Jira</v></c><c r="C1"><v>750</v></c><c r="D1" t="b"><v>1</v></c></row>'),
        }, shared=["<t>Статья</t>", "<t>Сумма</t>", "<r><t>Хос</t></r><r><t>тинг</t></r>"])
        self.assertEqual(xlsx.text_of(data).splitlines(), [
            "## Бюджет", "", "| Статья | Сумма |", "| --- | --- |", "| Хостинг | 1250.5 |", "| Итого | 1250.5 |", "",
            "## Лимиты", "", "| Jira |  | 750 | TRUE |", "| --- | --- | --- | --- |", "",
        ])

    def test_a_date_serial_is_shown_as_a_date_and_a_plain_number_stays_a_number(self):
        styles = (f'<styleSheet {S}><numFmts><numFmt numFmtId="164" formatCode="dd\\.mm\\.yyyy"/></numFmts>'
                  '<cellXfs><xf numFmtId="0"/><xf numFmtId="14"/><xf numFmtId="164"/><xf numFmtId="22"/></cellXfs></styleSheet>')
        data = self.build({"Сроки": (
            '<row r="1"><c r="A1" s="1"><v>46000</v></c><c r="B1" s="2"><v>46000.75</v></c>'
            '<c r="C1" s="3"><v>46000.75</v></c><c r="D1"><v>46000</v></c><c r="E1"><v>3</v></c></row>')}, styles=styles)
        self.assertIn("| 2025-12-09 | 2025-12-09 | 2025-12-09 18:00 | 46000 | 3 |", xlsx.text_of(data))

    def test_absolute_relationship_targets_resolve_like_openpyxl_writes_them(self):
        # openpyxl writes Target="/xl/worksheets/sheet1.xml" (an absolute part name); a
        # workbook saved that way used to fail with «в архиве нет части /xl/…» (#425).
        data = self.build({"A": '<row r="1"><c r="A1"><v>7</v></c></row>'})
        buf = io.BytesIO()
        with zipfile.ZipFile(io.BytesIO(data)) as zin, zipfile.ZipFile(buf, "w") as zout:
            for item in zin.infolist():
                body = zin.read(item.filename)
                if item.filename == "xl/_rels/workbook.xml.rels":
                    body = body.decode().replace('Target="', 'Target="/xl/').encode()
                zout.writestr(item, body)
        self.assertEqual(xlsx.sheet_names(buf.getvalue()), ["A"])
        self.assertIn("| 7 |", xlsx.text_of(buf.getvalue()))

    def test_sheet_names_are_listed_and_one_sheet_can_be_read_alone(self):
        data = self.build({"A": '<row r="1"><c r="A1"><v>1</v></c></row>', "B": '<row r="1"><c r="A1"><v>2</v></c></row>'})
        self.assertEqual(xlsx.sheet_names(data), ["A", "B"])
        self.assertEqual(xlsx.text_of(data, sheet="B").splitlines(), ["## B", "", "| 2 |", "| --- |", ""])
        with self.assertRaises(KeyError):
            xlsx.text_of(data, sheet="C")


class PptxTests(unittest.TestCase):
    def build(self) -> bytes:
        def sp(text, ph=None):
            ph_tag = f'<p:nvSpPr><p:cNvPr id="1" name="x"/><p:cNvSpPr/><p:nvPr><p:ph type="{ph}"/></p:nvPr></p:nvSpPr>' if ph else ""
            paras = "".join(f'<a:p><a:r><a:t>{t}</a:t></a:r></a:p>' for t in text)
            return f'<p:sp>{ph_tag}<p:txBody>{paras}</p:txBody></p:sp>'
        slide1 = (f'<p:sld {P} {A} {R}><p:cSld><p:spTree>{sp(["Дорожная карта"], "title")}'
                  f'{sp(["Кодовое имя Q4 — ЛАВИНА", "Запуск в ноябре"], "body")}</p:spTree></p:cSld></p:sld>')
        slide2 = (f'<p:sld {P} {A} {R}><p:cSld><p:spTree>{sp(["Команда"], "title")}'
                  '<p:graphicFrame><a:graphic><a:graphicData><a:tbl>'
                  '<a:tr><a:tc><a:txBody><a:p><a:r><a:t>Роль</a:t></a:r></a:p></a:txBody></a:tc><a:tc><a:txBody><a:p><a:r><a:t>Кто</a:t></a:r></a:p></a:txBody></a:tc></a:tr>'
                  '<a:tr><a:tc><a:txBody><a:p><a:r><a:t>Лид</a:t></a:r></a:p></a:txBody></a:tc><a:tc><a:txBody><a:p><a:r><a:t>Кунгур</a:t></a:r></a:p></a:txBody></a:tc></a:tr>'
                  '</a:tbl></a:graphicData></a:graphic></p:graphicFrame></p:spTree></p:cSld></p:sld>')
        notes = (f'<p:notes {P} {A}><p:cSld><p:spTree>{sp(["Сказать про бюджет"], "body")}'
                 f'{sp(["2"], "sldNum")}</p:spTree></p:cSld></p:notes>')
        return archive({
            "ppt/presentation.xml": f'<p:presentation {P} {R}><p:sldIdLst><p:sldId id="256" r:id="rId3"/><p:sldId id="257" r:id="rId2"/></p:sldIdLst></p:presentation>',
            "ppt/_rels/presentation.xml.rels": rels([("rId2", "slide", "slides/slide2.xml"), ("rId3", "slide", "slides/slide1.xml")]),
            "ppt/slides/slide1.xml": slide1,
            "ppt/slides/slide2.xml": slide2,
            "ppt/slides/_rels/slide2.xml.rels": rels([("rId1", "notesSlide", "../notesSlides/notesSlide1.xml")]),
            "ppt/notesSlides/notesSlide1.xml": notes,
        })

    def test_slides_in_order_with_titles_tables_and_notes(self):
        self.assertEqual(pptx.text_of(self.build()).splitlines(), [
            "## Слайд 1. Дорожная карта", "", "Кодовое имя Q4 — ЛАВИНА", "Запуск в ноябре", "",
            "## Слайд 2. Команда", "", "| Роль | Кто |", "| --- | --- |", "| Лид | Кунгур |", "",
            "Заметки: Сказать про бюджет", "",
        ])


if __name__ == "__main__":
    unittest.main()
