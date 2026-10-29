package admin

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"net/http"

	"github.com/buffsystems/buff-connector/internal/i18n"
)

// The page's server-side texts — errors, check results, a section's note —
// follow the language of the request (the page's switch cookie, then the
// browser's Accept-Language). Most are written in Russian far from any request,
// deep in the connector, so they are translated once, here, on the way out.

// The page itself is written in Russian; its English is page.en.json, keyed by
// the Russian text, which the server puts into the page it serves (the page's
// script reads it from there — no second request, nothing to load from outside).
//
//go:embed page.en.json
var pageEN []byte

// pageHTML is index.html with the English catalogue in place of its marker.
var pageHTML = withCatalogue(indexHTML, pageEN)

func withCatalogue(page, catalogue []byte) []byte {
	var lines map[string]string
	if err := json.Unmarshal(catalogue, &lines); err != nil {
		panic("admin: page.en.json: " + err.Error())
	}
	// json.Marshal escapes <, > and &, so nothing in a line can close the script.
	b, _ := json.Marshal(lines)
	tag := append(append([]byte(`<script type="application/json" id="i18n-en">`), b...), "</script>"...)
	return bytes.Replace(page, []byte("<!-- i18n:en -->"), tag, 1)
}

// langWriter carries the request's language to writeJSON, which every handler
// already calls, so no handler has to pass it along.
type langWriter struct {
	http.ResponseWriter
	lang i18n.Lang
}

func withLang(w http.ResponseWriter, r *http.Request) http.ResponseWriter {
	return &langWriter{ResponseWriter: w, lang: i18n.ForRequest(r)}
}

func langOf(w http.ResponseWriter) i18n.Lang {
	if lw, ok := w.(*langWriter); ok {
		return lw.lang
	}
	return i18n.RU
}

// shownFields name the values that are a message to a person. Everything else —
// names, ids, settings, the operator's own notes — is data and stays as it is.
var shownFields = map[string]bool{
	"error": true, "detail": true, "note": true, "reason": true,
	"last_error": true, "needs": true, "egress": true,
}

// localizeJSON returns v as the JSON it encodes to, with the shown fields
// translated at any depth.
func localizeJSON(l i18n.Lang, v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber() // a big integer must come out as it went in
	var tree any
	if dec.Decode(&tree) != nil {
		return v
	}
	return walkShown(l, tree, false)
}

func walkShown(l i18n.Lang, v any, shown bool) any {
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			x[k] = walkShown(l, child, shownFields[k])
		}
	case []any:
		for i, child := range x {
			x[i] = walkShown(l, child, shown)
		}
	case string:
		if shown {
			return i18n.Localize(l, x)
		}
	}
	return v
}
