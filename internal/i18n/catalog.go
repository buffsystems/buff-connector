package i18n

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// catalog maps each Russian text the connector shows a person to its English
// line. The parts live beside each other by area (catalog_*.go); every Russian
// literal in the Go source has to be here — coverage_test.go walks the source
// and fails on one that is not, so a new message cannot ship in one language.
var catalog = map[string]string{}

func add(part map[string]string) {
	for k, v := range part {
		catalog[k] = v
	}
}

// T is the line in l: the Russian text itself, or its English one. A line
// with no English yet is shown in Russian rather than as nothing.
func (l Lang) T(ru string) string {
	if l == EN {
		if en, ok := catalog[ru]; ok {
			return en
		}
	}
	return ru
}

// Sprintf formats the line in l. Error arguments are localized too: an error
// written in Russian deep inside the connector reads in the caller's language.
func (l Lang) Sprintf(format string, args ...any) string {
	if l == EN {
		for i, a := range args {
			if err, ok := a.(error); ok {
				args[i] = Localize(l, err.Error())
			}
		}
	}
	return fmt.Sprintf(l.T(format), args...)
}

// T and Sprintf in the process's language.
func T(ru string) string                        { return current.T(ru) }
func Sprintf(format string, args ...any) string { return current.Sprintf(format, args...) }

// LocalizeError is Localize for an error, "" for none.
func LocalizeError(l Lang, err error) string {
	if err == nil {
		return ""
	}
	return Localize(l, err.Error())
}

// Localize translates a message that was already FORMATTED in Russian — an
// error built deep in the connector, which reaches a person through a page or
// the terminal. It matches the whole text against the catalogue, exactly or as
// a format (each verb a wildcard, each filled value localized in turn), and
// failing that translates around the first ": " — the shape a wrapped error
// takes. What it cannot match it leaves as it is.
func Localize(l Lang, text string) string {
	if l != EN || text == "" {
		return text
	}
	if en, ok := matchWhole(text); ok {
		return en
	}
	if i := strings.Index(text, ": "); i > 0 {
		head, tail := text[:i], text[i+2:]
		if en, ok := matchWhole(head); ok {
			head = en
		}
		return head + ": " + Localize(l, tail)
	}
	return text
}

func matchWhole(text string) (string, bool) {
	if en, ok := catalog[text]; ok {
		return en, true
	}
	for _, p := range compiledPatterns() {
		m := p.re.FindStringSubmatch(text)
		if m == nil {
			continue
		}
		vals := make([]string, len(m)-1)
		for i, v := range m[1:] {
			vals[i] = Localize(EN, v)
		}
		return fill(p.en, vals), true
	}
	return "", false
}

type pattern struct {
	re *regexp.Regexp
	en string
}

var (
	patternsOnce sync.Once
	patterns     []pattern
)

func resetPatterns() { patternsOnce = sync.Once{}; patterns = nil }

// compiledPatterns turns every catalogue key with a verb into an anchored
// expression, longest key first so the most specific format wins.
func compiledPatterns() []pattern {
	patternsOnce.Do(func() {
		var keys []string
		for k := range catalog {
			if len(Verbs(k)) > 0 {
				keys = append(keys, k)
			}
		}
		sort.Slice(keys, func(a, b int) bool {
			if len(keys[a]) != len(keys[b]) {
				return len(keys[a]) > len(keys[b])
			}
			return keys[a] < keys[b]
		})
		for _, k := range keys {
			var b strings.Builder
			b.WriteString(`(?s)^`)
			rest := k
			for {
				loc := verbRe.FindStringIndex(rest)
				if loc == nil {
					b.WriteString(regexp.QuoteMeta(strings.ReplaceAll(rest, "%%", "%")))
					break
				}
				b.WriteString(regexp.QuoteMeta(strings.ReplaceAll(rest[:loc[0]], "%%", "%")))
				if rest[loc[0]:loc[1]] == "%%" {
					b.WriteString("%")
				} else {
					b.WriteString(`(.+?)`)
				}
				rest = rest[loc[1]:]
			}
			b.WriteString(`$`)
			patterns = append(patterns, pattern{regexp.MustCompile(b.String()), catalog[k]})
		}
	})
	return patterns
}

// verbRe matches one fmt verb, or the escaped percent sign.
var verbRe = regexp.MustCompile(`%%|%[-+# 0]*[0-9]*(?:\.[0-9]+)?[a-zA-Z]`)

// Verbs lists the fmt verbs of a format in order, without the escaped "%%".
func Verbs(format string) []string {
	var out []string
	for _, v := range verbRe.FindAllString(format, -1) {
		if v != "%%" {
			out = append(out, v)
		}
	}
	return out
}

// fill puts already-formatted values in place of the verbs of an English line.
func fill(en string, vals []string) string {
	i := 0
	return verbRe.ReplaceAllStringFunc(en, func(v string) string {
		if v == "%%" {
			return "%"
		}
		if i >= len(vals) {
			return v
		}
		i++
		return vals[i-1]
	})
}
