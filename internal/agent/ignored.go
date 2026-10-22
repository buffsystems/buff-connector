package agent

import (
	"fmt"

	"github.com/buffsystems/buff-connector/internal/config"
)

// ignoredSectionNotices explains, one line per section, why a part of the
// config is doing nothing — and the two causes take opposite advice. A section
// from a NEWER connector is fixed by upgrading. A RETIRED one is not: upgrading
// is what removed it, so the line names the replacement instead.
func ignoredSectionNotices(skipped []string) []string {
	var out []string
	for _, name := range skipped {
		if note, ok := config.RetiredSection(name); ok {
			out = append(out, fmt.Sprintf("config: section %q is no longer used — %s", name, note))
			continue
		}
		out = append(out, fmt.Sprintf(
			"config: ignoring section %q — this agent version does not support it; upgrade it", name))
	}
	return out
}
