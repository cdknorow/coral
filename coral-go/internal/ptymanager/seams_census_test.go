package ptymanager

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// A test must not assign a package-level variable of this package (#5266). The gate's retry loop is one goroutine for the whole process and
// outlives the test that started it, so a test that sets a package variable and puts it back in t.Cleanup races with the loop reading it (a
// DATA RACE under -race in three of twenty CI runs in five hours: pasteVerifyWait, pasteStuckAfter, the gate's clock). Seams a goroutine
// may read are atomic pointers set through setWaits, setNow and setSpacer (seams_test.go); a variable that is itself a mutex-guarded struct
// is changed under its lock (needsInputFunc and the like have Set functions). This census finds the direct assignments (x = ..., x, y = ...,
// x++), a name declared in the function (a parameter, :=, var) shadowing the package variable.
func TestTestsDoNotAssignPackageVariables(t *testing.T) {
	fs := token.NewFileSet()
	pkgs, err := parser.ParseDir(fs, ".", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	pkgVars := map[string]bool{}
	var tests []*ast.File
	var names []string
	for _, p := range pkgs {
		if strings.HasSuffix(p.Name, "_test") {
			continue
		}
		for fn, f := range p.Files {
			if strings.HasSuffix(fn, "_test.go") {
				tests = append(tests, f)
				names = append(names, fn)
			}
			for _, d := range f.Decls {
				if gd, ok := d.(*ast.GenDecl); ok && gd.Tok == token.VAR {
					for _, s := range gd.Specs {
						for _, n := range s.(*ast.ValueSpec).Names {
							if n.Name != "_" {
								pkgVars[n.Name] = true
							}
						}
					}
				}
			}
		}
	}
	// Package variables a test file declares are the test's own state, not the package's.
	for _, f := range tests {
		for _, d := range f.Decls {
			if gd, ok := d.(*ast.GenDecl); ok && gd.Tok == token.VAR {
				for _, s := range gd.Specs {
					for _, n := range s.(*ast.ValueSpec).Names {
						delete(pkgVars, n.Name)
					}
				}
			}
		}
	}
	var found []string
	for i, f := range tests {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			local := map[string]bool{}
			for _, fl := range []*ast.FieldList{fd.Type.Params, fd.Type.Results, fd.Recv} {
				if fl != nil {
					for _, p := range fl.List {
						for _, n := range p.Names {
							local[n.Name] = true
						}
					}
				}
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.FuncLit: // its parameters shadow too
					if x.Type.Params != nil {
						for _, p := range x.Type.Params.List {
							for _, id := range p.Names {
								local[id.Name] = true
							}
						}
					}
				case *ast.ValueSpec:
					for _, id := range x.Names {
						local[id.Name] = true
					}
				case *ast.RangeStmt:
					if x.Tok == token.DEFINE {
						for _, e := range []ast.Expr{x.Key, x.Value} {
							if id, ok := e.(*ast.Ident); ok {
								local[id.Name] = true
							}
						}
					}
				case *ast.AssignStmt:
					for _, l := range x.Lhs {
						id, ok := l.(*ast.Ident)
						if !ok {
							continue
						}
						if x.Tok == token.DEFINE {
							local[id.Name] = true
						} else if pkgVars[id.Name] && !local[id.Name] {
							found = append(found, filepath.Base(names[i])+":"+strconv.Itoa(fs.Position(x.Pos()).Line)+" in "+fd.Name.Name+": "+id.Name)
						}
					}
				case *ast.IncDecStmt:
					if id, ok := x.X.(*ast.Ident); ok && pkgVars[id.Name] && !local[id.Name] {
						found = append(found, filepath.Base(names[i])+":"+strconv.Itoa(fs.Position(x.Pos()).Line)+" in "+fd.Name.Name+": "+id.Name)
					}
				}
				return true
			})
		}
	}
	sort.Strings(found)
	if len(found) > 0 {
		t.Fatalf("a test assigns a package-level variable (a goroutine an earlier test left running may read it: a data race under -race, #5266). "+
			"Make it a field of waits() and use setWaits, or use setNow / setSpacer, or an atomic of its own:\n  %s", strings.Join(found, "\n  "))
	}
}

// A log destination a test hands to log.SetOutput or a slog handler must be safe to read while a goroutine writes: &buf of a bytes.Buffer is
// not (the gate's loop of an earlier test logged into the next test's buffer while it read it: #5266). logBuf, syncWriter and lockedWriter are.
func TestTestsDoNotLogIntoABareBuffer(t *testing.T) {
	fs := token.NewFileSet()
	pkgs, err := parser.ParseDir(fs, ".", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	for _, p := range pkgs {
		for fn, f := range p.Files {
			if !strings.HasSuffix(fn, "_test.go") {
				continue
			}
			bare := map[string]bool{} // names declared as `var x bytes.Buffer` in this file
			ast.Inspect(f, func(n ast.Node) bool {
				if vs, ok := n.(*ast.ValueSpec); ok {
					if se, ok := vs.Type.(*ast.SelectorExpr); ok && se.Sel.Name == "Buffer" {
						if pk, ok := se.X.(*ast.Ident); ok && pk.Name == "bytes" {
							for _, id := range vs.Names {
								bare[id.Name] = true
							}
						}
					}
				}
				return true
			})
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) == 0 {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || (sel.Sel.Name != "SetOutput" && sel.Sel.Name != "NewTextHandler" && sel.Sel.Name != "NewJSONHandler") {
					return true
				}
				if u, ok := call.Args[0].(*ast.UnaryExpr); ok && u.Op == token.AND {
					if id, ok := u.X.(*ast.Ident); ok && bare[id.Name] {
						found = append(found, filepath.Base(fn)+":"+strconv.Itoa(fs.Position(call.Pos()).Line)+": "+sel.Sel.Name+"(&"+id.Name+")")
					}
				}
				return true
			})
		}
	}
	sort.Strings(found)
	if len(found) > 0 {
		t.Fatalf("a test logs into a variable's address (a bytes.Buffer is a data race under -race when a goroutine still logs while the test reads it, #5266); use logBuf (seams_test.go):\n  %s", strings.Join(found, "\n  "))
	}
}
