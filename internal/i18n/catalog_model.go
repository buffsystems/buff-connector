package i18n

// What the connector tells the MODEL besides its tools' descriptions (those are
// files per language — internal/devgate/tools, plugins' tools.<lang>.md): the
// results and refusals of a tool call, read in the language of the call (#565).
func init() {
	add(map[string]string{
		// a call from the platform's runs or Buff Code, before it reaches a plugin
		"вызов не разобран: %w":            "the call could not be parsed: %w",
		"подключение %q здесь не запущено": "connection %q is not running here",
		"подключение %q вызывают чаще %d раз в минуту; сбавь темп, вызовы не отбрасываются": "connection %q is being called more than %d times a minute; slow down — calls are not dropped",
		"%s — не инструмент только для чтения, а этой стадии можно только читать":           "%s is not a read-only tool, and this stage may only read",
		"инструмента %q нет: имя — <подключение>%s<инструмент>":                             "there is no tool %q: the name is <connection>%s<tool>",
		"инструмента %q нет":             "there is no tool %q",
		"%s: аргументы не разобраны: %v": "%s: the arguments could not be parsed: %v",
		"\n\n[Выдача не закончилась: вызови этот инструмент ещё раз с теми же аргументами и cursor=%d]": "\n\n[The output is not finished: call this tool again with the same arguments and cursor=%d]",
		"Репозитории проекта сейчас недоступны: %v":                                                     "The project's repositories are unavailable right now: %v",

		// the organisation's knowledge for Buff Code
		"База знаний организации сейчас недоступна: %v":     "The organisation's knowledge base is unavailable right now: %v",
		"knowledge_read_entry: name не задан":               "knowledge_read_entry: name is not set",
		"knowledge_write_entry: аргументы не разобраны: %v": "knowledge_write_entry: the arguments could not be parsed: %v",
		"этот клиент не умеет спрашивать пользователя, поэтому запись не добавлена; предложи пользователю добавить её в кабинете, в разделе «База знаний»": "this client cannot ask the user, so the entry was not added; suggest the user add it in the cabinet, in the “Knowledge base” section",
		"пользователь не стал добавлять запись — не предлагай её снова и не записывай обходным путём":                                                      "the user chose not to add the entry — do not offer it again and do not write it some other way",
		"%v (человек)": "%v (a person)",
	})
}
