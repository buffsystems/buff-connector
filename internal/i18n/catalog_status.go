package i18n

// The lines of a connection's problem codes (internal/problem): what the
// terminal, the local page and the log read. The cabinet keeps its own text
// for each code — the connector reports the code, not this line.
func init() {
	add(map[string]string{
		"идентификатор %s уже занят подключением номер %s": "id %s is already used by the connection at position %s",
		"имя %s уже занято подключением «%s» — два подключения не могут давать инструменты под одним именем, переименуйте одно из них": "the name %s is already used by “%s” — two connections cannot offer tools under the same name; rename one of them",

		"плагин %s %s не установлен":            "plugin %s %s is not installed",
		"у плагина %s нет записи о целостности": "plugin %s has no integrity record",
		"плагин %s %s изменён после установки":  "plugin %s %s has been modified since it was installed",
		"версия отозвана издателем (%s)":        "the publisher withdrew this version (%s)",
		"настройки подключения не подходят":     "the connection's settings do not fit",
		"плагину не на чем работать — нужен Python %s или новее, доступный пользователю подключения (интеграция работает под собственным пользователем ОС, поэтому интерпретатор в домашнем каталоге root ей не подходит). Поставьте его пакетным менеджером или разверните среду в %s": "the plugin has nothing to run on — it needs Python %s or newer, available to the connection's user (an integration runs as its own OS user, so an interpreter in root's home directory will not do). Install it with the package manager or set up an environment in %s",

		"настройки должны быть объектом":            "the settings must be an object",
		"%s — должно быть объектом":                 "%s must be an object",
		"не заполнено поле %s":                      "%s is required",
		"%s — такой настройки у плагина нет":        "%s is not a setting this plugin understands",
		"%s — должно быть списком":                  "%s must be a list",
		"%s — должно быть текстом":                  "%s must be text",
		"%s — не короче %s символов":                "%s must be at least %s characters",
		"%s — не подходит по форме":                 "%s does not match the expected form",
		"%s — одно из значений %s":                  "%s must be one of %s",
		"%s — нужен полный адрес вместе с https://": "%s must be a full URL including https://",
		"%s — должно быть числом":                   "%s must be a number",
		"%s — не меньше %s":                         "%s must be at least %s",
		"%s — не больше %s":                         "%s must be at most %s",
		"%s — true или false":                       "%s must be true or false",

		"для выхода в сеть нужна настройка %s, а она не задана":         "network access needs %s, which is not set",
		"%s = %s — не адрес, которым можно ограничить плагин":           "%s = %s is not an address the plugin can be limited to",
		"ошибка в настройке %s":                                         "error in the setting %s",
		"ошибка в списке разрешённых сайтов":                            "error in the list of allowed sites",
		"ошибка в списке запрещённых сайтов":                            "error in the list of blocked sites",
		"%s — не тип содержимого (text/html, application/json, text/*)": "%s is not a media type (text/html, application/json, text/*)",
		"пустая строка": "an empty entry",
		"%s — домен пишется без схемы (example.com, а не https://example.com)": "%s — write the domain without a scheme (example.com, not https://example.com)",
		"%s — только ASCII, похожая буква — это уже другой домен":              "%s — only ASCII; a look-alike letter is a different domain",
		"%s — без пробелов": "%s — no whitespace",
		"%s — это не домен": "%s is not a domain",
		"%s — без звёздочки в домене, поддомены и так включены": "%s — no wildcard in the domain; subdomains are included already",
		"%s — правило называет сайт, а не порт":                 "%s — a rule names a site, not a port",

		"не удалось записать пользователя подключения":            "could not record the connection's user",
		"не удалось создать каталог %s":                           "could not create the directory %s",
		"не удалось выставить права каталогу %s":                  "could not set the permissions of the directory %s",
		"не удалось передать каталог %s пользователю подключения": "could not hand the directory %s to the connection's user",
		"плагины нельзя запустить изолированно":                   "plugins cannot run isolated",
		"коннектор работает под uid %s, а изоляции плагинов нужен root или CAP_SETUID (контейнер, запущенный с --user, этого тоже не может)": "the connector runs as uid %s, and plugin isolation needs root or CAP_SETUID (a container started with --user cannot do it either)",
		"на Windows плагины пока не работают — для изоляции нужна отдельная локальная служебная учётная запись, а установщик её не создаёт":  "plugins do not run on Windows yet — isolating them needs a dedicated local service account, which the installer does not create",
		"этот процесс не может сменить пользователя дочернему процессу":                                                                      "this process cannot change a child's user",

		"не удалось открыть %s плагина":                                             "could not open the plugin's %s",
		"не удалось запустить %s":                                                   "could not start %s",
		"плагин не ответил на рукопожатие":                                          "the plugin did not answer the handshake",
		"плагин не ответил на рукопожатие (последнее, что он написал — %s)":         "the plugin did not answer the handshake (the last thing it wrote — %s)",
		"плагину нужен host API %s, а этот коннектор знает %s — обновите коннектор": "the plugin needs host API %s, this connector speaks %s — update the connector",
		"не удалось завершить рукопожатие":                                          "could not complete the handshake",
		"не удалось получить список инструментов":                                   "could not read the list of tools",
		"список инструментов не кончается — сервер повторяет курсор %s":             "the list of tools never ends — the server repeats cursor %s",
		"плагин прислал кадр больше предела в %s байт":                              "the plugin sent a frame larger than the %s byte limit",
		"плагин закрыл канал":                                                       "the plugin closed the channel",
		"канал закрыт":                                                              "the channel is closed",
		"не удалось закодировать параметры":                                         "could not encode the parameters",
		"время ожидания истекло":                                                    "timed out",
		"операция отменена":                                                         "cancelled",

		"вызов %s не дошёл до сервера":                     "the call %s did not reach the server",
		"%s — сервер принял запрос, но не ответил на него": "%s — the server accepted the request without answering it",
		"%s — сервер больше не знает эту сессию (404)":     "%s — the server no longer knows this session (404)",
		"%s — сервер ответил %s (%s)":                      "%s — the server answered %s (%s)",
		"%s — ответ не читается":                           "%s — the answer is unreadable",
		"поток событий прервался":                          "the event stream broke off",
		"поток событий закончился без ответа":              "the event stream ended without the answer",

		"нет значения для %s": "no value for %s",
		"в «%s» не всё заполнено — заполните настройку или уберите её из команды":                                             "“%s” is not filled in — fill the setting in, or remove it from the command",
		"команда «%s» не найдена на этой машине (или недоступна пользователю подключения)":                                    "the command “%s” is not found on this machine (or the connection's user cannot reach it)",
		"команда «%s» не найдена на этой машине — поставьте её так, чтобы коннектор видел её в PATH, или укажите полный путь": "the command “%s” is not found on this machine — install it where the connector sees it in PATH, or give the full path",
	})
}
