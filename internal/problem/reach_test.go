package problem

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
)

// lastErrorPath is every function an error passes through on its way to a
// connection's last error — from the config check and the start of an
// instance to the supervisor that restarts it. An error built in one of them
// is the connector's own text; it has to be coded, or the platform would get
// it as a raw line in Russian. Keyed by file, from the module root.
var lastErrorPath = map[string][]string{
	"internal/config/config.go":     {"InstanceProblems"},
	"internal/agent/plugins.go":     {"Sync", "start", "For"},
	"internal/agent/mcpcustom.go":   {"startCustom", "startRemote"},
	"internal/mcpsource/expand.go":  {"Expand"},
	"internal/mcpsource/install.go": {"ResolveCommand"},
	"internal/plugin/store.go":      {"Resolve", "List", "open", "CheckIntegrity", "treeSum"},
	"internal/plugin/schema.go":     {"Validate"},
	"internal/plugin/manifest.go":   {"ValidateConfig", "ResolveEgress", "resolveWeb"},
	"internal/plugin/web.go":        {"ParseDomainPattern", "NewWebPolicy", "parsePatterns"},
	"internal/plugin/proc.go":       {"Start", "PrepareInstanceDir"},
	"internal/plugin/priv_unix.go":  {"CheckPrivilege", "chownDir"},
	"internal/plugin/host.go":       {"Start", "supervise", "attempt", "open", "listTools", "setState"},
	"internal/plugin/rpc.go":        {"read", "fail", "Notify", "Call", "call"},
	"internal/plugin/remote.go":     {"Notify", "Call", "post", "readStream", "fail"},
}

// uncoded are the calls that make an error out of a text.
var uncoded = map[string]map[string]bool{
	"fmt":    {"Errorf": true},
	"errors": {"New": true},
}

func TestEveryErrorOnTheWayToLastErrorIsCoded(t *testing.T) {
	for file, funcs := range lastErrorPath {
		want := map[string]bool{}
		for _, f := range funcs {
			want[f] = true
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, filepath.Join("..", "..", file), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || !want[fn.Name.Name] {
				continue
			}
			delete(want, fn.Name.Name)
			ast.Inspect(fn, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if pkg, ok := sel.X.(*ast.Ident); ok && uncoded[pkg.Name][sel.Sel.Name] {
					t.Errorf("%s: %s builds an uncoded error — use problem.New or problem.Wrap",
						fset.Position(call.Pos()), fn.Name.Name)
				}
				return true
			})
		}
		// A function renamed or moved must not silently leave the list.
		for name := range want {
			t.Errorf("%s: no function %s — update lastErrorPath", file, name)
		}
	}
}
