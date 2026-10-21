package secfile

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// lintedPackages write files into folders an agent can write to (logs, hook caches, the data
// folder), so a link planted at a file's name would steer the write (#3920 R1, Security's
// #3906 item 5). routes has its own, stricter lint of every open (file_open_lint_test.go).
var lintedPackages = []string{
	"../inputlog", "../logrotate", "../hooks", "../background", "../agent", "../ptymanager",
	"../stallwatch", "../tracking",
}

// reviewedWriteOpens are the writes in lintedPackages that don't go through OpenNoFollow, each
// with the reason a planted link can't steer it. Keyed package/file:function:call, never by line;
// a listed site that no longer exists fails too, so the list stays true.
var reviewedWriteOpens = map[string]string{
	"agent/claude.go:writeNoFollow:OpenFile": "O_EXCL (and O_NOFOLLOW on Unix): any existing name, link or not, is refused (#3582)",
}

// A new os.Create, os.WriteFile, or os.OpenFile that can write (O_WRONLY, O_RDWR, O_CREATE,
// O_TRUNC, O_APPEND, or flags the lint can't read) in lintedPackages fails until it uses
// OpenNoFollow or is reviewed and listed above, so the bug class stays fixed.
func TestWriteOpensFollowNoLink(t *testing.T) {
	found := map[string]bool{}
	for _, dir := range lintedPackages {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		if len(files) == 0 {
			t.Fatalf("%s: no Go files (moved? update lintedPackages)", dir)
		}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			fset := token.NewFileSet()
			af, err := parser.ParseFile(fset, f, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			key := filepath.Base(dir) + "/" + filepath.Base(f)
			for _, d := range af.Decls {
				fd, ok := d.(*ast.FuncDecl)
				if !ok || fd.Body == nil {
					continue
				}
				name := fd.Name.Name
				if fd.Recv != nil && len(fd.Recv.List) > 0 {
					typ := fd.Recv.List[0].Type
					if st, ok := typ.(*ast.StarExpr); ok {
						typ = st.X
					}
					if id, ok := typ.(*ast.Ident); ok {
						name = id.Name + "." + name
					}
				}
				ast.Inspect(fd.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "os" {
						return true
					}
					switch sel.Sel.Name {
					case "Create", "WriteFile":
						found[key+":"+name+":"+sel.Sel.Name] = true
					case "OpenFile":
						if len(call.Args) < 2 || mayWrite(call.Args[1]) {
							found[key+":"+name+":"+sel.Sel.Name] = true
						}
					}
					return true
				})
			}
		}
	}
	var unreviewed, stale []string
	for k := range found {
		if _, ok := reviewedWriteOpens[k]; !ok {
			unreviewed = append(unreviewed, k)
		}
	}
	for k := range reviewedWriteOpens {
		if !found[k] {
			stale = append(stale, k)
		}
	}
	sort.Strings(unreviewed)
	sort.Strings(stale)
	if len(unreviewed) > 0 {
		t.Errorf("a write that may follow a planted link (use secfile.OpenNoFollow, or review it and add it to reviewedWriteOpens with the reason):\n%s", strings.Join(unreviewed, "\n"))
	}
	if len(stale) > 0 {
		t.Errorf("reviewedWriteOpens lists sites that no longer exist (remove them):\n%s", strings.Join(stale, "\n"))
	}
}

// mayWrite reports whether an os.OpenFile flag expression can open for writing: it names a write
// flag, or it isn't plainly os.O_RDONLY (with other names the lint can't read, it may).
func mayWrite(flag ast.Expr) bool {
	readOnly, write := false, false
	ast.Inspect(flag, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SelectorExpr:
			switch x.Sel.Name {
			case "O_RDONLY":
				readOnly = true
			case "O_WRONLY", "O_RDWR", "O_CREATE", "O_TRUNC", "O_APPEND":
				write = true
			}
			return false
		}
		return true
	})
	return write || !readOnly
}
