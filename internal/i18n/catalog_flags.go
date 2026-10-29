package i18n

// The flag package's own words — a command's usage and its complaints about a
// wrong flag — which the standard library writes in English only (flags.go).
func init() {
	add(map[string]string{
		"флаг %s не предусмотрен":                        "flag %s is not defined",
		"флагу %s нужно значение":                        "flag %s needs a value",
		"неверное логическое значение %s у флага %s: %s": "invalid boolean value %s for the flag %s: %s",
		"неверное значение %s у флага %s: %s":            "invalid value %s for the flag %s: %s",
		"неверная запись флага: %s":                      "bad flag syntax: %s",
		"использование %s:":                              "usage of %s:",
		"текст":                                          "text",
		"число":                                          "number",
		"длительность":                                   "duration",
		"значение":                                       "value",
		" (по умолчанию %s)":                             " (default %s)",
	})
}
