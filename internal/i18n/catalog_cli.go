package i18n

// The command line: cmd/connector and the terminal wizard (connector setup --cli).
func init() {
	add(map[string]string{
		// connector catalog / install
		"%s — %s\nиздатель: %s\n":         "%s — %s\npublisher: %s\n",
		"\n  версия %s (host API %d)%s\n": "\n  version %s (host API %d)%s\n",
		"    выход наружу: по полям %s\n": "    outbound access: by the fields %s\n",
		"    учётные данные: %s\n":        "    credentials: %s\n",
		"    вебхуки: %s\n":               "    webhooks: %s\n",
		" [только чтение]":                " [read only]",
		"    инструмент %s%s — %s\n":      "    tool %s%s — %s\n",
		"в каталоге нет плагина %q":       "the catalogue has no plugin %q",
		"нет совместимой версии":          "no compatible version",
		"нет версии %s, совместимой с этой сборкой коннектора": "no version of %s is compatible with this connector build",
		"в каталоге нет %s %s":    "the catalogue has no %s %s",
		"установлен %s %s (%s)\n": "installed %s %s (%s)\n",
		"нет":                     "none",
		"включено":                "enabled",
		"выключено":               "disabled",
		"нет подключения %q":      "no connection %q",
		"проверено, записано НЕ будет:\n%s\n": "checked, NOT written:\n%s\n",
		"сохранено: %s (%s)\n":                "saved: %s (%s)\n",
		// connector instance secret / signin / rm
		"введите значение и нажмите Ctrl-D:":                          "type the value and press Ctrl-D:",
		"пустое значение":                                             "empty value",
		"плагин не предлагает способ входа %q для %q (есть: %s)":      "the plugin offers no sign-in method %q for %q (available: %s)",
		"записано в %s (права 0600), в конфиг попала только ссылка\n": "written to %s (mode 0600); the config holds only a reference to it\n",
		"вид доступа: %s, учётная запись %s\n":                        "access: %s, account %s\n",
		"вид доступа: %s\n":                                           "access: %s\n",
		"откройте в браузере и войдите:\n":                            "open this in a browser and sign in:\n",
		"затем вставьте сюда адрес страницы, на которую вернул источник (или код из него), и нажмите Enter:": "then paste the address of the page the source returned you to (or the code from it) and press Enter:",
		"вход выполнен как %s\n":   "signed in as %s\n",
		"вход выполнен\n":          "signed in\n",
		"будет удалено: %s (%s)\n": "would remove: %s (%s)\n",
		"удалено: %s\n":            "removed: %s\n",

		// connector setup --cli
		"Настройка коннектора Buff. Токен остаётся у вас (в локальном файле).":         "Buff connector setup. The token stays with you (in a local file).",
		"Адрес relay (обычно менять не нужно)":                                         "Relay address (usually no need to change it)",
		"Каталог состояния (state_dir)":                                                "State directory (state_dir)",
		"Провайдер (gitlab/ghe/github/gitea/bitbucket-server/bitbucket, пусто=готово)": "Provider (gitlab/ghe/github/gitea/bitbucket-server/bitbucket, empty = done)",
		"Токен доступа":       "Access token",
		"  ошибка: %v\n":      "  error: %v\n",
		"  нет доступа: %v\n": "  no access: %v\n",
		"  токен валиден, но репозиториев не видно — пропускаю": "  the token is valid but no repositories are visible — skipping",
		"Выберите репозитории (напр. 1,3 или all)":              "Choose repositories (e.g. 1,3 or all)",
		"  ничего не выбрано — пропускаю провайдера":            "  nothing chosen — skipping the provider",
		"  добавлено: %s (%d репо)\n":                           "  added: %s (%d repos)\n",
		"\nНи одного провайдера не добавлено. Это допустимо: интеграции\nставятся отдельно — посмотрите `connector catalog`.": "\nNo provider added. That is fine: integrations\nare installed separately — see `connector catalog`.",
		"Конфиг сохранён: %s\n":                                           "Config saved: %s\n",
		"Код привязки из приложения Buff (пусто=позже)":                   "Pairing code from the Buff app (empty = later)",
		"привязка не удалась: %v\n":                                       "pairing failed: %v\n",
		"готово — агент привязан. Запустите: connector run --config %s\n": "done — the agent is paired. Run: connector run --config %s\n",
		"Позже выполните: connector pair --config %s --code <код>\n":      "Later, run: connector pair --config %s --code <code>\n",
	})
}
