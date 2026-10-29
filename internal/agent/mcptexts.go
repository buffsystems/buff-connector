package agent

import (
	"embed"

	"github.com/buffsystems/buff-connector/internal/i18n"
)

// What Buff Code's model is told first about the connector's tools is prompt text,
// so it lives in a file per language, not in the code (texts/instructions.<lang>.md).
// Each connection's line follows it, with the administrator's notes.

//go:embed texts/*.md
var modelTexts embed.FS

// instructionsIn is the opening of the instructions in l; Russian when l has no file.
func instructionsIn(l i18n.Lang) string {
	if b, err := modelTexts.ReadFile("texts/instructions." + string(l) + ".md"); err == nil {
		return string(b)
	}
	b, _ := modelTexts.ReadFile("texts/instructions.ru.md")
	return string(b)
}
