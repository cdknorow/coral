package routes

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// #3633: a path is resolved through internal/safefs (Resolve, or realPath here, which uses it),
// never with filepath.EvalSymlinks directly: on Windows EvalSymlinks doesn't follow junctions,
// so a junction inside a repository could lead a containment check outside it.
func TestRoutesNeverCallEvalSymlinks(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		af, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(af, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "EvalSymlinks" {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "filepath" {
				found = append(found, fset.Position(sel.Pos()).String())
			}
			return true
		})
	}
	sort.Strings(found)
	if len(found) > 0 {
		t.Errorf("filepath.EvalSymlinks in routes (use safefs.Resolve, which follows Windows junctions too):\n%s", strings.Join(found, "\n"))
	}
}
