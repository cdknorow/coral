package startup

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

// Importing net/http/pprof (pprof.go) registers the profiling handlers on
// http.DefaultServeMux. Nothing may serve that mux, or heap and goroutine
// dumps would be exposed without CORAL_PPROF_ADDR. This test fails on any
// production code that references http.DefaultServeMux, passes a nil handler
// to http.ListenAndServe / ListenAndServeTLS / Serve / ServeTLS, or builds an
// http.Server without a Handler.
func TestNothingServesDefaultServeMux(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	var violations []string
	fset := token.NewFileSet()
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "vendor", "node_modules", "testdata", ".git":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return nil // not this test's concern
		}
		httpName := ""
		for _, imp := range file.Imports {
			if p, _ := strconv.Unquote(imp.Path.Value); p == "net/http" {
				httpName = "http"
				if imp.Name != nil {
					httpName = imp.Name.Name
				}
			}
		}
		if httpName == "" {
			return nil
		}
		isHTTP := func(e ast.Expr, names ...string) bool {
			sel, ok := e.(*ast.SelectorExpr)
			if !ok {
				return false
			}
			x, ok := sel.X.(*ast.Ident)
			if !ok || x.Name != httpName {
				return false
			}
			for _, n := range names {
				if sel.Sel.Name == n {
					return true
				}
			}
			return false
		}
		report := func(n ast.Node, what string) {
			violations = append(violations, fset.Position(n.Pos()).String()+": "+what)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.SelectorExpr:
				if isHTTP(n, "DefaultServeMux") {
					report(n, "references http.DefaultServeMux")
				}
			case *ast.CallExpr:
				if isHTTP(n.Fun, "ListenAndServe", "ListenAndServeTLS", "Serve", "ServeTLS") {
					handler := n.Args[len(n.Args)-1]
					if id, ok := handler.(*ast.Ident); ok && id.Name == "nil" {
						report(n, "serves with a nil handler (http.DefaultServeMux)")
					}
				}
			case *ast.CompositeLit:
				if isHTTP(n.Type, "Server") {
					hasHandler := false
					for _, el := range n.Elts {
						if kv, ok := el.(*ast.KeyValueExpr); ok {
							if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "Handler" {
								if id, ok := kv.Value.(*ast.Ident); !ok || id.Name != "nil" {
									hasHandler = true
								}
							}
						}
					}
					if !hasHandler {
						report(n, "http.Server without a Handler serves http.DefaultServeMux")
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
	for _, v := range violations {
		t.Error(v)
	}
}
