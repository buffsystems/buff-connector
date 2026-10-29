package i18n

// The local page's server answers: refusals of a request, in the request's
// language (writeJSON and the guard translate them on the way out).
func init() {
	add(map[string]string{
		"чужой адрес":              "forbidden host",
		"нет локального заголовка": "missing local header",
		"неверный запрос":          "bad request",
		"нужен state_dir":          "state_dir required",
		"нужен код":                "code required",
		"нет доступа — откройте адрес, который коннектор напечатал при запуске (в нём одноразовый токен), или передайте токен в заголовке X-Buff-Token": "unauthorized — open the URL the connector printed on start (it carries a one-time token), or send it as the X-Buff-Token header",
		"плагины не включены":                 "plugins are not enabled",
		"в запросе нет архива":                "no archive in the request",
		"нет своего сервера с таким id":       "no custom server with this id",
		"плагинов нет":                        "no plugins",
		"у этого коннектора не задан каталог": "no catalogue is configured for this connector",
	})
}
