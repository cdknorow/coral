package startup

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// #6767 (Security's LOW on #6769): an AgentLauncher that is not told which server it belongs to (SetServer) starts scheduled and one-shot agents with no address, so their hooks and coral-board dial
// :8420. Every function of the product that builds one with NewAgentLauncher must also call SetServer in the same function.
func TestEveryAgentLauncherIsToldItsServer(t *testing.T) {
	root := ".."
	fset := token.NewFileSet()
	built := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			var makes, tells bool
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch fun := call.Fun.(type) {
				case *ast.SelectorExpr:
					switch fun.Sel.Name {
					case "NewAgentLauncher":
						makes = true
					case "SetServer":
						tells = true
					}
				case *ast.Ident:
					if fun.Name == "NewAgentLauncher" {
						makes = true
					}
				}
				return true
			})
			if makes {
				built++
				if !tells {
					t.Errorf("%s: %s builds an AgentLauncher and never calls SetServer: its agents would dial the default port", path, fn.Name.Name)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if built == 0 {
		t.Fatal("found no NewAgentLauncher call: the census is looking in the wrong place")
	}
}
