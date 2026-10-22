package logsafe

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

func TestTheHomeFolderIsWrittenAsATildeAndNothingElseIs(t *testing.T) {
	home := filepath.Join(string(filepath.Separator), "people", "someone")
	for in, want := range map[string]string{
		home:                                 "~",
		filepath.Join(home, "go", "bin"):     filepath.Join("~", "go", "bin"),
		home + "-other":                      home + "-other", // a sibling that starts with the same letters is not the home folder
		filepath.Join("/", "usr", "bin"):     filepath.Join("/", "usr", "bin"),
		"":                                   "",
		filepath.Join(home, ".local", "bin"): filepath.Join("~", ".local", "bin"),
		strings.TrimSuffix(home, "someone"):  strings.TrimSuffix(home, "someone"), // a parent of the home folder stays
	} {
		assert.Equal(t, want, HomeRelative(in, home), in)
	}
	assert.Equal(t, "x", HomeRelative("x", ""), "no home folder known: unchanged")
}

func TestThePathSummaryCountsEveryEntryAndNamesNoHomeFolder(t *testing.T) {
	home := filepath.Join(t.TempDir(), "profile-folder-marker")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	sep := string(filepath.ListSeparator)
	t.Setenv("PATH", strings.Join([]string{filepath.Join(home, "go", "bin"), "", filepath.Join("/", "usr", "bin"), home}, sep))
	assert.Equal(t, 3, Count(), "an empty entry is not a folder")
	s := PathSummary()
	assert.Contains(t, s, "path_entries=3")
	assert.NotContains(t, s, "profile-folder-marker", "the home folder is written as ~")
	assert.Contains(t, s, "~")
	assert.Contains(t, s, filepath.Join("/", "usr", "bin"), "other folders are named")
}

func TestAnEmptyPathIsZeroEntries(t *testing.T) {
	t.Setenv("PATH", "")
	assert.Equal(t, 0, Count())
	assert.Equal(t, "path_entries=0 path=", PathSummary())
}

// The census: no Go code of Coral's own (tests apart) writes the PATH into a log or an error message, directly or through a variable it was read into first; the one place it is logged is
// logsafe.PathSummary, at server start (#6018). It reads the syntax, so `p := os.Getenv("PATH")` followed by a log call that mentions p is found as well as a log call that reads the PATH itself.

var logFuncs = map[string]bool{"Printf": true, "Print": true, "Println": true, "Info": true, "Warn": true, "Error": true, "Debug": true, "Infof": true, "Warnf": true, "Errorf": true, "Fatalf": true, "Fatal": true, "Panicf": true}

func isPathRead(n ast.Node) bool {
	call, ok := n.(*ast.CallExpr)
	if !ok || len(call.Args) == 0 {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || (sel.Sel.Name != "Getenv" && sel.Sel.Name != "LookupEnv") {
		return false
	}
	lit, ok := call.Args[0].(*ast.BasicLit)
	return ok && (lit.Value == `"PATH"` || lit.Value == "`PATH`")
}

func isLogCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || !logFuncs[sel.Sel.Name] {
		return false
	}
	if id, ok := sel.X.(*ast.Ident); ok {
		switch id.Name {
		case "log", "slog", "fmt", "logger", "l":
			return true
		}
	}
	return sel.Sel.Name != "Errorf" // a method on a logger of any name (h.log.Info, slog.Default().Warn); an Errorf is only fmt's
}

// mentionsPath reports whether the expression reads the PATH or uses a variable that holds it.
func mentionsPath(e ast.Node, tainted map[string]bool) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if found {
			return false
		}
		if isPathRead(n) {
			found = true
		}
		if id, ok := n.(*ast.Ident); ok && tainted[id.Name] {
			found = true
		}
		return !found
	})
	return found
}

// pathLogs are the places in file where the PATH reaches a log call or an Errorf, as "line: what".
func pathLogs(fset *token.FileSet, file *ast.File) []string {
	pkgTainted := map[string]bool{}
	for _, d := range file.Decls {
		if gd, ok := d.(*ast.GenDecl); ok {
			ast.Inspect(gd, func(n ast.Node) bool {
				if vs, ok := n.(*ast.ValueSpec); ok {
					for i, v := range vs.Values {
						if mentionsPath(v, pkgTainted) && i < len(vs.Names) {
							pkgTainted[vs.Names[i].Name] = true
						}
					}
				}
				return true
			})
		}
	}
	var out []string
	check := func(body ast.Node) {
		tainted := map[string]bool{}
		for k := range pkgTainted {
			tainted[k] = true
		}
		// variables the PATH is read into, to a fixed point (q := strings.Split(p, ":") holds it too)
		for changed := true; changed; {
			changed = false
			ast.Inspect(body, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.AssignStmt:
					for i, r := range x.Rhs {
						if !mentionsPath(r, tainted) {
							continue
						}
						targets := x.Lhs
						if len(x.Rhs) == 1 {
							targets = x.Lhs // one call giving several values: all of them hold it
						} else if i < len(x.Lhs) {
							targets = x.Lhs[i : i+1]
						}
						for _, l := range targets {
							// err and ok are what a call that was GIVEN the PATH answers (SetEnvironment(..., p) returns an error, not the PATH): not a holder of it
							if id, ok := l.(*ast.Ident); ok && !tainted[id.Name] && id.Name != "err" && id.Name != "ok" && id.Name != "_" {
								tainted[id.Name], changed = true, true
							}
						}
					}
				case *ast.ValueSpec:
					for i, v := range x.Values {
						if mentionsPath(v, tainted) && i < len(x.Names) && !tainted[x.Names[i].Name] {
							tainted[x.Names[i].Name], changed = true, true
						}
					}
				}
				return true
			})
		}
		ast.Inspect(body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !isLogCall(call) {
				return true
			}
			for _, a := range call.Args {
				if mentionsPath(a, tainted) {
					out = append(out, itoa(fset.Position(call.Pos()).Line)+": the PATH reaches a log or error call")
					break
				}
			}
			return true
		})
	}
	for _, d := range file.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Body != nil {
			check(fd.Body)
		}
	}
	return out
}

func parse(t *testing.T, src string) (*token.FileSet, *ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "x.go", src, parser.ParseComments)
	require.NoError(t, err)
	return fset, f
}

// The census finds what it is for, and leaves alone a PATH that is only used (negative controls).
func TestTheCensusFindsAPathThatIsLoggedDirectlyOrLaterAndNotOneThatIsOnlyUsed(t *testing.T) {
	for name, tc := range map[string]struct {
		src  string
		want int
	}{
		"logged directly":         {`package x; func f() { log.Printf("%s", os.Getenv("PATH")) }`, 1},
		"slog attribute":          {`package x; func f() { slog.Info("m", "path", os.Getenv("PATH")) }`, 1},
		"read into a variable":    {`package x; func f() { p := os.Getenv("PATH"); doWork(); log.Printf("searched %s", p) }`, 1},
		"var declaration":         {`package x; func f() { var p = os.Getenv("PATH"); slog.Warn("m", "p", p) }`, 1},
		"derived and then logged": {`package x; func f() { p := os.Getenv("PATH"); q := strings.Split(p, ":"); log.Println(q) }`, 1},
		"inside a Sprintf":        {`package x; func f() { p := os.Getenv("PATH"); log.Print(fmt.Sprintf("path=%s", p)) }`, 1},
		"a package variable":      {`package x; var p = os.Getenv("PATH"); func f() { log.Print(p) }`, 1},
		"an error message":        {`package x; func f() error { p := os.Getenv("PATH"); return fmt.Errorf("not found in %s", p) }`, 1},
		"a closure":               {`package x; func f() { p := os.Getenv("PATH"); go func() { log.Print(p) }() }`, 1},
		"LookupEnv":               {`package x; func f() { p, _ := os.LookupEnv("PATH"); log.Print(p) }`, 1},
		"a method on a logger":    {`package x; func f() { p := os.Getenv("PATH"); h.log.Info("m", p) }`, 1},
		"an error a call returns when given the PATH is not the PATH": {`package x; func f() { p := os.Getenv("PATH"); if err := set(p); err != nil { log.Printf("failed: %v", err) } }`, 0},
		"only used for the child env":                                 {`package x; func f() []string { p := os.Getenv("PATH"); return []string{"PATH=" + p} }`, 0},
		"used, and a log without it":                                  {`package x; func f() { p := os.Getenv("PATH"); use(p); log.Printf("started") }`, 0},
		"another variable":                                            {`package x; func f() { p := os.Getenv("HOME"); log.Print(p) }`, 0},
		"the count":                                                   {`package x; func f() { log.Printf("entries=%d", logsafe.Count()) }`, 0},
	} {
		fset, f := parse(t, tc.src)
		assert.Len(t, pathLogs(fset, f), tc.want, name)
	}
}

func TestNoGoCodeOfCoralLogsThePath(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	var bad []string
	require.NoError(t, filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, p, nil, parser.ParseComments)
		if err != nil {
			return nil // a file that does not parse is the compiler's to report
		}
		rel, _ := filepath.Rel(root, p)
		for _, l := range pathLogs(fset, f) {
			bad = append(bad, rel+":"+l)
		}
		return nil
	}))
	assert.Empty(t, bad, "the PATH reaches a log: use logsafe.Count (or PathSummary once at server start)")
}

func TestTheUserNameIsHiddenWhereverItIsAFolderAndInItsShortForm(t *testing.T) {
	names := []string{"runneradmin"}
	sep := string(filepath.Separator)
	for in, want := range map[string]string{
		`C:\Users\RUNNER~1\AppData\Local\bin`: `C:\Users\<user>\AppData\Local\bin`,
		`C:\Users\runneradmin\go\bin`:         `C:\Users\<user>\go\bin`,
		`C:\Users\RunnerAdmin\go\bin`:         `C:\Users\<user>\go\bin`,
		`D:\a\runneradmin\tools`:              `D:\a\<user>\tools`,
		`/home/runneradmin/.cargo/bin`:        `/home/<user>/.cargo/bin`,
		`C:\Program Files\Git\cmd`:            `C:\Program Files\Git\cmd`,
		`C:\Users\runneradmin2\x`:             `C:\Users\runneradmin2\x`, // another folder that merely starts with the name
		`C:\Users\RUNNER~1x\x`:                `C:\Users\RUNNER~1x\x`,
		`C:\PROGRA~1\Git`:                     `C:\PROGRA~1\Git`, // another short name
		"":                                    "",
	} {
		assert.Equal(t, want, HideUserName(in, names), in)
	}
	assert.Equal(t, "x", HideUserName("x", nil), "no name known: unchanged")
	assert.True(t, isShortFormOf("RUNNER~1", "runneradmin"))
	assert.True(t, isShortFormOf("runner~12", "runneradmin"))
	assert.False(t, isShortFormOf("RUNNER~", "runneradmin"))
	assert.False(t, isShortFormOf("OTHERS~1", "runneradmin"))
	_ = sep
}

func TestThePathSummaryHidesTheUserNameAnywhereInAnEntry(t *testing.T) {
	home := filepath.Join(t.TempDir(), "profile-folder-marker")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("USERNAME", "uniqueuser99")
	t.Setenv("USER", "uniqueuser99")
	t.Setenv("PATH", strings.Join([]string{filepath.Join("/", "opt", "uniqueuser99", "bin"), filepath.Join("/", "usr", "bin")}, string(filepath.ListSeparator)))
	s := PathSummary()
	assert.NotContains(t, s, "uniqueuser99")
	assert.Contains(t, s, "<user>")
	assert.Contains(t, s, "path_entries=2")
}
