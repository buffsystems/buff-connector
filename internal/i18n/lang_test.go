package i18n

import (
	"net/http"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestFromLocale(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want Lang
	}{
		{"nothing set is Russian", nil, RU},
		{"C locale is undetermined, so Russian", map[string]string{"LANG": "C.UTF-8"}, RU},
		{"POSIX too", map[string]string{"LC_ALL": "POSIX"}, RU},
		{"LANG English", map[string]string{"LANG": "en_US.UTF-8"}, EN},
		{"LANG Russian", map[string]string{"LANG": "ru_RU.UTF-8"}, RU},
		{"LC_MESSAGES beats LANG", map[string]string{"LC_MESSAGES": "en_GB.UTF-8", "LANG": "ru_RU.UTF-8"}, EN},
		{"LC_ALL beats LC_MESSAGES", map[string]string{"LC_ALL": "ru_RU.UTF-8", "LC_MESSAGES": "en_GB"}, RU},
		{"LANGUAGE beats everything", map[string]string{"LANGUAGE": "en", "LC_ALL": "ru_RU.UTF-8"}, EN},
		{"LANGUAGE is a list, the first ru/en wins", map[string]string{"LANGUAGE": "de:en_GB:ru"}, EN},
		{"a language we lack falls through to the next variable", map[string]string{"LANGUAGE": "de", "LANG": "en_US"}, EN},
		{"only languages we lack: Russian", map[string]string{"LANG": "de_DE.UTF-8"}, RU},
		{"modifier and codeset", map[string]string{"LANG": "en_US.ISO-8859-1@euro"}, EN},
	}
	for _, c := range cases {
		if got := FromLocale(env(c.env)); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

func TestFromEnvOverride(t *testing.T) {
	if got := FromEnv(env(map[string]string{"BUFF_LANG": "en", "LANG": "ru_RU.UTF-8"})); got != EN {
		t.Errorf("BUFF_LANG=en: got %s", got)
	}
	if got := FromEnv(env(map[string]string{"BUFF_LANG": "ru", "LANG": "en_US.UTF-8"})); got != RU {
		t.Errorf("BUFF_LANG=ru: got %s", got)
	}
	// A value we cannot use is ignored, not obeyed into a broken state.
	if got := FromEnv(env(map[string]string{"BUFF_LANG": "fr", "LANG": "en_US.UTF-8"})); got != EN {
		t.Errorf("BUFF_LANG=fr: got %s", got)
	}
}

func TestParse(t *testing.T) {
	for in, want := range map[string]Lang{"en": EN, "EN": EN, "en-GB": EN, "en_US.UTF-8": EN, "ru": RU, "ru-RU": RU} {
		if got, ok := Parse(in); !ok || got != want {
			t.Errorf("Parse(%q) = %s, %v", in, got, ok)
		}
	}
	for _, in := range []string{"", "C", "POSIX", "de", "english", "e"} {
		if _, ok := Parse(in); ok {
			t.Errorf("Parse(%q) should not pick a language", in)
		}
	}
}

func TestFromAcceptLanguage(t *testing.T) {
	cases := map[string]Lang{
		"":                             RU,
		"en-US,en;q=0.9":               EN,
		"ru-RU,ru;q=0.9,en;q=0.8":      RU,
		"de-DE,de;q=0.9,en;q=0.8":      EN,
		"de,fr":                        RU,
		"ru;q=0.5, en;q=0.9":           EN,
		"en;q=0, ru":                   RU,
		"fr-CH, fr;q=0.9, en;q=0.8, *": EN,
	}
	for h, want := range cases {
		if got := FromAcceptLanguage(h); got != want {
			t.Errorf("FromAcceptLanguage(%q) = %s, want %s", h, got, want)
		}
	}
}

func TestForRequest(t *testing.T) {
	r, _ := http.NewRequest("GET", "/", nil)
	r.Header.Set("Accept-Language", "en-US")
	if got := ForRequest(r); got != EN {
		t.Errorf("Accept-Language en: got %s", got)
	}
	// The page's own switch travels as a cookie and outranks the browser.
	r.AddCookie(&http.Cookie{Name: CookieName, Value: "ru"})
	if got := ForRequest(r); got != RU {
		t.Errorf("cookie ru over Accept-Language en: got %s", got)
	}
	bare, _ := http.NewRequest("GET", "/", nil)
	if got := ForRequest(bare); got != RU {
		t.Errorf("nothing said: got %s", got)
	}
}
