package agent

import (
	"github.com/buffsystems/buff-connector/internal/config"
	"github.com/buffsystems/buff-connector/internal/i18n"
)

// ignoredSectionNotices explains, one line per section, why a part of the
// config is doing nothing — and the two causes take opposite advice. A section
// from a NEWER connector is fixed by upgrading. A RETIRED one is not: upgrading
// is what removed it, so the line names the replacement instead.
func ignoredSectionNotices(skipped []string) []string {
	var out []string
	for _, name := range skipped {
		if note, ok := config.RetiredSection(name); ok {
			out = append(out, i18n.Sprintf("конфиг — раздел %q больше не используется. %s", name, i18n.T(note)))
			continue
		}
		out = append(out, i18n.Sprintf(
			"конфиг — раздел %q пропущен: эта версия коннектора его не знает, обновите её", name))
	}
	return out
}
