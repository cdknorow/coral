package routes

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #5480 (Security's condition 1): a model that came from outside (a request, a saved setting, a workflow step, an operator's custom model) becomes a command-line argument only
// through agentmodels.ModelArgs, which refuses anything that is not a valid model id. So no code outside that package builds "--model" followed by a value that is not a string
// literal (a literal such as "haiku" is Coral's own); a sixth site cannot be added silently.
func TestNoCodeBuildsAModelArgumentButThroughModelArgs(t *testing.T) {
	t.Parallel() // no shared state: these ran one after another (#5622)
	isLit := func(e ast.Expr, s string) bool {
		l, ok := e.(*ast.BasicLit)
		return ok && l.Kind == token.STRING && strings.Trim(l.Value, "`\"") == s
	}
	isAnyLit := func(e ast.Expr) bool { l, ok := e.(*ast.BasicLit); return ok && l.Kind == token.STRING }
	check := func(t *testing.T, path string, elts []ast.Expr) {
		for i, e := range elts {
			if isLit(e, "--model") && i+1 < len(elts) && !isAnyLit(elts[i+1]) {
				t.Errorf("%s: \"--model\" is followed by a value that is not a literal: use agentmodels.ModelArgs", path)
			}
		}
	}
	n := 0
	for _, root := range []string{"../../../cmd", "../..", "../../../pkg"} {
		require.NoError(t, filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() && filepath.Base(path) == "agentmodels" {
				if err == nil {
					return filepath.SkipDir
				}
				return err
			}
			if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			n++
			src, perr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			require.NoError(t, perr, path)
			ast.Inspect(src, func(node ast.Node) bool {
				switch x := node.(type) {
				case *ast.CallExpr:
					if id, ok := x.Fun.(*ast.Ident); ok && id.Name == "append" {
						check(t, path, x.Args)
					}
				case *ast.CompositeLit:
					check(t, path, x.Elts)
				}
				return true
			})
			return nil
		}))
	}
	assert.Greater(t, n, 100, "the walk saw the code")
}
