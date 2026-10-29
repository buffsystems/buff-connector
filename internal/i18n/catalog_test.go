package i18n

import (
	"fmt"
	"testing"
)

func withCatalog(t *testing.T, m map[string]string) {
	t.Helper()
	saved := catalog
	catalog = m
	resetPatterns()
	t.Cleanup(func() { catalog = saved; resetPatterns() })
}

func TestT(t *testing.T) {
	withCatalog(t, map[string]string{"сохранено": "saved"})
	if got := EN.T("сохранено"); got != "saved" {
		t.Errorf("EN.T = %q", got)
	}
	if got := RU.T("сохранено"); got != "сохранено" {
		t.Errorf("RU.T = %q", got)
	}
	// A line with no English yet is shown as it is rather than as nothing.
	if got := EN.T("неизвестно"); got != "неизвестно" {
		t.Errorf("missing line = %q", got)
	}
}

func TestSprintfKeepsVerbs(t *testing.T) {
	withCatalog(t, map[string]string{"сохранено: %s (%s)": "saved: %s (%s)"})
	if got := EN.Sprintf("сохранено: %s (%s)", "jira", "abc"); got != "saved: jira (abc)" {
		t.Errorf("EN.Sprintf = %q", got)
	}
	if got := RU.Sprintf("сохранено: %s (%s)", "jira", "abc"); got != "сохранено: jira (abc)" {
		t.Errorf("RU.Sprintf = %q", got)
	}
}

func TestLocalizeExactAndPattern(t *testing.T) {
	withCatalog(t, map[string]string{
		"нет такого подключения":                         "no such connection",
		"подключение %s не найдено":                      "connection %s not found",
		"команда %q не найдена на этой машине":           "command %q is not on this machine",
		"свой сервер моделей не ответил: %w":             "your own model server did not answer: %w",
		"зависимости не установились: %s":                "the dependencies did not install: %s",
		"плагину не на чем работать: нужен Python %d.%d": "the plugin has nothing to run on: it needs Python %d.%d",
	})
	cases := map[string]string{
		"нет такого подключения":                              "no such connection",
		"подключение abc-1 не найдено":                        "connection abc-1 not found",
		`команда "uvx" не найдена на этой машине`:             `command "uvx" is not on this machine`,
		"плагину не на чем работать: нужен Python 3.11":       "the plugin has nothing to run on: it needs Python 3.11",
		"свой сервер моделей не ответил: dial tcp: refused":   "your own model server did not answer: dial tcp: refused",
		"зависимости не установились: нет такого подключения": "the dependencies did not install: no such connection",
		// A prefix the catalogue does not know is kept, the rest translated.
		"install: нет такого подключения": "install: no such connection",
		// Nothing known: unchanged.
		"что-то совсем другое": "что-то совсем другое",
		"plain English":        "plain English",
	}
	for in, want := range cases {
		if got := Localize(EN, in); got != want {
			t.Errorf("Localize(EN, %q) = %q, want %q", in, got, want)
		}
		if got := Localize(RU, in); got != in {
			t.Errorf("Localize(RU, %q) changed it to %q", in, got)
		}
	}
}

func TestLocalizeError(t *testing.T) {
	withCatalog(t, map[string]string{"подключение %s не найдено": "connection %s not found"})
	err := fmt.Errorf("подключение %s не найдено", "x")
	if got := LocalizeError(EN, err); got != "connection x not found" {
		t.Errorf("LocalizeError = %q", got)
	}
	if got := LocalizeError(EN, nil); got != "" {
		t.Errorf("nil error = %q", got)
	}
}

func TestVerbs(t *testing.T) {
	got := Verbs("a %s b %q c %5.2f d %% e %w %v %d")
	want := []string{"%s", "%q", "%5.2f", "%w", "%v", "%d"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("Verbs = %v", got)
	}
}
