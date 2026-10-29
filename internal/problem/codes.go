package problem

// def is one code: its Russian line (a catalogue key, every verb %s, filled by
// the params in order) and the names the params are reported under.
type def struct {
	ru     string
	params []string
}

// codes are the problems a connection can be reported with — everything the
// connector itself constructs on the way to a connection's last error. A
// client keeps a text for each (the cabinet: src/lib/connectorProblem.ts);
// adding one here means adding it there, and a client that does not know a
// code yet shows the connector's own text instead.
//
// A line never holds ": " — that is where the cause starts.
var codes = map[string]def{
	// The connection's own settings in connector.yaml.
	"instance_id_taken":   {"идентификатор %s уже занят подключением номер %s", []string{"id", "position"}},
	"instance_slug_taken": {"имя %s уже занято подключением «%s» — два подключения не могут давать инструменты под одним именем, переименуйте одно из них", []string{"slug", "other"}},

	// The installed plugin.
	"plugin_not_installed":     {"плагин %s %s не установлен", []string{"plugin", "version"}},
	"plugin_integrity_missing": {"у плагина %s нет записи о целостности", []string{"plugin"}},
	"plugin_modified":          {"плагин %s %s изменён после установки", []string{"plugin", "version"}},
	"plugin_withdrawn":         {"версия отозвана издателем (%s)", []string{"reason"}},
	"plugin_settings_invalid":  {"настройки подключения не подходят", nil},
	"python_missing": {"плагину не на чем работать — нужен Python %s или новее, доступный " +
		"пользователю подключения (интеграция работает под собственным пользователем ОС, " +
		"поэтому интерпретатор в домашнем каталоге root ей не подходит). Поставьте его " +
		"пакетным менеджером или разверните среду в %s", []string{"version", "dir"}},

	// A setting, checked against the plugin's schema.
	"setting_root_not_object": {"настройки должны быть объектом", nil},
	"setting_not_object":      {"%s — должно быть объектом", []string{"field"}},
	"setting_required":        {"не заполнено поле %s", []string{"field"}},
	"setting_unknown":         {"%s — такой настройки у плагина нет", []string{"field"}},
	"setting_not_list":        {"%s — должно быть списком", []string{"field"}},
	"setting_not_text":        {"%s — должно быть текстом", []string{"field"}},
	"setting_too_short":       {"%s — не короче %s символов", []string{"field", "min"}},
	"setting_bad_form":        {"%s — не подходит по форме", []string{"field"}},
	"setting_not_in_enum":     {"%s — одно из значений %s", []string{"field", "choices"}},
	"setting_not_url":         {"%s — нужен полный адрес вместе с https://", []string{"field"}},
	"setting_not_number":      {"%s — должно быть числом", []string{"field"}},
	"setting_below_min":       {"%s — не меньше %s", []string{"field", "min"}},
	"setting_above_max":       {"%s — не больше %s", []string{"field", "max"}},
	"setting_not_bool":        {"%s — true или false", []string{"field"}},

	// Where the plugin may go on the network.
	"egress_setting_missing": {"для выхода в сеть нужна настройка %s, а она не задана", []string{"field"}},
	"egress_setting_not_url": {"%s = %s — не адрес, которым можно ограничить плагин", []string{"field", "value"}},
	"web_setting":            {"ошибка в настройке %s", []string{"field"}},
	"web_allow_list":         {"ошибка в списке разрешённых сайтов", nil},
	"web_deny_list":          {"ошибка в списке запрещённых сайтов", nil},
	"web_content_type":       {"%s — не тип содержимого (text/html, application/json, text/*)", []string{"value"}},
	"web_entry_empty":        {"пустая строка", nil},
	"web_entry_scheme":       {"%s — домен пишется без схемы (example.com, а не https://example.com)", []string{"entry"}},
	"web_entry_ascii":        {"%s — только ASCII, похожая буква — это уже другой домен", []string{"entry"}},
	"web_entry_space":        {"%s — без пробелов", []string{"entry"}},
	"web_entry_not_domain":   {"%s — это не домен", []string{"entry"}},
	"web_entry_wildcard":     {"%s — без звёздочки в домене, поддомены и так включены", []string{"entry"}},
	"web_entry_port":         {"%s — правило называет сайт, а не порт", []string{"entry"}},

	// The connection's own user and directories.
	"instance_uid_record": {"не удалось записать пользователя подключения", nil},
	"dir_create":          {"не удалось создать каталог %s", []string{"path"}},
	"dir_chmod":           {"не удалось выставить права каталогу %s", []string{"path"}},
	"dir_chown":           {"не удалось передать каталог %s пользователю подключения", []string{"path"}},

	// Isolation.
	"no_isolation":         {"плагины нельзя запустить изолированно", nil},
	"no_isolation_uid":     {"коннектор работает под uid %s, а изоляции плагинов нужен root или CAP_SETUID (контейнер, запущенный с --user, этого тоже не может)", []string{"uid"}},
	"no_isolation_windows": {"на Windows плагины пока не работают — для изоляции нужна отдельная локальная служебная учётная запись, а установщик её не создаёт", nil},
	"no_isolation_child":   {"этот процесс не может сменить пользователя дочернему процессу", nil},

	// Starting the process and the handshake.
	"plugin_pipe":           {"не удалось открыть %s плагина", []string{"stream"}},
	"plugin_start":          {"не удалось запустить %s", []string{"path"}},
	"handshake_failed":      {"плагин не ответил на рукопожатие", nil},
	"handshake_failed_said": {"плагин не ответил на рукопожатие (последнее, что он написал — %s)", []string{"said"}},
	"host_api_newer":        {"плагину нужен host API %s, а этот коннектор знает %s — обновите коннектор", []string{"needed", "spoken"}},
	"handshake_end_failed":  {"не удалось завершить рукопожатие", nil},
	"tools_list_failed":     {"не удалось получить список инструментов", nil},
	"tools_cursor_loop":     {"список инструментов не кончается — сервер повторяет курсор %s", []string{"cursor"}},
	"frame_too_large":       {"плагин прислал кадр больше предела в %s байт", []string{"limit"}},
	"plugin_closed":         {"плагин закрыл канал", nil},
	"channel_closed":        {"канал закрыт", nil},
	"encode_params":         {"не удалось закодировать параметры", nil},
	"timed_out":             {"время ожидания истекло", nil},
	"cancelled":             {"операция отменена", nil},

	// A server reached over HTTP.
	"remote_unreachable":  {"вызов %s не дошёл до сервера", []string{"method"}},
	"remote_no_answer":    {"%s — сервер принял запрос, но не ответил на него", []string{"method"}},
	"remote_session_gone": {"%s — сервер больше не знает эту сессию (404)", []string{"method"}},
	"remote_status":       {"%s — сервер ответил %s (%s)", []string{"method", "status", "body"}},
	"remote_unreadable":   {"%s — ответ не читается", []string{"method"}},
	"remote_event_stream": {"поток событий прервался", nil},
	"remote_stream_ended": {"поток событий закончился без ответа", nil},

	// A server the operator brought.
	"placeholder_missing":     {"нет значения для %s", []string{"names"}},
	"command_value_missing":   {"в «%s» не всё заполнено — заполните настройку или уберите её из команды", []string{"text"}},
	"command_runtime_missing": {"команда «%s» не найдена на этой машине (или недоступна пользователю подключения)", []string{"command"}},
	"command_not_found":       {"команда «%s» не найдена на этой машине — поставьте её так, чтобы коннектор видел её в PATH, или укажите полный путь", []string{"command"}},
}

// Codes lists every code, for the tests that hold the clients to the table.
func Codes() map[string][]string {
	out := make(map[string][]string, len(codes))
	for k, d := range codes {
		out[k] = append([]string(nil), d.params...)
	}
	return out
}
