package coralnames

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// #6891: the names of Coral's branches and worktree folders are built in this package and nowhere else. A string literal that names one (coral-team/, coral/job-run-, job-run-, _task_run_), or the worktrees
// folder joined into a path, in any other non-test Go file fails this, with its place: use the function here.
func TestNoNameOfCoralsIsBuiltElsewhere(t *testing.T) {
	fset := token.NewFileSet()
	names := []string{"coral-team/", "coral/job-run-", "job-run-", "_task_run_"}
	scanned := 0
	for _, root := range []string{"..", "../../cmd", "../../pkg"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			if strings.Contains(filepath.ToSlash(path), "/coralnames/") {
				return nil
			}
			f, perr := parser.ParseFile(fset, path, nil, 0)
			if perr != nil {
				return nil
			}
			scanned++
			ast.Inspect(f, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.BasicLit:
					if x.Kind != token.STRING {
						return true
					}
					v, _ := strconv.Unquote(x.Value)
					for _, name := range names {
						if strings.Contains(v, name) {
							t.Errorf("%s: the literal %s names one of Coral's branches or worktree folders: build it with the coralnames package", fset.Position(x.Pos()), x.Value)
						}
					}
				case *ast.CallExpr:
					if sel, ok := x.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Join" {
						for _, a := range x.Args {
							if lit, ok := a.(*ast.BasicLit); ok && lit.Kind == token.STRING && lit.Value == `"worktrees"` {
								t.Errorf("%s: the worktrees folder is joined here: use coralnames.WorktreesRoot", fset.Position(lit.Pos()))
							}
						}
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if scanned < 100 {
		t.Fatalf("scanned only %d files: the census is looking in the wrong place", scanned)
	}
}
