package main

import (
	"reflect"
	"testing"

	"github.com/buffsystems/buff-connector/internal/i18n"
)

// --lang may stand anywhere on the line — before the command or among its
// flags — and is taken out before the command parses its own flags, which
// would otherwise refuse it as unknown.
func TestTakeLang(t *testing.T) {
	cases := []struct {
		in   []string
		rest []string
		lang i18n.Lang
		set  bool
	}{
		{[]string{"catalog"}, []string{"catalog"}, "", false},
		{[]string{"--lang", "en", "catalog"}, []string{"catalog"}, i18n.EN, true},
		{[]string{"catalog", "show", "jira", "--lang=ru", "--config", "c.yaml"}, []string{"catalog", "show", "jira", "--config", "c.yaml"}, i18n.RU, true},
		{[]string{"instance", "list", "-lang", "en"}, []string{"instance", "list"}, i18n.EN, true},
	}
	for _, c := range cases {
		rest, lang, set, err := takeLang(c.in)
		if err != nil || set != c.set || lang != c.lang || !reflect.DeepEqual(rest, c.rest) {
			t.Errorf("takeLang(%q) = %q, %q, %v, %v", c.in, rest, lang, set, err)
		}
	}
	for _, bad := range [][]string{{"--lang", "fr"}, {"run", "--lang"}, {"--lang="}} {
		if _, _, _, err := takeLang(bad); err == nil {
			t.Errorf("takeLang(%q) should refuse", bad)
		}
	}
}
