package admin

import (
	"regexp"
	"testing"
)

// An inline handler is an HTML attribute in double quotes. A raw JSON string
// spliced into it brings its own double quote, the attribute ends there, and
// the browser drops the handler with a syntax error — the credential's kind
// could not be switched and «Войти» did nothing (autonomous-dev/cloud-front#475).
// The value goes through esc(), which turns the quote into &quot;.
func TestInlineHandlersDoNotSpliceRawJSONIntoTheAttribute(t *testing.T) {
	raw := regexp.MustCompile(`on[a-z]+="[^"]*\$\{JSON\.stringify`)
	for _, m := range raw.FindAllString(string(indexHTML), -1) {
		t.Errorf("a raw JSON string in an inline handler breaks the attribute: %s", m)
	}
}

// The page is one script: a function declared twice silently replaces the first
// everywhere. A second plural() printed «63 63 символа» (cloud-front#475).
func TestPageDeclaresEachFunctionOnce(t *testing.T) {
	seen := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^\s*(?:async\s+)?function\s+([A-Za-z_$][\w$]*)\s*\(`).FindAllStringSubmatch(string(indexHTML), -1) {
		if seen[m[1]] {
			t.Errorf("function %s is declared more than once", m[1])
		}
		seen[m[1]] = true
	}
}
