package routes

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The binding of each route that attributes by identity (#4244 follow-up (a); Security's requirement
// (b) on #4232): is the caller's identity compared with its agent session token, or is the route
// claim-only, so that enforcing identity (actor()) leaves no claim-only route behind unseen. The list
// is built from the source, not written by hand: router.go's registrations, each handler followed
// through the calls it makes on its own receiver and to package functions, looking for the helpers
// that bind a request to its token. testdata/identity_route_bindings.txt is the reviewed result;
// a new or changed route fails here until the list is regenerated (UPDATE_GOLDEN=1) and read.
//
// Bindings:
//   board-claims     /api/board/{project}/...: for a token, the identity layer (checkBoard) compares the
//                    claimed names (subscriber_id, as, created_by, session_name; query and body) with the
//                    token's subscription and board, in every mode
//   board-unchecked  /api/board/projects, /tasks and /remotes/...: checkBoard skips them
//   token-bound      the handler reaches callerSession or agentFromRequest: a claimed session is compared
//                    with the token's
//   token-session    it reaches VerifiedSession (the token's session) but neither of those: read the handler
//   unbound          it reaches none: the request names what it is about, nothing compares the caller
//   inline           the handler is not a method of a known handler type
// "operator-only" marks routes an agent token is refused (operatorOnlyRoutes).

func parseRouterSource(t *testing.T) (*ast.File, *token.FileSet) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "../router.go", nil, 0)
	require.NoError(t, err)
	return f, fset
}

type fnKey struct{ recv, name string }

func recvTypeName(fd *ast.FuncDecl) (typ, varName string) {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return "", ""
	}
	f := fd.Recv.List[0]
	e := f.Type
	if st, ok := e.(*ast.StarExpr); ok {
		e = st.X
	}
	if id, ok := e.(*ast.Ident); ok {
		typ = id.Name
	}
	if len(f.Names) > 0 {
		varName = f.Names[0].Name
	}
	return typ, varName
}

// bindingFlags reports whether the function (type, name) reaches the token-binding helpers.
func bindingFlags(funcs map[fnKey]*ast.FuncDecl, start fnKey) (bound, session bool) {
	seen := map[fnKey]bool{}
	var walk func(k fnKey)
	walk = func(k fnKey) {
		if seen[k] {
			return
		}
		seen[k] = true
		fd, ok := funcs[k]
		if !ok || fd.Body == nil {
			return
		}
		_, recvVar := recvTypeName(fd)
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			c, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch x := c.Fun.(type) {
			case *ast.Ident:
				switch x.Name {
				case "callerSession":
					bound = true
				case "VerifiedSession":
					session = true
				}
				walk(fnKey{"", x.Name})
			case *ast.SelectorExpr:
				if id, ok := x.X.(*ast.Ident); ok && recvVar != "" && id.Name == recvVar {
					if x.Sel.Name == "agentFromRequest" {
						bound = true
					}
					walk(fnKey{k.recv, x.Sel.Name})
				}
			}
			return true
		})
	}
	walk(start)
	return bound, session
}

func routeBindings(t *testing.T) []string {
	t.Helper()
	router, _ := parseRouterSource(t)
	src := parseRoutesSource(t)
	funcs := map[fnKey]*ast.FuncDecl{}
	for _, f := range src.files {
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok {
				typ, _ := recvTypeName(fd)
				funcs[fnKey{typ, fd.Name.Name}] = fd
			}
		}
	}
	// handler variables -> types: x := routes.NewFooHandler(...)
	varType := map[string]string{"sessHandler": "SessionsHandler"}
	ast.Inspect(router, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
			return true
		}
		id, ok := as.Lhs[0].(*ast.Ident)
		c, ok2 := as.Rhs[0].(*ast.CallExpr)
		if !ok || !ok2 {
			return true
		}
		if se, ok := c.Fun.(*ast.SelectorExpr); ok && strings.HasPrefix(se.Sel.Name, "New") && strings.HasSuffix(se.Sel.Name, "Handler") {
			varType[id.Name] = strings.TrimPrefix(se.Sel.Name, "New")
		}
		return true
	})

	// Every registration, wherever it is made: router.go's, and the routes files' own (agent_ui.go
	// registers its routes through a method of its handler), each handler resolved by the receiver
	// of the registering function or by the variable it was made in.
	all := []*ast.File{router}
	for _, f := range src.files {
		all = append(all, f)
	}
	var lines []string
	for _, f := range all {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			recvT, recvVar := recvTypeName(fd)
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				c, ok := n.(*ast.CallExpr)
				if !ok || len(c.Args) != 2 {
					return true
				}
				se, ok := c.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				method := strings.ToUpper(se.Sel.Name)
				switch method {
				case "GET", "POST", "PUT", "PATCH", "DELETE":
				default:
					return true
				}
				bl, ok := c.Args[0].(*ast.BasicLit)
				if !ok || bl.Kind != token.STRING {
					return true
				}
				path := strings.Trim(bl.Value, `"`)
				if !strings.HasPrefix(path, "/api/") {
					return true
				}
				binding, flags := "inline", ""
				if hs, ok := c.Args[1].(*ast.SelectorExpr); ok {
					if hid, ok := hs.X.(*ast.Ident); ok {
						typ := varType[hid.Name]
						if recvVar != "" && hid.Name == recvVar {
							typ = recvT
						}
						if typ != "" {
							bound, session := bindingFlags(funcs, fnKey{typ, hs.Sel.Name})
							switch {
							case bound:
								binding = "token-bound"
							case session:
								binding = "token-session"
							default:
								binding = "unbound"
							}
						}
					}
				}
				if rest, ok := strings.CutPrefix(path, "/api/board/"); ok {
					first, _, _ := strings.Cut(rest, "/")
					if first == "projects" || first == "tasks" || first == "remotes" {
						binding = "board-unchecked"
					} else {
						switch binding {
						case "token-bound", "token-session":
							flags += " +" + binding
						}
						binding = "board-claims"
					}
				}
				if _, ok := operatorOnlyRoutes[method+" "+path]; ok {
					flags += " | operator-only"
				}
				listed := strings.HasPrefix(path, "/api/board/") || strings.HasPrefix(path, "/api/agent/") || strings.HasPrefix(path, "/api/sessions/") ||
					binding == "token-bound" || binding == "token-session"
				if listed {
					lines = append(lines, fmt.Sprintf("%s %s | %s%s", method, path, binding, flags))
				}
				return true
			})
		}
	}
	sort.Strings(lines)
	out := lines[:0]
	for i, l := range lines {
		if i == 0 || l != lines[i-1] {
			out = append(out, l)
		}
	}
	return out
}

func TestRouteBindingsAreReviewed(t *testing.T) {
	lines := routeBindings(t)
	require.Greater(t, len(lines), 100, "the list must find the routes (found %d)", len(lines))
	got := strings.Join(lines, "\n") + "\n"
	const golden = "testdata/identity_route_bindings.txt"
	if os.Getenv("UPDATE_GOLDEN") != "" {
		require.NoError(t, os.WriteFile(golden, []byte(got), 0o644))
		return
	}
	want, err := os.ReadFile(golden)
	require.NoError(t, err, "no reviewed list: run with UPDATE_GOLDEN=1")
	if string(want) != got {
		wantSet, gotSet := map[string]bool{}, map[string]bool{}
		for _, l := range strings.Split(strings.TrimSpace(string(want)), "\n") {
			wantSet[l] = true
		}
		for _, l := range lines {
			gotSet[l] = true
		}
		var added, removed []string
		for l := range gotSet {
			if !wantSet[l] {
				added = append(added, "+ "+l)
			}
		}
		for l := range wantSet {
			if !gotSet[l] {
				removed = append(removed, "- "+l)
			}
		}
		sort.Strings(added)
		sort.Strings(removed)
		assert.Fail(t, "a route's binding changed or a route is new: read it, then regenerate "+golden+" (UPDATE_GOLDEN=1)",
			strings.Join(append(removed, added...), "\n"))
	}
}
