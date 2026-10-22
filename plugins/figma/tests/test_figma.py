"""Unit checks for the Figma integration (#299).

Fixtures follow the REST API's own specification (figma/rest-api-spec): the node
traits, the Comment object, the file/nodes/styles/variables envelopes and the
error payloads. No network: the host's buff/http is replaced by a table of
canned answers, keyed by method and path.
"""

import base64
import json
import os
import sys
import unittest
import unittest.mock

HERE = os.path.dirname(__file__)
sys.path.insert(0, os.path.join(HERE, "..", "..", "..", "sdk"))
os.environ["BUFF_CONFIG"] = json.dumps({
    "base_url": "https://api.figma.com",
    "images_base": "https://figma-alpha-api.s3.us-west-2.amazonaws.com",
    "files": ["Kb7Cabinet01"],
    "projects": ["4815162342"],
    "mention_alias": "buff",
})
sys.path.insert(0, os.path.join(HERE, ".."))
import design  # noqa: E402
import main  # noqa: E402

# ---- fixtures, in the API's shapes -----------------------------------------------

BOT = {"id": "1000000000000000001", "handle": "buffbot", "img_url": "https://example/1.png"}
HUMAN = {"id": "1000000000000000002", "handle": "Пётр Петров", "img_url": "https://example/2.png"}

MALACHITE = {"r": 0.0588, "g": 0.6392, "b": 0.498, "a": 1}
INK = {"r": 0.0667, "g": 0.0941, "b": 0.1529, "a": 1}

STYLES = {
    "S:malachite": {"key": "k1", "name": "Кнопка/Малахит", "description": "Основная кнопка", "remote": False, "styleType": "FILL"},
    "S:h1": {"key": "k2", "name": "Заголовок/H1", "description": "", "remote": False, "styleType": "TEXT"},
}
COMPONENTS = {"2:1": {"key": "c1", "name": "Кнопка/Основная", "description": "Главное действие экрана",
                      "documentationLinks": [], "remote": False}}

BUTTON_TEXT = {
    "id": "1:6", "name": "Подпись", "type": "TEXT", "scrollBehavior": "SCROLLS",
    "blendMode": "PASS_THROUGH",
    "absoluteBoundingBox": {"x": 48, "y": 300, "width": 294, "height": 24},
    "absoluteRenderBounds": {"x": 48, "y": 300, "width": 294, "height": 24},
    "fills": [{"blendMode": "NORMAL", "type": "SOLID", "color": {"r": 1, "g": 1, "b": 1, "a": 1}}],
    "strokes": [], "strokeWeight": 1, "strokeAlign": "OUTSIDE", "effects": [],
    "characters": "Войти", "style": {"fontFamily": "Inter", "fontPostScriptName": "Inter-SemiBold", "fontStyle": "Semi Bold",
                                     "fontWeight": 600, "fontSize": 16, "textAlignHorizontal": "CENTER",
                                     "textAlignVertical": "CENTER", "letterSpacing": 0, "lineHeightPx": 24,
                                     "lineHeightPercent": 100, "lineHeightUnit": "PIXELS"},
    "characterStyleOverrides": [], "styleOverrideTable": {}, "lineTypes": ["NONE"], "lineIndentations": [0],
    "layoutAlign": "INHERIT", "layoutGrow": 0, "layoutSizingHorizontal": "HUG", "layoutSizingVertical": "HUG",
}

BUTTON = {
    "id": "1:5", "name": "Кнопка «Войти»", "type": "INSTANCE", "scrollBehavior": "SCROLLS",
    "componentId": "2:1", "overrides": [],
    "componentProperties": {"Состояние": {"type": "VARIANT", "value": "Обычная"},
                            "Текст#12:0": {"type": "TEXT", "value": "Войти"}},
    "blendMode": "PASS_THROUGH", "clipsContent": False,
    "children": [BUTTON_TEXT],
    "absoluteBoundingBox": {"x": 24, "y": 288, "width": 342, "height": 48},
    "absoluteRenderBounds": {"x": 24, "y": 288, "width": 342, "height": 48},
    "layoutMode": "HORIZONTAL", "primaryAxisAlignItems": "CENTER", "counterAxisAlignItems": "CENTER",
    "primaryAxisSizingMode": "FIXED", "counterAxisSizingMode": "FIXED",
    "paddingLeft": 24, "paddingRight": 24, "paddingTop": 12, "paddingBottom": 12, "itemSpacing": 8,
    "layoutSizingHorizontal": "FILL", "layoutSizingVertical": "FIXED",
    "cornerRadius": 12, "rectangleCornerRadii": [12, 12, 12, 12],
    "fills": [{"blendMode": "NORMAL", "type": "SOLID", "color": MALACHITE,
               "boundVariables": {"color": {"type": "VARIABLE_ALIAS", "id": "VariableID:1:1"}}}],
    "strokes": [], "strokeWeight": 1, "strokeAlign": "INSIDE",
    "styles": {"fill": "S:malachite"},
    "effects": [{"type": "DROP_SHADOW", "visible": True, "color": {"r": 0, "g": 0, "b": 0, "a": 0.16},
                 "blendMode": "NORMAL", "offset": {"x": 0, "y": 4}, "radius": 12, "spread": 0, "showShadowBehindNode": False}],
    "boundVariables": {"fills": [{"type": "VARIABLE_ALIAS", "id": "VariableID:1:1"}],
                       "itemSpacing": {"type": "VARIABLE_ALIAS", "id": "VariableID:1:2"}},
}

HEADING = {
    "id": "1:3", "name": "Заголовок", "type": "TEXT", "scrollBehavior": "SCROLLS", "blendMode": "PASS_THROUGH",
    "absoluteBoundingBox": {"x": 24, "y": 120, "width": 342, "height": 32},
    "absoluteRenderBounds": {"x": 24, "y": 120, "width": 342, "height": 32},
    "fills": [{"blendMode": "NORMAL", "type": "SOLID", "color": INK}],
    "strokes": [], "strokeWeight": 1, "strokeAlign": "OUTSIDE", "effects": [],
    "characters": "Войти в кабинет",
    "style": {"fontFamily": "Inter", "fontPostScriptName": None, "fontStyle": "Semi Bold", "fontWeight": 600,
              "fontSize": 24, "textAlignHorizontal": "LEFT", "textAlignVertical": "TOP",
              "letterSpacing": -0.5, "lineHeightPx": 32, "lineHeightPercent": 100, "lineHeightUnit": "PIXELS",
              "textCase": "ORIGINAL", "textDecoration": "NONE"},
    "characterStyleOverrides": [], "styleOverrideTable": {}, "lineTypes": ["NONE"], "lineIndentations": [0],
    "styles": {"text": "S:h1"},
}

HIDDEN = {"id": "1:9", "name": "Черновик", "type": "RECTANGLE", "visible": False, "scrollBehavior": "SCROLLS",
          "blendMode": "PASS_THROUGH", "absoluteBoundingBox": None, "absoluteRenderBounds": None,
          "fills": [{"blendMode": "NORMAL", "type": "SOLID", "visible": False, "color": INK}], "strokes": [],
          "strokeWeight": 1, "strokeAlign": "INSIDE", "effects": []}

SCREEN = {
    "id": "1:2", "name": "Экран входа", "type": "FRAME", "scrollBehavior": "SCROLLS", "blendMode": "PASS_THROUGH",
    "clipsContent": True, "children": [HEADING, BUTTON, HIDDEN],
    "absoluteBoundingBox": {"x": 0, "y": 0, "width": 390, "height": 844},
    "absoluteRenderBounds": {"x": 0, "y": 0, "width": 390, "height": 844},
    "layoutMode": "VERTICAL", "primaryAxisAlignItems": "MIN", "counterAxisAlignItems": "MIN",
    "primaryAxisSizingMode": "FIXED", "counterAxisSizingMode": "FIXED",
    "paddingLeft": 24, "paddingRight": 24, "paddingTop": 24, "paddingBottom": 24, "itemSpacing": 16,
    "fills": [{"blendMode": "NORMAL", "type": "SOLID", "color": {"r": 1, "g": 1, "b": 1, "a": 1}}],
    "strokes": [], "strokeWeight": 1, "strokeAlign": "INSIDE", "effects": [],
    "opacity": 1,
}

PAGE = {"id": "0:1", "name": "Экраны", "type": "CANVAS", "scrollBehavior": "SCROLLS",
        "backgroundColor": {"r": 0.96, "g": 0.96, "b": 0.96, "a": 1}, "prototypeStartNodeID": None,
        "flowStartingPoints": [], "prototypeDevice": {"type": "NONE", "rotation": "NONE"}, "children": [SCREEN]}

FILE = {"name": "Кабинет клиента", "role": "editor", "lastModified": "2026-09-01T10:00:00Z", "editorType": "figma",
        "thumbnailUrl": "https://example/t.png", "version": "2024", "schemaVersion": 0,
        "document": {"id": "0:0", "name": "Document", "type": "DOCUMENT", "scrollBehavior": "SCROLLS", "children": [PAGE]},
        "components": COMPONENTS, "componentSets": {}, "styles": STYLES}

ROOT_COMMENT = {"id": "500", "file_key": "Kb7Cabinet01", "parent_id": "", "user": HUMAN,
                "created_at": "2026-09-05T09:00:00Z", "resolved_at": None, "message": "Обсуждение экрана входа",
                "client_meta": {"node_id": "1:2", "node_offset": {"x": 10, "y": 20}}, "order_id": "3", "reactions": []}
ASK_REPLY = {"id": "501", "file_key": "Kb7Cabinet01", "parent_id": "500", "user": HUMAN,
             "created_at": "2026-09-05T09:05:00Z", "resolved_at": None,
             "message": "@buffbot какой цвет у основной кнопки?", "client_meta": {"x": 0, "y": 0}, "order_id": None, "reactions": []}
BOT_REPLY = {"id": "502", "file_key": "Kb7Cabinet01", "parent_id": "500", "user": BOT,
             "created_at": "2026-09-05T09:06:00Z", "resolved_at": None, "message": "@buffbot Принял",
             "client_meta": {"x": 0, "y": 0}, "order_id": None, "reactions": []}

VARIABLES_META = {
    "variableCollections": {"VariableCollectionId:1:0": {
        "id": "VariableCollectionId:1:0", "name": "Тема", "key": "vc1", "remote": False, "hiddenFromPublishing": False,
        "modes": [{"modeId": "1:0", "name": "Светлая"}, {"modeId": "1:1", "name": "Тёмная"}],
        "defaultModeId": "1:0", "variableIds": ["VariableID:1:1", "VariableID:1:2"]}},
    "variables": {
        "VariableID:1:1": {"id": "VariableID:1:1", "name": "Цвет/Акцент", "key": "v1",
                           "variableCollectionId": "VariableCollectionId:1:0", "resolvedType": "COLOR",
                           "valuesByMode": {"1:0": MALACHITE, "1:1": {"r": 0.2, "g": 0.8, "b": 0.6, "a": 1}},
                           "remote": False, "description": "", "hiddenFromPublishing": False, "scopes": ["ALL_SCOPES"], "codeSyntax": {}},
        "VariableID:1:2": {"id": "VariableID:1:2", "name": "Отступ/S", "key": "v2",
                           "variableCollectionId": "VariableCollectionId:1:0", "resolvedType": "FLOAT",
                           "valuesByMode": {"1:0": 8, "1:1": 8},
                           "remote": False, "description": "", "hiddenFromPublishing": False, "scopes": ["GAP"], "codeSyntax": {}},
    },
}


class FakeHost:
    """The host's buff/http, answered from a table. Unknown → 404 in Figma's form."""

    def __init__(self, answers: dict, variables: str = "forbidden"):
        self.answers = answers
        self.variables = variables
        self.calls: list[tuple[str, str, dict]] = []

    def __call__(self, method, url, credential="", headers=None, body="", binary=False):
        path = url.replace("https://api.figma.com", "")
        self.calls.append((method, path, json.loads(body) if body else {}))
        if path.startswith("/v1/files/") and path.endswith("/variables/local"):
            if self.variables == "forbidden":
                return {"status": 403, "headers": {}, "body": json.dumps(
                    {"status": 403, "error": True, "message": "This API is only available to Enterprise organizations."})}
            return {"status": 200, "headers": {}, "body": json.dumps({"status": 200, "error": False, "meta": VARIABLES_META})}
        if binary:
            return {"status": 200, "headers": {}, "body_base64": base64.b64encode(b"\x89PNG\r\n\x1a\n\x00").decode()}
        key = (method, path.split("?")[0])
        if key in self.answers:
            payload = self.answers[key]
            return {"status": 200, "headers": {}, "body": json.dumps(payload(path, self.calls[-1][2]) if callable(payload) else payload)}
        return {"status": 404, "headers": {}, "body": json.dumps({"status": 404, "err": "Not found"})}


def host(extra: dict | None = None) -> FakeHost:
    answers = {
        ("GET", "/v1/me"): {**BOT, "email": "bot@example.com"},
        ("GET", "/v1/projects/4815162342/files"): {"name": "Кабинет", "files": [
            {"key": "Kb7Kit000002", "name": "Набор компонентов", "thumbnail_url": "", "last_modified": "2026-09-01T00:00:00Z"}]},
        ("GET", "/v1/files/Kb7Cabinet01"): FILE,
        ("GET", "/v1/files/Kb7Cabinet01/meta"): {"file": {"name": "Кабинет клиента", "folder_name": "Кабинет",
                                                          "last_touched_at": "2026-09-01T10:00:00Z", "creator": HUMAN,
                                                          "editorType": "figma", "role": "editor", "version": "2024"}},
        ("GET", "/v1/files/Kb7Cabinet01/nodes"): lambda path, _: {
            "name": "Кабинет клиента", "role": "editor", "lastModified": "2026-09-01T10:00:00Z", "editorType": "figma",
            "thumbnailUrl": "", "version": "2024",
            "nodes": {nid: ({"document": {"1:5": BUTTON, "1:2": SCREEN, "3:1": {"id": "3:1", "name": "Кнопка/Малахит", "type": "RECTANGLE",
                                                                                 "fills": [{"blendMode": "NORMAL", "type": "SOLID", "color": MALACHITE}]},
                                          "3:2": {"id": "3:2", "name": "Заголовок/H1", "type": "TEXT", "style": HEADING["style"]}}[nid],
                             "components": COMPONENTS, "componentSets": {}, "schemaVersion": 0, "styles": STYLES}
                            if nid in ("1:5", "1:2", "3:1", "3:2") else None)
                      for nid in path.split("ids=")[1].split("&")[0].replace("%3A", ":").split(",")}},
        ("GET", "/v1/files/Kb7Cabinet01/styles"): {"status": 200, "error": False, "meta": {"styles": [
            {"key": "k1", "file_key": "Kb7Cabinet01", "node_id": "3:1", "style_type": "FILL", "thumbnail_url": "",
             "name": "Кнопка/Малахит", "description": "Основная кнопка", "created_at": "2026-08-01T00:00:00Z",
             "updated_at": "2026-08-02T00:00:00Z", "user": HUMAN, "sort_position": "!"},
            {"key": "k2", "file_key": "Kb7Cabinet01", "node_id": "3:2", "style_type": "TEXT", "thumbnail_url": "",
             "name": "Заголовок/H1", "description": "", "created_at": "2026-08-01T00:00:00Z",
             "updated_at": "2026-08-02T00:00:00Z", "user": HUMAN, "sort_position": "\""}]}},
        ("GET", "/v1/files/Kb7Cabinet01/comments"): {"comments": [BOT_REPLY, ASK_REPLY, ROOT_COMMENT]},
        ("POST", "/v1/files/Kb7Cabinet01/comments"): lambda path, body: {**ASK_REPLY, "id": "777", "user": BOT,
                                                                          "message": body.get("message", "")},
        ("GET", "/v1/images/Kb7Cabinet01"): {"err": None, "images": {
            "1:2": "https://figma-alpha-api.s3.us-west-2.amazonaws.com/images/abc/1-2.png", "9:9": None}},
    }
    answers.update(extra or {})
    fake = FakeHost(answers)
    main.plugin.http = fake
    main._project_files["at"] = 0.0
    main._project_files["files"] = {}
    main._file_names.clear()
    main._variable_names.clear()
    main._mentioned.clear()
    main.me.update({"id": "", "handle": "", "email": ""})
    return fake


# ---- the design, as parameters ----------------------------------------------------

class Colours(unittest.TestCase):
    def test_floats_become_hex_and_alpha_becomes_a_percentage(self):
        self.assertEqual(design.hex_of(MALACHITE), "#0FA37F")
        self.assertEqual(design.hex_of({"r": 0, "g": 0, "b": 0, "a": 0.16}), "#000000 16%")
        # paint opacity folds into the colour: that is what the inspector shows
        self.assertEqual(design.hex_of({"r": 1, "g": 1, "b": 1, "a": 1}, opacity=0.5), "#FFFFFF 50%")


class Nodes(unittest.TestCase):
    def setUp(self):
        self.ctx = design.Context(styles=STYLES, components=COMPONENTS,
                                  variables={"VariableID:1:1": "Цвет/Акцент", "VariableID:1:2": "Отступ/S"})

    def test_a_button_reads_like_its_inspector_panel(self):
        b = design.compress(BUTTON, self.ctx)
        self.assertEqual(b["type"], "instance")
        self.assertEqual(b["component"], "Кнопка/Основная", "an instance names its component, not an id")
        self.assertEqual(b["properties"], {"Состояние": "Обычная", "Текст": "Войти"},
                         "component property names lose the #id suffix the API appends")
        self.assertEqual(b["bounds"], {"x": 24, "y": 288, "w": 342, "h": 48})
        self.assertEqual(b["layout"]["mode"], "horizontal")
        self.assertEqual(b["layout"]["padding"], {"top": 12, "right": 24, "bottom": 12, "left": 24})
        self.assertEqual(b["layout"]["gap"], 8)
        self.assertEqual(b["placement"], {"width": "fill", "height": "fixed"})
        self.assertEqual(b["radius"], 12, "four equal radii are one radius")
        self.assertEqual(b["fills"], [{"type": "solid", "color": "#0FA37F", "variable": "Цвет/Акцент"}])
        self.assertEqual(b["styles"], {"fill": "Кнопка/Малахит"}, "a style id becomes the name a design system documents")
        self.assertEqual(b["variables"], {"fills": ["Цвет/Акцент"], "itemSpacing": "Отступ/S"})
        self.assertEqual(b["effects"], [{"type": "drop_shadow", "color": "#000000 16%", "offset": {"x": 0, "y": 4}, "blur": 12}])
        self.assertNotIn("strokes", b, "an empty stroke list is not a stroke")
        self.assertNotIn("opacity", b, "a default is not a fact worth a line")
        # children come along, the same way
        self.assertEqual(b["children"][0]["text"], "Войти")
        self.assertEqual(b["children"][0]["typography"], {"font": "Inter", "style": "Semi Bold", "weight": 600, "size": 16,
                                                          "line_height": 24, "align": "center"})

    def test_a_text_node_carries_its_words_and_type_style(self):
        h = design.compress(HEADING, self.ctx)
        self.assertEqual(h["text"], "Войти в кабинет")
        self.assertEqual(h["typography"]["letter_spacing"], -0.5)
        self.assertNotIn("align", h["typography"], "left is the default and says nothing")
        self.assertEqual(h["styles"], {"text": "Заголовок/H1"})

    def test_unknown_ids_stay_as_ids_rather_than_vanish(self):
        # Without the Variables endpoint (every non-Enterprise plan) an id still tells
        # a reviewer that two fields share one token.
        b = design.compress(BUTTON, design.Context(styles=STYLES, components={}))
        self.assertEqual(b["component"], "2:1")
        self.assertEqual(b["variables"]["itemSpacing"], "VariableID:1:2")

    def test_a_hidden_layer_is_marked_and_its_invisible_paint_is_dropped(self):
        h = design.compress(HIDDEN, self.ctx)
        self.assertTrue(h["hidden"])
        self.assertNotIn("fills", h)
        self.assertNotIn("bounds", h, "an invisible node has no bounds")

    def test_nothing_is_truncated(self):
        many = {**SCREEN, "children": [dict(HEADING, id=f"1:{i}") for i in range(200)]}
        self.assertEqual(len(design.compress(many, self.ctx)["children"]), 200)

    def test_outline_is_the_skeleton(self):
        o = design.outline(PAGE)
        self.assertEqual(o["children"][0]["size"], {"w": 390, "h": 844})
        self.assertEqual(o["children"][0]["children"][0]["text"], "Войти в кабинет")
        self.assertNotIn("fills", json.dumps(o))

    def test_search_matches_by_words_in_name_or_text(self):
        words = design.words_of("кнопка войти")
        self.assertTrue(design.matches(BUTTON, words, []))
        self.assertTrue(design.matches(BUTTON_TEXT, design.words_of("войти"), ["text"]))
        self.assertFalse(design.matches(BUTTON, words, ["text"]), "a type filter is a filter")
        self.assertFalse(design.matches(HEADING, words, []))


class Tokens(unittest.TestCase):
    def test_variables_come_out_by_mode_with_aliases_named(self):
        collections, names = design.variables_of(VARIABLES_META)
        self.assertEqual(names["VariableID:1:1"], "Цвет/Акцент")
        theme = collections[0]
        self.assertEqual(theme["name"], "Тема")
        self.assertEqual(theme["modes"], ["Светлая", "Тёмная"])
        self.assertEqual(theme["default_mode"], "Светлая")
        accent = theme["variables"][0]
        self.assertEqual(accent["values"], {"Светлая": "#0FA37F", "Тёмная": "#33CC99"})
        self.assertEqual(design.variable_value({"type": "VARIABLE_ALIAS", "id": "VariableID:1:1"}, names), "→ Цвет/Акцент")

    def test_a_published_style_value_is_read_off_its_node(self):
        ctx = design.Context()
        self.assertEqual(design.style_value("FILL", {"fills": [{"type": "SOLID", "color": MALACHITE, "blendMode": "NORMAL"}]}, ctx),
                         [{"type": "solid", "color": "#0FA37F"}])
        self.assertEqual(design.style_value("TEXT", {"style": HEADING["style"]}, ctx)["size"], 24)
        self.assertIsNone(design.style_value("FILL", None, ctx), "a style whose node was not returned has no value, not a crash")


# ---- the plugin surface -----------------------------------------------------------

class Scope(unittest.TestCase):
    def test_scope_is_the_listed_files_plus_the_projects_files(self):
        fake = host()
        self.assertEqual(set(main.scope_files()), {"Kb7Cabinet01", "Kb7Kit000002"})
        self.assertEqual(sum(1 for m, p, _ in fake.calls if "projects/" in p), 1)
        main.scope_files()
        self.assertEqual(sum(1 for m, p, _ in fake.calls if "projects/" in p), 1, "a project's files are cached")

    def test_reading_outside_the_scope_is_refused(self):
        host()
        with self.assertRaises(RuntimeError):
            main.get_file(key="Zz9Secret000")
        with self.assertRaises(RuntimeError):
            main.reply(issue="Zz9Secret000#1", text="нет")


class Requests(unittest.TestCase):
    def test_a_mention_in_a_reply_becomes_a_request_in_the_root_thread(self):
        host()
        events = main.poll(None)
        self.assertEqual(len(events), 1, "the root comment has no mention and the bot's own reply is not a request")
        e = events[0]
        self.assertEqual(e.action, "ask")
        self.assertEqual(e.body, "какой цвет у основной кнопки?")
        self.assertEqual(e.extra["issue"], "Kb7Cabinet01#500", "the thread is the ROOT comment")
        self.assertEqual(e.author, "Пётр Петров")
        self.assertIn("node-id=1-2", e.url, "the link opens the pinned node")
        self.assertIn("Кабинет клиента", e.title)
        self.assertEqual(e.id, "comment:501@2026-09-05T09:05:00Z")

    def test_the_asker_has_no_login_and_an_e_mail_only_if_figma_gives_one(self):
        # A handle is a display name, not a login (#476); Figma's comment user
        # carries no e-mail as documented, so the «who may ask» rules see none.
        host()
        e = main.poll(None)[0]
        self.assertEqual((e.author_login, e.author_email), ("", ""))
        shown = dict(ASK_REPLY, user=dict(HUMAN, email="petrov@corp.local"))
        host({("GET", "/v1/files/Kb7Cabinet01/comments"): {"comments": [ROOT_COMMENT, shown]}})
        self.assertEqual(main.poll(None)[0].author_email, "petrov@corp.local")

    def test_the_watermark_and_the_bots_own_id_are_respected(self):
        host()
        self.assertEqual(main.poll("2026-09-05T09:05:00Z"), [], "a comment at the watermark was already seen")
        # the bot is told apart by its id, the one thing every comment carries
        own = dict(ASK_REPLY, user=dict(BOT, handle="Кто-то другой"))
        host({("GET", "/v1/files/Kb7Cabinet01/comments"): {"comments": [own]}})
        self.assertEqual(main.poll(None), [])

    def test_an_alias_is_a_mention_too(self):
        aliased = dict(ASK_REPLY, message="@buff research почему кнопка зелёная")
        host({("GET", "/v1/files/Kb7Cabinet01/comments"): {"comments": [aliased]}})
        e = main.poll(None)[0]
        self.assertEqual((e.action, e.body), ("research", "почему кнопка зелёная"))

    def test_a_comment_in_a_file_outside_the_scope_starts_nothing(self):
        host({("GET", "/v1/projects/4815162342/files"): {"name": "Кабинет", "files": []}})
        main.whoami()
        self.assertIsNone(main.comment_event("Zz9Secret000", ASK_REPLY, None))


class Writing(unittest.TestCase):
    def test_reply_goes_to_the_root_of_the_thread_with_the_link(self):
        fake = host()
        main.reply(issue="Kb7Cabinet01#500", text="Малахит, #0FA37F.",
                   link={"title": "Открыть в Buff", "url": "https://buff.systems/app/x"},
                   attachment={"filename": "report.pdf", "base64": "QQ=="})
        posted = [b for m, p, b in fake.calls if m == "POST"][0]
        self.assertEqual(posted["comment_id"], "500")
        self.assertIn("Малахит", posted["message"])
        self.assertIn("https://buff.systems/app/x", posted["message"])
        self.assertIn("report.pdf", posted["message"], "a file Figma cannot take is named, not dropped")


class Tools(unittest.TestCase):
    def test_get_file_is_the_outline_plus_styles_and_components(self):
        host()
        f = main.get_file(key="Kb7Cabinet01", depth=2)
        self.assertEqual(f["name"], "Кабинет клиента")
        self.assertEqual(f["pages"][0]["children"][0]["name"], "Экран входа")
        self.assertIn({"name": "Кнопка/Малахит", "type": "fill", "remote": False}, f["styles"])
        self.assertEqual(f["components"][0]["name"], "Кнопка/Основная")

    def test_get_node_resolves_names_and_refuses_a_missing_node(self):
        host()
        n = main.get_node(key="Kb7Cabinet01", node_id="1:5")
        self.assertEqual(n["node"]["styles"], {"fill": "Кнопка/Малахит"})
        self.assertEqual(n["node"]["component"], "Кнопка/Основная")
        # no Variables on this plan: the id stays, the answer does not pretend
        self.assertEqual(n["node"]["variables"]["fills"], ["VariableID:1:1"])
        with self.assertRaises(RuntimeError):
            main.get_node(key="Kb7Cabinet01", node_id="9:9")

    def test_get_styles_carries_values_and_says_what_the_plan_withholds(self):
        host()
        s = main.get_styles(key="Kb7Cabinet01")
        malachite = next(x for x in s["published"] if x["name"] == "Кнопка/Малахит")
        self.assertEqual(malachite["value"], [{"type": "solid", "color": "#0FA37F"}])
        h1 = next(x for x in s["published"] if x["name"] == "Заголовок/H1")
        self.assertEqual(h1["value"]["size"], 24)
        self.assertFalse(s["variables"]["available"])
        self.assertIn("тарифе", s["variables"]["note"])
        self.assertNotIn("collections", s["variables"])

    def test_get_styles_reads_variables_where_the_plan_allows(self):
        fake = host()
        fake.variables = "enterprise"
        s = main.get_styles(key="Kb7Cabinet01")
        self.assertTrue(s["variables"]["available"])
        self.assertEqual(s["variables"]["collections"][0]["variables"][0]["values"]["Светлая"], "#0FA37F")
        # and from then on node readers name the variables
        n = main.get_node(key="Kb7Cabinet01", node_id="1:5")
        self.assertEqual(n["node"]["variables"]["fills"], ["Цвет/Акцент"])

    def test_search_nodes_finds_by_words_with_page_and_path(self):
        host()
        found = main.search_nodes(key="Kb7Cabinet01", query="кнопка войти")
        self.assertEqual([n["id"] for n in found["nodes"]], ["1:5"])
        self.assertEqual(found["nodes"][0]["page"], "Экраны")
        self.assertEqual(found["nodes"][0]["path"], "Экран входа")
        texts = main.search_nodes(key="Kb7Cabinet01", query="войти", types=["text"])
        self.assertEqual({n["text"] for n in texts["nodes"]}, {"Войти в кабинет", "Войти"})

    def test_get_comments_groups_replies_under_their_root(self):
        host()
        c = main.get_comments(key="Kb7Cabinet01")
        self.assertEqual(c["total"], 1)
        root = c["threads"][0]
        self.assertEqual(root["node"], "1:2")
        self.assertEqual([r["id"] for r in root["replies"]], ["501", "502"])
        self.assertFalse(root["resolved"])

    def test_render_node_answers_with_a_picture_as_mcp_content(self):
        fake = host()
        out = main.render_node(key="Kb7Cabinet01", node_id="1:2", scale=2)
        self.assertEqual(out["content"][0]["type"], "image")
        self.assertEqual(out["content"][0]["mimeType"], "image/png")
        self.assertTrue(base64.b64decode(out["content"][0]["data"]).startswith(b"\x89PNG"))
        self.assertIn("scale=2", [p for m, p, _ in fake.calls if "/v1/images/" in p][0])
        # the picture is fetched WITHOUT the API credential: it lives on storage, not on the API
        with self.assertRaises(RuntimeError):
            main.render_node(key="Kb7Cabinet01", node_id="9:9")

    def test_list_files_names_both_sources(self):
        host()
        listed = main.list_files()
        by_key = {f["key"]: f for f in listed["files"]}
        self.assertEqual(by_key["Kb7Kit000002"]["project"], "Кабинет")
        self.assertEqual(by_key["Kb7Cabinet01"]["name"], "Кабинет клиента")


class EmptyMeansEverything(unittest.TestCase):
    """No files and no projects is every file the bot's account can open (#429).

    Figma already draws that line with its own sharing. What it cannot do is
    list those files, so a question in a comment is found through the callback:
    FILE_COMMENT names the file and the comment, the comment is read from the
    API, and the bot answers where it was mentioned.
    """

    def setUp(self):
        self.patches = [unittest.mock.patch.object(main, "FILES", []),
                        unittest.mock.patch.object(main, "PROJECTS", [])]
        for p in self.patches:
            p.start()
        self.fake = host()

    def tearDown(self):
        for p in self.patches:
            p.stop()

    def callback(self, **kw):
        payload = {"event_type": "FILE_COMMENT", "file_key": "Kb7Cabinet01", "file_name": "Кабинет клиента",
                   "comment_id": "501", "comment": [{"text": "какой цвет у основной кнопки?"}],
                   "mentions": [{"id": BOT["id"], "handle": BOT["handle"]}], "triggered_by": HUMAN,
                   "created_at": "2026-09-05T09:05:00Z", "timestamp": "2026-09-05T09:05:01Z",
                   "passcode": "", "protocol_version": "2", "retries": 0, "webhook_id": "22"}
        payload.update(kw)
        return main.webhook({}, json.dumps(payload))

    def test_reading_and_writing_work_in_any_file(self):
        self.assertEqual(main.get_file(key="Kb7Cabinet01")["name"], "Кабинет клиента")
        main.reply(issue="Kb7Cabinet01#500", text="готово")
        self.assertTrue(any(m == "POST" for m, _, _ in self.fake.calls))

    def test_a_callback_about_a_mention_becomes_a_request_in_its_thread(self):
        events = self.callback()
        self.assertEqual(len(events), 1)
        e = events[0]
        self.assertEqual(e.extra["issue"], "Kb7Cabinet01#500", "the thread is the ROOT comment, read from the API")
        self.assertEqual(e.body, "какой цвет у основной кнопки?")
        self.assertEqual(e.id, "comment:501@2026-09-05T09:05:00Z", "the same id the poll gives, so the host dedups")

    def test_a_comment_without_a_mention_starts_nothing_and_is_not_watched(self):
        self.assertEqual(self.callback(comment_id="500"), [])
        self.assertEqual(main.poll(None), [])

    def test_the_poll_recovers_files_where_the_bot_was_mentioned(self):
        self.assertEqual(main.poll(None), [], "nothing is known yet, so nothing is read")
        self.assertFalse(any("/comments" in p for _, p, _ in self.fake.calls))
        self.callback()
        self.assertEqual([e.extra["issue"] for e in main.poll(None)], ["Kb7Cabinet01#500"])

    def test_anything_but_a_comment_is_not_a_request(self):
        self.assertEqual(self.callback(event_type="PING"), [])
        self.assertEqual(main.webhook({}, "не json"), [])
        self.assertEqual(self.callback(file_key=""), [])

    def test_list_files_says_the_area_is_everything(self):
        self.callback()
        listed = main.list_files()
        self.assertEqual([f["key"] for f in listed["files"]], ["Kb7Cabinet01"])
        self.assertIn("все файлы, открытые учётной записи", listed["area"])

    def test_the_check_says_the_area_is_everything_the_account_opens(self):
        detail = main.check()["detail"]
        self.assertIn("все файлы, открытые учётной записи", detail)
        self.assertNotIn("0 из", detail)


class Callback(unittest.TestCase):
    def test_a_callback_about_a_file_outside_the_list_reads_nothing(self):
        fake = host()
        payload = {"event_type": "FILE_COMMENT", "file_key": "Zz9Secret000", "comment_id": "501"}
        self.assertEqual(main.webhook({}, json.dumps(payload)), [])
        self.assertFalse(any("Zz9Secret000" in p for _, p, _ in fake.calls))

    def test_a_callback_in_a_listed_file_is_a_request(self):
        host()
        payload = {"event_type": "FILE_COMMENT", "file_key": "Kb7Cabinet01", "comment_id": "501"}
        self.assertEqual([e.extra["issue"] for e in main.webhook({}, json.dumps(payload))], ["Kb7Cabinet01#500"])


class Check(unittest.TestCase):
    def test_check_says_who_what_and_what_the_plan_withholds(self):
        host()
        out = main.check()
        self.assertTrue(out["ok"])
        self.assertIn("buffbot (bot@example.com)", out["detail"])
        self.assertIn("Бот видит 1 из 2 указанных файлов", out["detail"], "the kit file answers 404 on this stand")
        self.assertIn("На этом тарифе Figma переменные недоступны", out["detail"])


class TestTheseTestsActuallyRun(unittest.TestCase):
    def test_nothing_is_defined_after_the_entry_point(self):
        src = open(__file__, encoding="utf-8").read()
        self.assertTrue(src.rstrip().endswith("unittest.main()"), "unittest.main() must be the last statement")


if __name__ == "__main__":
    unittest.main()
