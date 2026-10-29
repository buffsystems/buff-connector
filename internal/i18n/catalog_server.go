package i18n

// What the connector says outside the command line: the local page's server
// side, the sign-in return page, the gateway Buff Code talks to, and the errors
// built deep inside that reach a person through one of them.
func init() {
	add(map[string]string{
		// local page: settings, pairing, connections
		"этот раздел появился в более новой версии коннектора — обновите его.":               "this section belongs to a newer version of the connector — update it.",
		"сначала настройте провайдера и сохраните: %v":                                       "set up a provider and save it first: %v",
		"нет такого подключения":                                                             "no such connection",
		"настройки этого подключения не подходят версии %s: %w":                              "this connection's settings do not fit version %s: %w",
		"способ входа для «%s» изменён — введите учётные данные заново":                      "the sign-in method for “%s” has changed — enter the credentials again",
		"проверка не прошла":                                                                 "the check failed",
		"учётные данные «%s»: имя должно быть идентификатором, вид — env, secret или header": "credential “%s”: the name must be an identifier and the kind env, secret or header",
		"правило %s: не регулярное выражение: %v":                                            "rule %s: not a regular expression: %v",
		"встроенная интеграция с Jira заменена интеграцией из каталога: поставьте её в разделе «Каталог» и перенесите настройки. Раздел jira в файле сохранён, но не используется.": "the built-in Jira integration has been replaced by the one in the catalogue: install it in the “Catalogue” section and move the settings over. The jira section in the file is kept but not used.",

		// signing a credential in (oauth2)
		"подключение %s не найдено": "connection %s not found",
		"у подключения нет учётных данных %q вида oauth2 — сначала сохраните подключение с client_id и client_secret": "the connection has no oauth2 credential %q — save the connection with client_id and client_secret first",
		"плагин не объявляет oauth для %q": "the plugin declares no oauth for %q",
		"Вход не выполнен":                 "Sign-in failed",
		"Вход выполнен":                    "Signed in",
		"Источник ответил: %s":             "The source answered: %s",
		"Этот возврат не относится к входу, начатому на странице коннектора, или вход начат слишком давно. Начните заново.": "This return does not belong to a sign-in started on the connector's page, or the sign-in was started too long ago. Start again.",
		"Подключение работает от имени %s. Вернитесь на страницу коннектора — эту вкладку можно закрыть.":                   "The connection works on behalf of %s. Go back to the connector's page — this tab can be closed.",
		"выбранной учётной записи": "the chosen account",
		"вставьте адрес страницы, на которую вернул источник, или код из него": "paste the address of the page the source returned you to, or the code from it",
		"вход не был начат или начат слишком давно — нажмите «Войти» ещё раз":  "the sign-in was not started or was started too long ago — press “Sign in” again",

		// integrations and the operator's own MCP servers
		"сервер %s %s отвечает, инструментов: %d": "server %s %s answers, tools: %d",
		"зависимости не установлены: %s":          "the dependencies are not installed: %s",
		"зависимости не установились: %v":         "the dependencies did not install: %v",
		"этому серверу нужен Node.js (18 или новее), а на этой машине его нет — или он стоит там, куда пользователю подключения нет доступа (например, в /root). Поставьте его пакетным менеджером системы": "this server needs Node.js (18 or newer), and this machine has none — or it is installed where the connection's user has no access (in /root, for example). Install it with the system's package manager",
		"для установки зависимостей нужен npm рядом с Node.js":                                                                          "installing the dependencies needs npm next to Node.js",
		"этот сервер запускается через npx, а его на машине нет":                                                                        "this server is started with npx, and the machine has none",
		"этому серверу нужен Python 3 (или uv), а на этой машине его нет — или он стоит там, куда пользователю подключения нет доступа": "this server needs Python 3 (or uv), and this machine has none — or it is installed where the connection's user has no access",
		"этот сервер запускается через uvx, а его на машине нет":                                                                        "this server is started with uvx, and the machine has none",
		"интернет":            "internet",
		"интернет: %s":        "internet: %s",
		"интернет, кроме: %s": "internet, except: %s",

		// your own model server (BYO AI)
		"свой сервер моделей на коннекторе не настроен": "no model server of your own is set up on the connector",
		"вызов модели без запроса":                      "a model call with no request",
		"свой сервер моделей не ответил: %w":            "your own model server did not answer: %w",

		// the link to Buff
		"кадр чужого потока %q в потоке %q":            "a frame of stream %q in stream %q",
		"кадр потока без номера":                       "a stream frame with no number",
		"кадр потока: %w":                              "stream frame: %w",
		"неверный кусок ответа":                        "a malformed chunk of the answer",
		"кусок ответа: %w":                             "chunk of the answer: %w",
		"%s: ожидался ответ, пришёл поток":             "%s: expected an answer, got a stream",
		"%s: кадр %d потока не пришёл — прошу дослать": "%s: stream frame %d did not arrive — asking for it again",
		"%s: бэкенд замолчал — ни кадра за %s":         "%s: the backend went silent — no frame for %s",
		"%s: ожидался поток, пришёл обычный ответ":     "%s: expected a stream, got a plain answer",

		// the gateway for Buff Code
		"нет такого адреса":           "no such address",
		"только POST":                 "POST only",
		"тело запроса — не JSON":      "the request body is not JSON",
		"тело запроса — не JSON-RPC":  "the request body is not JSON-RPC",
		"не удалось прочитать запрос": "could not read the request",
		"коннектор не достучался до Buff — попробуйте ещё раз":                 "the connector could not reach Buff — try again",
		"нужен вход в Buff: токен разработчика в Authorization":                "sign in to Buff first: the developer token goes in Authorization",
		"нужен токен разработчика":                                             "a developer token is required",
		"этот коннектор не умеет потоковые ответы":                             "this connector cannot stream answers",
		"сначала initialize: сессия не открыта":                                "initialize first: no session is open",
		"сессия не найдена — откройте новую (initialize)":                      "session not found — open a new one (initialize)",
		"метод не поддерживается: %s":                                          "method not supported: %s",
		"нужно имя инструмента":                                                "a tool name is required",
		"этот коннектор работает для организации %s, а вход выполнен в другую": "this connector works for organisation %s, and the sign-in is to another one",
		"пользователь не ответил на вопрос — ничего не сделано":                "the user did not answer the question — nothing was done",
		"на этот вопрос ответа не ждут":                                        "no answer is expected to this question",
		"Добавить эту запись в базу знаний":                                    "Add this entry to the knowledge base",
		"вся организация":                                                      "the whole organisation",
		"правило":                                                              "rule",
		"факт":                                                                 "fact",
		"ловушка":                                                              "pitfall",
		"Модель предлагает добавить запись в базу знаний организации %s.\n\n«%s» — %s, область: %s, имя: %s\n%s\n\n%s": "The model proposes adding an entry to the knowledge base of organisation %s.\n\n“%s” — %s, scope: %s, name: %s\n%s\n\n%s",

		// buffpub: the catalogue and the offline kit
		"интеграция":  "integration",
		"инструменты": "tools",
		"офлайн-комплект готов: %d файл(ов) в %s\n":                                                     "offline kit ready: %d file(s) in %s\n",
		"на машине без сети: BUFF_CATALOG_URL=file://<путь> connector install <плагин> --config <файл>": "on a machine with no network: BUFF_CATALOG_URL=file://<path> connector install <plugin> --config <file>",
	})
}
