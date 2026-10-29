package i18n

// Checks of the tool-text files (plugins' tools.<lang>.md, devgate tools):
// what buffpub and a plugin load say when a file and the code disagree (#718).
func init() {
	add(map[string]string{
		"devgate: тексты инструментов %s: %v":                         "devgate: tool texts %s: %v",
		"тексты инструментов только на одном языке: нужны и %s, и %s": "tool texts in one language only: both %s and %s are needed",
		"тексты инструментов: %w":                                     "tool texts: %w",
		"%s.%s: нет текста":                                           "%s.%s: no text",
		"%s: нет текста":                                              "%s: no text",
		"параметр %q раньше первого инструмента":                      "parameter %q before any tool",
		"%s: %q — не имя параметра":                                   "%s: %q is not a parameter name",
		"%s.%s: написан дважды":                                       "%s.%s: written twice",
		"%q — не имя инструмента":                                     "%q is not a tool name",
		"%s: написан дважды":                                          "%s: written twice",
		"нет ни одного инструмента":                                   "there are no tools",
		"параметр %s: его нет в схеме":                                "parameter %s: not in the schema",
	})
}
