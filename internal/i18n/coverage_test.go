package i18n

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

// The source tree this test reads, from internal/i18n.
const repoRoot = "../.."

// covered is where every Russian string literal must have its English line:
// the code a person reads the output of.
var covered = []string{"cmd", "internal"}

// modelFacing are files or declarations whose Russian is not the catalogue's to
// carry. Texts written FOR THE MODEL used to be here; they are in both languages
// now (#565) — tool descriptions in files per language, results and errors in the
// catalogue — so nothing is. Keyed by file, then by top-level declaration ("*"
// for the whole file).
var modelFacing = map[string]map[string]string{}

// keptForData are lines the source does not spell out: values that arrive as
// data (a catalogue category) and are translated where they are shown.
var keptForData = map[string]bool{}

func hasCyrillic(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Cyrillic, r) {
			return true
		}
	}
	return false
}

// literal folds a string literal, or a "+" chain of them, into its value.
func literal(e ast.Expr) (string, bool) {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(v.Value)
		return s, err == nil
	case *ast.BinaryExpr:
		if v.Op != token.ADD {
			return "", false
		}
		a, ok1 := literal(v.X)
		b, ok2 := literal(v.Y)
		return a + b, ok1 && ok2
	case *ast.ParenExpr:
		return literal(v.X)
	}
	return "", false
}

type found struct {
	text, where string
}

// russianLiterals lists every Russian string in one file, with the top-level
// declaration it sits in.
func russianLiterals(t *testing.T, path string) map[string][]found {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]found{}
	for _, d := range f.Decls {
		name := declName(d)
		ast.Inspect(d, func(n ast.Node) bool {
			e, ok := n.(ast.Expr)
			if !ok {
				return true
			}
			s, ok := literal(e)
			if !ok {
				return true
			}
			if hasCyrillic(s) {
				out[name] = append(out[name], found{s, fset.Position(e.Pos()).String()})
			}
			return false
		})
	}
	return out
}

func declName(d ast.Decl) string {
	switch v := d.(type) {
	case *ast.FuncDecl:
		return v.Name.Name
	case *ast.GenDecl:
		for _, s := range v.Specs {
			switch sp := s.(type) {
			case *ast.ValueSpec:
				return sp.Names[0].Name
			case *ast.TypeSpec:
				return sp.Name.Name
			}
		}
	}
	return ""
}

func goFiles(t *testing.T, rel string) []string {
	t.Helper()
	root := filepath.Join(repoRoot, rel)
	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		return []string{rel}
	}
	var out []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") {
			r, _ := filepath.Rel(repoRoot, p)
			out = append(out, filepath.ToSlash(r))
		}
		return nil
	})
	return out
}

// Every Russian text the connector shows a person has its English line, and
// every line in the catalogue is still used somewhere.
func TestEveryRussianLiteralHasEnglish(t *testing.T) {
	used := map[string]bool{}
	defer func() {
		if t.Failed() {
			return
		}
		for ru := range catalog {
			if !used[ru] && !keptForData[ru] {
				t.Errorf("catalogue line no longer used in the source: %q", ru)
			}
		}
	}()
	for _, rel := range covered {
		for _, file := range goFiles(t, rel) {
			skip := modelFacing[file]
			if skip["*"] != "" || isCatalogue(file) {
				continue
			}
			for decl, lits := range russianLiterals(t, filepath.Join(repoRoot, file)) {
				if skip[decl] != "" {
					continue
				}
				for _, l := range lits {
					used[l.text] = true
					if _, ok := catalog[l.text]; !ok {
						t.Errorf("%s: no English for %q", l.where, l.text)
					}
				}
			}
		}
	}
}

// An English line keeps the verbs of its Russian one, in order — the same
// arguments fill both — and is English.
func TestCatalogLinesMatch(t *testing.T) {
	for ru, en := range catalog {
		if !reflect.DeepEqual(Verbs(ru), Verbs(en)) {
			t.Errorf("verbs differ:\n  %q %v\n  %q %v", ru, Verbs(ru), en, Verbs(en))
		}
		if hasCyrillic(en) {
			t.Errorf("English line is not English: %q", en)
		}
		if strings.TrimSpace(en) == "" {
			t.Errorf("empty English for %q", ru)
		}
	}
}

// isCatalogue is a catalogue part: its keys are the lines themselves, so
// reading them as uses would keep a line nobody shows any more.
func isCatalogue(file string) bool {
	return strings.HasPrefix(file, "internal/i18n/catalog")
}
