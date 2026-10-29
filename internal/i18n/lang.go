// Package i18n is the connector's two languages: Russian, the language its
// texts are written in, and English, kept as a catalogue keyed by the Russian
// text (gettext-style, fmt verbs preserved). Russian is the default whenever
// nothing says otherwise.
package i18n

import (
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
)

// Lang is one of the two languages the connector speaks.
type Lang string

const (
	RU Lang = "ru"
	EN Lang = "en"
)

// CookieName carries the local page's language switch to the server, so the
// texts the server sends follow the page and not just the browser.
const CookieName = "buff_lang"

// Parse reads a language tag or a POSIX locale name — "en", "en-GB",
// "en_US.UTF-8@euro" — and reports whether it names one of ours.
func Parse(s string) (Lang, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if i := strings.IndexAny(s, "_-.@"); i >= 0 {
		s = s[:i]
	}
	switch s {
	case "ru":
		return RU, true
	case "en":
		return EN, true
	}
	return "", false
}

// FromLocale picks the language the way gettext reads the environment:
// LANGUAGE (a colon list), then LC_ALL, LC_MESSAGES, LANG. The first value that
// names Russian or English wins; anything else — C, POSIX, a language we do not
// have, nothing at all — leaves Russian.
func FromLocale(getenv func(string) string) Lang {
	var candidates []string
	candidates = append(candidates, strings.Split(getenv("LANGUAGE"), ":")...)
	candidates = append(candidates, getenv("LC_ALL"), getenv("LC_MESSAGES"), getenv("LANG"))
	for _, c := range candidates {
		if l, ok := Parse(c); ok {
			return l
		}
	}
	return RU
}

// FromEnv is FromLocale with the connector's own override, BUFF_LANG, on top.
func FromEnv(getenv func(string) string) Lang {
	if l, ok := Parse(getenv("BUFF_LANG")); ok {
		return l
	}
	return FromLocale(getenv)
}

// FromAcceptLanguage picks by the browser's preference order (q values),
// the first ru/en wins; Russian when the header names neither.
func FromAcceptLanguage(header string) Lang {
	type pref struct {
		lang Lang
		q    float64
		at   int
	}
	var prefs []pref
	for i, part := range strings.Split(header, ",") {
		tag, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		l, ok := Parse(tag)
		if !ok {
			continue
		}
		q := 1.0
		if v, found := strings.CutPrefix(strings.TrimSpace(params), "q="); found {
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				q = f
			}
		}
		if q > 0 {
			prefs = append(prefs, pref{l, q, i})
		}
	}
	if len(prefs) == 0 {
		return RU
	}
	sort.SliceStable(prefs, func(a, b int) bool { return prefs[a].q > prefs[b].q })
	return prefs[0].lang
}

// ForRequest is the language of one request to a local page: the page's own
// switch (a cookie) first, then the browser's Accept-Language.
func ForRequest(r *http.Request) Lang {
	if c, err := r.Cookie(CookieName); err == nil {
		if l, ok := Parse(c.Value); ok {
			return l
		}
	}
	return FromAcceptLanguage(r.Header.Get("Accept-Language"))
}

// current is the process's language: the terminal's, for the command line,
// the wizard and the log. Set once at start-up from the environment and,
// for the command line, from --lang.
var current = FromEnv(os.Getenv)

// Default is the process's language.
func Default() Lang { return current }

// SetDefault replaces the process's language (the --lang flag).
func SetDefault(l Lang) { current = l }
