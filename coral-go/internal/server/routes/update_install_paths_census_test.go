package routes

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #6290: nothing installs an update without a person's yes. The ways to start an install are counted here, over every Go file that is not a test: the program run with the
// word apply, the server's Updater.Apply (the relaunch) and the Windows trigger's Launch (the "Install update" route). Each may be called from one place, the handler that
// takes the person's yes, so an idle, on-quit or at-start install cannot be added without this test failing and a reviewer reading the new site.
func TestOnlyTheTwoRoutesThatTakeAYesCanStartAnInstall(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	var found []string
	fset := token.NewFileSet()
	require.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "node_modules" || d.Name() == ".git") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		require.NoError(t, err)
		if !strings.Contains(string(raw), "apply") && !strings.Contains(string(raw), "Apply") && !strings.Contains(string(raw), "Launch") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, raw, 0)
		if err != nil {
			return nil // a file for another build the parser cannot read is not a start
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch {
			case isExecCall(sel):
				for _, a := range call.Args {
					if lit, ok := a.(*ast.BasicLit); ok && lit.Value == `"apply"` {
						found = append(found, rel+" exec apply")
					}
				}
			case sel.Sel.Name == "Apply" && selectsField(sel.X, "Updater"):
				found = append(found, rel+" Updater.Apply")
			case sel.Sel.Name == "Launch" && isIdent(sel.X, "trigger"):
				found = append(found, rel+" trigger.Launch")
			}
			return true
		})
		return nil
	}))
	sort.Strings(found)
	assert.Equal(t, []string{
		"internal/server/routes/relaunch_real.go exec apply",             // the relaunch's updater start, reached only through Updater.Apply below
		"internal/server/routes/system_update_install.go trigger.Launch", // the Windows "Install update": Operator only, local only, a fresh POST
		"internal/server/routes/system_update_relaunch.go Updater.Apply", // the relaunch: an acknowledgement token the person's click fetched, a person or the Hub's owner
		"internal/updater/trigger/launch_windows.go exec apply",          // trigger.Launch's own start of the program
	}, found, "a new way to start an install: it must take a person's yes first, and this list and the audit (#6290) change with it")
}

func isExecCall(sel *ast.SelectorExpr) bool {
	return isIdent(sel.X, "exec") && (sel.Sel.Name == "Command" || sel.Sel.Name == "CommandContext")
}

func isIdent(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}

func selectsField(e ast.Expr, name string) bool {
	s, ok := e.(*ast.SelectorExpr)
	return ok && s.Sel.Name == name
}
