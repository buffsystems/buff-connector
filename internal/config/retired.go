package config

// Sections earlier connectors read and this one no longer does.
//
// An ignored section has two opposite causes. One came from a NEWER connector
// and the answer is to upgrade. One came from an OLDER connector, where the
// section was retired — and there "upgrade" points at the very version that
// removed it. The config is kept either way (see Config.Unknown); what differs
// is what the operator is told.
var retiredSections = map[string]string{
	"jira": "встроенная интеграция с Jira заменена интеграцией из каталога: " +
		"поставьте её в разделе «Каталог» и перенесите настройки. " +
		"Раздел jira в файле сохранён, но не используется.",
}

// RetiredSection reports whether a section was retired, and what to do instead.
func RetiredSection(name string) (string, bool) {
	note, ok := retiredSections[name]
	return note, ok
}
