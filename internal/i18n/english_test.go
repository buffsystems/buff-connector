package i18n

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The other half of the coverage: a text written ONLY in English. The catalogue
// is keyed by the Russian line, so a person in Russian mode would read such a
// text in English — the source has to be Russian and its English a catalogue
// line. What counts as a text a person reads is where it goes: an error, a line
// printed or logged (an operator reads the log in a terminal), an HTTP error
// body, a flag's help, a message field of a JSON answer.

// sinks name the calls whose string arguments a person reads, by package.
var sinks = map[string]map[string]bool{
	"fmt": {"Errorf": true, "Sprintf": true, "Print": true, "Printf": true, "Println": true,
		"Fprint": true, "Fprintf": true, "Fprintln": true},
	"errors": {"New": true},
	"log": {"Print": true, "Printf": true, "Println": true,
		"Fatal": true, "Fatalf": true, "Fatalln": true},
	"http": {"Error": true},
	// A line handed to the catalogue is Russian; English here is a line the
	// catalogue cannot translate.
	"i18n": {"T": true, "Sprintf": true, "Logf": true, "Fatalf": true,
		"Printf": true, "Fprintf": true},
}

// localSinks are the command line's own printers (cmd/connector: say,
// fatal), and translated are the methods every catalogue lookup goes
// through (i18n.T, a Lang's T and Sprintf) — whatever the receiver.
var (
	localSinks = map[string]bool{"say": true, "fatal": true}
	translated = map[string]bool{"T": true, "Sprintf": true}
)

// flagDefs are FlagSet methods whose third argument is the flag's help.
var flagDefs = map[string]bool{
	"String": true, "Bool": true, "Int": true, "Int64": true, "Uint": true,
	"Duration": true, "Float64": true, "StringVar": true, "BoolVar": true,
	"IntVar": true, "DurationVar": true,
}

// messageKeys are JSON fields that carry a message to a person.
var messageKeys = map[string]bool{
	"error": true, "detail": true, "note": true, "reason": true, "message": true,
}

// wordRe is a run of Latin letters long enough to be a word of prose.
var wordRe = regexp.MustCompile(`[A-Za-z]{2,}`)

// notProse are Latin words that are names, not language: what the connector
// is called and the things a person types or sees spelled the same in either
// language. A text made of them and punctuation alone is not English.
var notProse = map[string]bool{
	"Buff": true, "MCP": true, "OAuth": true, "oauth": true, "JSON": true, "json": true,
	"YAML": true, "URL": true, "HTTP": true, "HTTPS": true, "API": true, "ID": true,
	"id": true, "PR": true, "IDE": true, "Git": true, "git": true, "OK": true, "ok": true,
}

// isEnglishProse reports whether s is a sentence or phrase in English: no
// Cyrillic, and some Latin word outside fmt verbs, flags, paths and the names
// above.
func isEnglishProse(s string) bool {
	if hasCyrillic(s) {
		return false
	}
	// One word with a verb or a separator in it is a name being built — a
	// file, a key — not a phrase.
	if !strings.ContainsAny(s, " \n") && strings.ContainsAny(s, "%.-_") {
		return false
	}
	s = verbRe.ReplaceAllString(s, " ")
	for _, w := range strings.Fields(s) {
		// --flag, a path, an address, an identifier with an underscore, a
		// config key — typed by a person, spelled the same in both languages.
		if strings.HasPrefix(w, "-") || strings.ContainsAny(w, "/._=<>`$@") {
			continue
		}
		for _, word := range wordRe.FindAllString(w, -1) {
			if !notProse[word] {
				return true
			}
		}
	}
	return false
}

// notForAPerson are the places whose English texts go to a machine or to the
// model, not to a person, keyed by file, then by top-level declaration ("*"
// for the whole file), each with the reason. Like modelFacing, they are not
// the operator's reading.
var notForAPerson = map[string]map[string]string{
	// The host API a plugin calls (buff/http, buff/credential, …): its errors
	// answer the plugin process, which hands them to the model as a tool result.
	"internal/plugin/http.go": {"*": "answers to the plugin process (host API)"},
	"internal/plugin/web.go": {
		"Allows":           "answers a plugin's web request (host API)",
		"AllowsType":       "answers a plugin's web request (host API)",
		"publicAddressFor": "answers a plugin's web request (host API)",
	},
	// A tool call into a running instance: the answer goes back to the run, to
	// the model. The language of tool texts is the model-facing slice's (#718).
	"internal/plugin/host.go": {
		"CallTool": "tool call answers, for the model",
		"Call":     "tool call answers, for the model",
	},
	"internal/agent/mcp.go": {
		"Handle": "an answer to the platform's relay about an unknown method",
		"call":   "an answer to the platform's relay about a malformed call",
		"invoke": "tool call answers, for the model",
	},
	// A webhook source (Jira, GitLab, …) gets a status and a word; nobody
	// reads it but the source's delivery log, which is the source's, not ours.
	"internal/agent/webhook.go": {"webhookHandler": "HTTP replies to a webhook source"},
	// Protocol: a server-sent events frame and a change fingerprint.
	"internal/devgate/elicit.go": {"send": "the server-sent events frame format"},
	"internal/config/mcp.go":     {"fingerprint": "a hash input, never shown"},
}

// englishLiterals lists the English texts one file hands a person.
func englishLiterals(t *testing.T, path string) map[string][]found {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]found{}
	for _, d := range f.Decls {
		name := declName(d)
		take := func(e ast.Expr) {
			if s, ok := literal(e); ok && isEnglishProse(s) {
				out[name] = append(out[name], found{s, fset.Position(e.Pos()).String()})
			}
		}
		ast.Inspect(d, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.CallExpr:
				if id, ok := v.Fun.(*ast.Ident); ok && localSinks[id.Name] {
					for _, a := range v.Args {
						take(a)
					}
				}
				sel, ok := v.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkg, isPkg := sel.X.(*ast.Ident)
				if (isPkg && sinks[pkg.Name][sel.Sel.Name]) ||
					translated[sel.Sel.Name] {
					for _, a := range v.Args {
						take(a)
					}
				}
				if flagDefs[sel.Sel.Name] && len(v.Args) == 3 {
					if _, isName := literal(v.Args[0]); isName {
						take(v.Args[2])
					}
				}
			case *ast.KeyValueExpr:
				if k, ok := literal(v.Key); ok && messageKeys[k] {
					take(v.Value)
				}
			}
			return true
		})
	}
	return out
}

// Every text the connector hands a person is written in Russian, so that its
// English is a catalogue line and Russian mode reads Russian.
func TestNoEnglishOnlyText(t *testing.T) {
	for _, rel := range covered {
		for _, file := range goFiles(t, rel) {
			skip, kept := modelFacing[file], notForAPerson[file]
			if skip["*"] != "" || kept["*"] != "" {
				continue
			}
			for decl, lits := range englishLiterals(t, filepath.Join(repoRoot, file)) {
				if skip[decl] != "" || kept[decl] != "" {
					continue
				}
				for _, l := range lits {
					t.Errorf("%s: English-only text %q — write it in Russian, its English in the catalogue", l.where, l.text)
				}
			}
		}
	}
}

func TestIsEnglishProse(t *testing.T) {
	for s, want := range map[string]bool{
		"bad request":                 true,
		"config: %v":                  true,
		"нет подключения %q":          false,
		"%s: %v":                      false,
		"--config <path>":             false,
		"Buff MCP: %s":                false,
		"usage:":                      true,
		"  connector run --config %s": true,
		"127.0.0.1:8765":              false,
		"%s (%s)":                     false,
		"v%s":                         false,
		"provider-%d.token":           false,
		// Protocol words look like prose; only an exemption can say otherwise.
		"event: message\ndata: %s\n\n": true,
	} {
		if got := isEnglishProse(s); got != want {
			t.Errorf("isEnglishProse(%q) = %v, want %v", s, got, want)
		}
	}
}
