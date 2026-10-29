package admin

import (
	"encoding/json"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"unicode"
)

// The local page is written in Russian; page.en.json is its English, keyed by
// the Russian text. These tests read the page the way its script does — t("…")
// for what the script writes, the text nodes and the placeholder / title /
// aria-label / data-tip attributes for the static markup — and hold the two
// together: every Russian line has its English, and no English is left over.

func cyrillic(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Cyrillic, r) {
			return true
		}
	}
	return false
}

var (
	scriptRe   = regexp.MustCompile(`(?s)<script[^>]*>(.*?)</script>`)
	styleRe    = regexp.MustCompile(`(?s)<style[^>]*>.*?</style>`)
	commentRe  = regexp.MustCompile(`(?s)<!--.*?-->`)
	tCallRe    = regexp.MustCompile(`\bt\(\s*"((?:[^"\\]|\\.)*)"`)
	tokenRe    = regexp.MustCompile(`<[^>]+>|[^<]+`)
	attrRe     = regexp.MustCompile(`\b(placeholder|title|aria-label|data-tip)="([^"]*)"`)
	spaceRe    = regexp.MustCompile(`\s+`)
	placeRe    = regexp.MustCompile(`\{\d+\}`)
	inlineTag  = regexp.MustCompile(`<[^>]+>`)
	pageEnFile = "page.en.json"
)

func norm(s string) string { return strings.TrimSpace(spaceRe.ReplaceAllString(s, " ")) }

// pageLines lists the page's Russian lines, each with where it was found.
func pageLines(t *testing.T) map[string]string {
	t.Helper()
	page := string(indexHTML)
	lines := map[string]string{}

	for _, m := range scriptRe.FindAllStringSubmatch(page, -1) {
		script := m[1]
		covered := make([]bool, len(script))
		for _, loc := range tCallRe.FindAllStringSubmatchIndex(script, -1) {
			var key string
			if err := json.Unmarshal([]byte(`"`+script[loc[2]:loc[3]]+`"`), &key); err != nil {
				t.Errorf("t() literal %q is not a plain string: %v", script[loc[2]:loc[3]], err)
				continue
			}
			lines[key] = "script"
			for i := loc[2]; i < loc[3]; i++ {
				covered[i] = true
			}
		}
		// Every Russian character the script holds is inside a t("…"), but for
		// a comment line and a line marked as data.
		at := 0
		for _, line := range strings.SplitAfter(script, "\n") {
			skip := strings.HasPrefix(strings.TrimSpace(line), "//") || strings.Contains(line, "// i18n: data")
			for i, r := range line {
				if !skip && unicode.Is(unicode.Cyrillic, r) && !covered[at+i] {
					t.Errorf("Russian outside t() in the script: %s", strings.TrimSpace(line))
					break
				}
			}
			at += len(line)
		}
	}

	// Comments first: one of them mentions <style>, which would otherwise
	// open a match that swallows the markup up to the real </style>.
	markup := styleRe.ReplaceAllString(scriptRe.ReplaceAllString(commentRe.ReplaceAllString(page, ""), ""), "")
	lastTag := ""
	for _, tok := range tokenRe.FindAllString(markup, -1) {
		if strings.HasPrefix(tok, "<") {
			for _, a := range attrRe.FindAllStringSubmatch(tok, -1) {
				if v := norm(html.UnescapeString(a[2])); cyrillic(v) {
					lines[v] = "markup attribute"
				}
			}
			if !strings.HasPrefix(tok, "</") {
				lastTag = tok
			}
			continue
		}
		// A language named in itself (lang="ru" on its element) keeps its words.
		if strings.Contains(lastTag, ` lang="`) {
			continue
		}
		if v := norm(html.UnescapeString(tok)); cyrillic(v) {
			lines[v] = "markup"
		}
	}
	return lines
}

func pageEnglish(t *testing.T) map[string]string {
	t.Helper()
	var en map[string]string
	if err := json.Unmarshal(pageEN, &en); err != nil {
		t.Fatalf("%s: %v", pageEnFile, err)
	}
	return en
}

func TestEveryPageLineHasEnglish(t *testing.T) {
	en := pageEnglish(t)
	lines := pageLines(t)
	var missing []string
	for ru, where := range lines {
		if _, ok := en[ru]; !ok {
			missing = append(missing, where+": "+ru)
		}
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Errorf("no English in %s for %s", pageEnFile, m)
	}
	for ru := range en {
		if _, ok := lines[ru]; !ok {
			t.Errorf("%s has a line the page no longer uses: %q", pageEnFile, ru)
		}
	}
}

// An English line keeps its Russian line's placeholders and markup, and is English.
func TestPageEnglishMatchesItsLine(t *testing.T) {
	for ru, en := range pageEnglish(t) {
		if cyrillic(en) || strings.TrimSpace(en) == "" {
			t.Errorf("not an English line for %q: %q", ru, en)
		}
		if !sameSet(placeRe.FindAllString(ru, -1), placeRe.FindAllString(en, -1)) {
			t.Errorf("placeholders differ:\n  %q\n  %q", ru, en)
		}
		if !sameSet(inlineTag.FindAllString(ru, -1), inlineTag.FindAllString(en, -1)) {
			t.Errorf("markup differs:\n  %q\n  %q", ru, en)
		}
	}
}

func sameSet(a, b []string) bool {
	sort.Strings(a)
	sort.Strings(b)
	return reflect.DeepEqual(a, b) || (len(a) == 0 && len(b) == 0)
}

// The server puts the English into the page it serves, where the script finds it.
func TestServedPageCarriesTheEnglish(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "c.yaml"), "test", nil)
	srv := httptest.NewServer(s.handler())
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL+"/", nil)
	req.Header.Set("X-Buff-Token", os.Getenv(tokenEnv))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body := new(strings.Builder)
	_, _ = io.Copy(body, res.Body)
	m := regexp.MustCompile(`(?s)<script type="application/json" id="i18n-en">(.*?)</script>`).FindStringSubmatch(body.String())
	if m == nil {
		t.Fatal("the served page carries no English catalogue")
	}
	var en map[string]string
	if err := json.Unmarshal([]byte(m[1]), &en); err != nil {
		t.Fatalf("catalogue in the page: %v", err)
	}
	if en["Обзор"] != "Overview" {
		t.Errorf("catalogue in the page: Обзор = %q", en["Обзор"])
	}
}
