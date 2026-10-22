"""Files on disk — local folders and SMB shares — as context for the model.

The tools are opencode's file tools, kept by name, parameter and output shape
so a model that knows them needs no new habits: `glob` finds files by name,
`grep` searches their text, `read` reads one (or lists a folder). What is
different is behind them: an Office document or a PDF is searched and read as
the text a person sees, a path may be on a share, everything stays inside the
connection's area — and nothing is capped: no result limit, no default line
limit, no line truncation. Paging is the model's choice, through offset/limit.
"""

from __future__ import annotations

import fnmatch
import re

from buff_plugin import Plugin, visible

import area
import extract
import store

plugin = Plugin("files", host_api=5)

AREA = area.Area(plugin.config.get("roots") or [], plugin.config.get("excludes") or [])
LOCAL = store.Local()
SMB = store.Smb(lambda: plugin.credential("share"))


def _where() -> str:
    roots = ", ".join(str(r) for r in AREA.roots) or "весь диск машины с коннектором"
    ex = f"; исключены: {', '.join(AREA.excludes)}" if AREA.excludes else ""
    return (f"Это папки КОМПАНИИ на диске и сетевых папках клиента — не репозиторий проекта. "
            f"Область подключения: {roots}{ex}.")


def store_for(loc: area.Location):
    return SMB if loc.kind == "smb" else LOCAL


def resolve(raw, what: str = "путь") -> area.Location:
    loc = area.parse(raw)
    if loc.kind == "local":
        real = LOCAL.realpath(loc)
        if real.path != loc.path and not AREA.contains(real):
            raise ValueError(f"{what} {loc} ведёт по ссылке в {real} — вне области. {_where()}")
    if not AREA.contains(loc):
        raise ValueError(f"{what} {loc} — вне области подключения. {_where()}")
    return loc


def _skip(loc: area.Location, entry: store.Entry) -> bool:
    return AREA.excluded(loc)


def _starts(path) -> list[area.Location]:
    return [resolve(path, "папка")] if str(path or "").strip() else AREA.starting_points()


def _glob_re(pattern: str) -> re.Pattern:
    """A glob over a path: `*` and `?` stay within one segment, `**` spans
    folders — the ripgrep dialect opencode's glob speaks."""
    out, i = "", 0
    while i < len(pattern):
        ch = pattern[i]
        if pattern.startswith("**/", i):
            out += "(?:.*/)?"
            i += 3
        elif pattern.startswith("**", i):
            out += ".*"
            i += 2
        elif ch == "*":
            out += "[^/]*"
            i += 1
        elif ch == "?":
            out += "[^/]"
            i += 1
        else:
            out += re.escape(ch)
            i += 1
    return re.compile("^" + out + "$")


@plugin.tool(description=f"Найти файлы по имени — шаблон вида \"*.docx\", \"**/*.xlsx\", \"договор*\" (регистр не важен). "
                         "Без path ищет по всей области, с path — в этой папке и вложенных. Возвращает пути, "
                         "новые сначала. Использовать, когда нужен файл по имени или расширению; содержимое ищет grep. "
                         f"{_where()}",
             read_only=True,
             schema={"type": "object", "properties": {
                 "pattern": {"type": "string", "description": "Шаблон имени: * ? и ** для вложенных папок"},
                 "path": {"type": "string", "description": "Папка, в которой искать; пусто — вся область"}},
                 "required": ["pattern"]})
def glob(pattern: str = "", path: str = ""):
    if not str(pattern or "").strip():
        raise ValueError("нужен шаблон имени, например *.docx")
    pattern = str(pattern).replace("\\", "/")
    by_name = "/" not in pattern
    rx = _glob_re(pattern.lower())
    found = []
    for start in _starts(path):
        s = store_for(start)
        base = start.path.rstrip("/")
        for loc, entry in s.walk(start, _skip):
            rel = loc.path[len(base):].lstrip("/") if base and loc.path.startswith(base) else loc.path.lstrip("/")
            subject = entry.name if by_name else rel
            if rx.match(subject.lower()):
                found.append((entry.mtime, str(loc)))
    if not found:
        return "Файлы не найдены"
    found.sort(key=lambda t: (-t[0], t[1]))
    return "\n".join(p for _, p in found)


@plugin.tool(description="Найти по содержимому — регулярное выражение по тексту файлов области (регистр не важен): "
                         "документы Word, таблицы Excel, презентации PowerPoint (и старые .doc/.xls/.ppt), PDF и "
                         "текстовые файлы читаются как текст. Возвращает файл, номер строки и строку; номер строки "
                         "тот же, что покажет read. path сужает до папки, include — до имён (\"*.pdf\", \"*.{doc,docx}\"). "
                         "Использовать ПЕРВЫМ делом, когда вопрос касается регламентов, договоров, планов, цифр — "
                         f"того, что записано в файлах. {_where()}",
             read_only=True,
             schema={"type": "object", "properties": {
                 "pattern": {"type": "string", "description": "Регулярное выражение (Python), регистр не важен"},
                 "path": {"type": "string", "description": "Папка или файл; пусто — вся область"},
                 "include": {"type": "string", "description": "Шаблон имён файлов, например *.docx или *.{xls,xlsx}"}},
                 "required": ["pattern"]})
def grep(pattern: str = "", path: str = "", include: str = ""):
    if not str(pattern or ""):
        raise ValueError("нужно регулярное выражение для поиска")
    try:
        rx = re.compile(str(pattern), re.IGNORECASE)
    except re.error as exc:
        raise ValueError(f"регулярное выражение не разбирается: {exc}") from exc
    names = _include_patterns(include)
    rows: list[tuple[str, int, str]] = []
    for start in _starts(path):
        s = store_for(start)
        entry = s.stat(start)
        files = [(start, entry)] if not entry.is_dir else s.walk(start, _skip)
        for loc, entry in files:
            if names and not any(fnmatch.fnmatchcase(entry.name.lower(), n) for n in names):
                continue
            try:
                text = extract.text_of(entry.name, s.read(loc))
            except (extract.Unreadable, store.Missing, PermissionError, OSError):
                continue
            for n, line in enumerate(text.splitlines(), 1):
                if rx.search(line):
                    rows.append((str(loc), n, line))
    if not rows:
        return "Ничего не найдено"
    out = [f"Найдено {_count(len(rows))}"]
    current = None
    for path_, n, line in rows:
        if path_ != current:
            out += ["", f"{path_}:"]
            current = path_
        out.append(f"  Строка {n}: {line}")
    return "\n".join(out)


def _include_patterns(include: str) -> list[str]:
    include = str(include or "").strip().lower()
    if not include:
        return []
    m = re.match(r"^(.*)\{([^}]*)\}(.*)$", include)
    if m:
        return [m.group(1) + alt.strip() + m.group(3) for alt in m.group(2).split(",")]
    return [include]


def _count(n: int) -> str:
    if n % 10 == 1 and n % 100 != 11:
        return f"{n} совпадение"
    if 2 <= n % 10 <= 4 and not 12 <= n % 100 <= 14:
        return f"{n} совпадения"
    return f"{n} совпадений"


@plugin.tool(description="Прочитать файл или папку по абсолютному пути (локальному /srv/… или сетевому "
                         "\\\\сервер\\папка\\…, smb://сервер/папка/…). Документ Word приходит как markdown, таблица — "
                         "листами-таблицами, презентация — слайдами с заметками, PDF — постранично, текстовый файл — "
                         "как есть; строки пронумерованы «N: текст». Папка — списком записей, папки со слэшем на конце. "
                         "По умолчанию файл читается ЦЕЛИКОМ; offset (с какой строки, от 1) и limit (сколько строк) — "
                         "если нужна часть. Использовать, когда путь известен или найден через glob/grep. "
                         f"{_where()}",
             read_only=True,
             schema={"type": "object", "properties": {
                 "filePath": {"type": "string", "description": "Абсолютный путь к файлу или папке"},
                 "offset": {"type": "integer", "description": "Первая строка (от 1); пусто — с начала"},
                 "limit": {"type": "integer", "description": "Сколько строк; пусто — до конца"}},
                 "required": ["filePath"]})
def read(filePath: str = "", offset: int = 0, limit: int = 0):
    loc = resolve(filePath, "файл")
    s = store_for(loc)
    try:
        entry = s.stat(loc)
    except store.Missing:
        raise ValueError(_missing(loc, s)) from None
    if entry.is_dir:
        return _read_dir(loc, s)
    try:
        text = extract.text_of(entry.name, s.read(loc))
    except extract.Unreadable as exc:
        return f"<path>{loc}</path>\n<type>binary</type>\n<kind>{extract.kind_of(entry.name)}</kind>\n({exc} — прочитать как текст нельзя)"
    lines = text.splitlines()
    start = int(offset or 1)
    if start < 1 or (start > len(lines) and not (len(lines) == 0 and start == 1)):
        raise ValueError(f"offset {start} вне файла: в нём {len(lines)} строк")
    end = min(len(lines), start - 1 + int(limit)) if limit else len(lines)
    body = "\n".join(f"{n}: {line}" for n, line in zip(range(start, end + 1), lines[start - 1:end]))
    tail = (f"(Показаны строки {start}–{end} из {len(lines)}. Продолжить с offset={end + 1})" if end < len(lines)
            else f"(Конец файла — всего {len(lines)} строк)")
    return f"<path>{loc}</path>\n<type>file</type>\n<kind>{extract.kind_of(entry.name)}</kind>\n<content>\n{body}\n\n{tail}\n</content>"


def _read_dir(loc: area.Location, s) -> str:
    entries = [e for e in s.list(loc) if not AREA.excluded(loc.join(e.name))]
    names = [e.name + ("/" if e.is_dir else "") for e in entries]
    return f"<path>{loc}</path>\n<type>directory</type>\n<entries>\n" + "\n".join(names) + f"\n\n({_entries(len(names))})\n</entries>"


def _entries(n: int) -> str:
    if n % 10 == 1 and n % 100 != 11:
        return f"{n} запись"
    if 2 <= n % 10 <= 4 and not 12 <= n % 100 <= 14:
        return f"{n} записи"
    return f"{n} записей"


def _missing(loc: area.Location, s) -> str:
    """opencode's «did you mean»: neighbours whose name shares text with the
    one asked for."""
    base = loc.name.lower()
    stem = base.rsplit(".", 1)[0]
    try:
        near = [str(loc.parent().join(e.name)) for e in s.list(loc.parent())
                if stem in e.name.lower() or e.name.lower().rsplit(".", 1)[0] in base]
    except (store.Missing, OSError):
        near = []
    msg = f"Файл не найден: {loc}"
    if near:
        msg += "\n\nВозможно, вы имели в виду:\n" + "\n".join(near)
    return msg


@plugin.check
def check():
    if not AREA.roots:
        return {"ok": True, "detail": "Папки не заданы — бот читает весь диск машины, на которой стоит коннектор."}
    seen, notes = 0, []
    for root in AREA.roots:
        try:
            store_for(root).stat(root)
            seen += 1
        except store.Missing:
            notes.append(f"{root}: нет такого пути")
        except Exception as exc:  # a share that will not answer names itself
            notes.append(f"{root}: {exc}")
    servers = sorted({r.server for r in AREA.roots if r.kind == "smb"})
    if servers:
        account = plugin.credential("share").get("username") or "(без учётной записи)"
        notes.append(f"сетевые папки {', '.join(servers)} — учётная запись {account}")
    detail = visible(seen, len(AREA.roots), one="указанную папку", many="указанные папки",
                     many_of="указанных папок", fix="Проверьте путь и права на чтение.")
    if notes:
        detail += " " + "; ".join(notes) + "."
    return {"ok": seen == len(AREA.roots), "detail": detail}


if __name__ == "__main__":
    plugin.run()
