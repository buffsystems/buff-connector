package i18n

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

// The flag package's own complaints are the terminal's reading too.
func TestFlagErrorsSpeakTheLanguage(t *testing.T) {
	for in, want := range map[string]string{
		"flag provided but not defined: -nope":            "флаг -nope не предусмотрен",
		"flag needs an argument: -config":                 "флагу -config нужно значение",
		`invalid value "x" for flag -n: parse error`:      `неверное значение "x" у флага -n: parse error`,
		`invalid boolean value "x" for -cli: parse error`: `неверное логическое значение "x" у флага -cli: parse error`,
		"bad flag syntax: ---x":                           "неверная запись флага: ---x",
		"something the flag package will say one day":     "something the flag package will say one day",
	} {
		if got := flagError(RU, in); got != want {
			t.Errorf("flagError(RU, %q) = %q, want %q", in, got, want)
		}
	}
	if got := flagError(EN, "flag needs an argument: -config"); got != "flag -config needs a value" {
		t.Errorf("flagError(EN) = %q", got)
	}
}

func TestFlagUsageSpeaksTheLanguage(t *testing.T) {
	fs := Flags("pair")
	fs.String("config", "", "путь к YAML-конфигу")
	fs.String("addr", "127.0.0.1:8765", "локальный адрес страницы")
	fs.Bool("cli", false, "мастер в терминале")
	fs.Duration("wait", time.Second, "сколько ждать")

	var ru, en bytes.Buffer
	printUsage(RU, &ru, fs)
	printUsage(EN, &en, fs)
	for _, want := range []string{"использование pair:", "-config текст", "-cli\n", "(по умолчанию 127.0.0.1:8765)", "-wait длительность"} {
		if !strings.Contains(ru.String(), want) {
			t.Errorf("ru usage lacks %q:\n%s", want, ru.String())
		}
	}
	for _, want := range []string{"usage of pair:", "-config text", "(default 127.0.0.1:8765)"} {
		if !strings.Contains(en.String(), want) {
			t.Errorf("en usage lacks %q:\n%s", want, en.String())
		}
	}
}

// The printers translate the line and an error among the arguments.
func TestFprintfSpeaksTheLanguage(t *testing.T) {
	withCatalog(t, map[string]string{"сохранено: %v": "saved: %v", "нет места": "no room"})
	saved := current
	t.Cleanup(func() { current = saved })
	current = EN
	var b bytes.Buffer
	Fprintf(&b, "сохранено: %v", errors.New("нет места"))
	if b.String() != "saved: no room" {
		t.Errorf("Fprintf = %q", b.String())
	}
}
