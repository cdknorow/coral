package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// boardNameExpr reports whether e is a board name: st.Project (any .Project), or a variable
// named project, board, boardName or target, or *project.
func boardNameExpr(e ast.Expr) bool {
	switch t := e.(type) {
	case *ast.SelectorExpr:
		return t.Sel.Name == "Project"
	case *ast.Ident:
		switch t.Name {
		case "project", "board", "boardName", "target":
			return true
		}
	case *ast.StarExpr:
		return boardNameExpr(t.X)
	case *ast.ParenExpr:
		return boardNameExpr(t.X)
	}
	return false
}

func stringLit(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	return s, err == nil
}

// TestBoardNamesInPathsAreEscaped (#3640): a board name is one path segment, so it goes through
// url.PathEscape wherever coral-board puts it in a URL path; a '?' or '#' in it would otherwise
// send the request to another route. This lint fails on a board name concatenated after a "/"
// (or before one), or formatted by fmt.Sprintf right after a "/" in the format, unescaped.
func TestBoardNamesInPathsAreEscaped(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	checked := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		checked++
		ast.Inspect(file, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.BinaryExpr:
				if x.Op != token.ADD {
					return true
				}
				if s, ok := stringLit(x.X); ok && strings.HasSuffix(s, "/") && boardNameExpr(x.Y) {
					t.Errorf("%s: a board name is concatenated into a path unescaped (use url.PathEscape)", fset.Position(x.Pos()))
				}
				if s, ok := stringLit(x.Y); ok && strings.HasPrefix(s, "/") && boardNameExpr(x.X) {
					t.Errorf("%s: a board name is concatenated into a path unescaped (use url.PathEscape)", fset.Position(x.Pos()))
				}
			case *ast.CallExpr:
				sel, ok := x.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Sprintf" || len(x.Args) < 2 {
					return true
				}
				if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "fmt" {
					return true
				}
				format, ok := stringLit(x.Args[0])
				if !ok {
					return true
				}
				arg := 0
				for i := 0; i < len(format); i++ {
					if format[i] != '%' {
						continue
					}
					if i+1 < len(format) && format[i+1] == '%' {
						i++
						continue
					}
					afterSlash := i > 0 && format[i-1] == '/'
					arg++
					if afterSlash && arg < len(x.Args) && boardNameExpr(x.Args[arg]) {
						t.Errorf("%s: a board name is formatted into a path unescaped (use url.PathEscape)", fset.Position(x.Args[arg].Pos()))
					}
				}
			}
			return true
		})
	}
	if checked == 0 {
		t.Fatal("no source files checked")
	}
}
