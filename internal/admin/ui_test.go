package admin

import (
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// The page's look is the design system's plain CSS (#561), its typeface included, embedded
// and served like the mark: without a credential, or the 401 page would lose its look too.
func TestDesignSystemCSSIsServedWithoutACredential(t *testing.T) {
	_, srv := newAuthed(t)

	for path, want := range map[string]string{
		"/ui/ui.css":                     "@import './components.css'",
		"/ui/tokens.css":                 "--primary:",
		"/ui/components/button.css":      ".ui-btn",
		"/ui/fonts/Geist-Variable.woff2": "",
	} {
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("get %s: %v", path, err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", path, res.StatusCode)
		}
		if want != "" && !strings.Contains(string(body), want) {
			t.Errorf("%s: no %q in the body", path, want)
		}
	}
}

// The page takes its look from the design system, not from a copy of it: it links the
// served stylesheet and restates no typeface of its own (the old /fonts/ route is gone).
func TestPageTakesItsLookFromTheDesignSystem(t *testing.T) {
	page := string(indexHTML)
	if !strings.Contains(page, `href="/ui/ui.css"`) {
		t.Error("the page does not load the design system's stylesheet")
	}
	for _, copy := range []string{"@font-face", "/fonts/"} {
		if strings.Contains(page, copy) {
			t.Errorf("the page restates the typeface itself: %q", copy)
		}
	}
}

// The apps' light / dark / system switch, on the same contract: the stored choice under
// localStorage 'theme' painted before the first frame, and a labelled menu of the three.
func TestPageCarriesTheAppsThemeSwitch(t *testing.T) {
	page := string(indexHTML)
	for _, want := range []string{
		`localStorage.getItem("theme")`,
		`classList.add("dark")`,
		`aria-label="Переключить тему"`,
		`data-theme-value="light"`, `data-theme-value="dark"`, `data-theme-value="system"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the theme switch lacks %s", want)
		}
	}
	if strings.Index(page, `localStorage.getItem("theme")`) > strings.Index(page, `href="/ui/ui.css"`) {
		t.Error("the stored theme is painted after the stylesheet loads: the page would flash")
	}
}

// What the page styles itself is layout and the few pieces the catalogue lacks, written on
// the design system's tokens: a raw colour there is a restated token drifting from the app's.
func TestPageStyleIsWrittenOnTheTokens(t *testing.T) {
	page := string(indexHTML)
	start, end := strings.Index(page, "<style>"), strings.Index(page, "</style>")
	if start < 0 || end < start {
		t.Fatal("the page has no <style> block")
	}
	for _, hex := range regexp.MustCompile(`#[0-9a-fA-F]{3,8}\b`).FindAllString(page[start:end], -1) {
		t.Errorf("a raw colour in the page's own style: %s — use a token", hex)
	}
}
