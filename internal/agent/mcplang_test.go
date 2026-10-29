package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode"

	"github.com/buffsystems/buff-connector/internal/i18n"
)

// A run names its language (#565): a refusal reaches its model in it; a run that names none
// gets the connector's own.
func TestACallIsRefusedInTheRunsLanguage(t *testing.T) {
	defer i18n.SetDefault(i18n.Default())
	i18n.SetDefault(i18n.RU)
	s, _ := surfaceWithDemo(t)
	for lang, want := range map[string]string{
		"en": `connection "nope" is not running here`,
		"ru": `подключение "nope" здесь не запущено`,
		"":   `подключение "nope" здесь не запущено`,
	} {
		params, _ := json.Marshal(map[string]any{"slug": "nope", "tool": "echo", "lang": lang})
		_, err := s.Handle(context.Background(), "mcp/call", params)
		if err == nil || err.Error() != want {
			t.Errorf("lang %q: %v, want %q", lang, err, want)
		}
	}
}

// What Buff Code's model is told first about the tools exists in both languages and names
// the tools the way the catalogue does.
func TestInstructionsInBothLanguages(t *testing.T) {
	for _, l := range []i18n.Lang{i18n.RU, i18n.EN} {
		text := instructionsIn(l)
		if !strings.Contains(text, toolSep) {
			t.Errorf("%s: %q does not name the tools as <connection>%s<tool>", l, text, toolSep)
		}
		cyrillic := strings.IndexFunc(text, func(r rune) bool { return unicode.Is(unicode.Cyrillic, r) }) >= 0
		if cyrillic != (l == i18n.RU) {
			t.Errorf("%s: %q", l, text)
		}
	}
}
