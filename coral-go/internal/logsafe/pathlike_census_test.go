package logsafe

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A log is what a bug report carries, and a path in it names the person's folders, projects and tools (#6053). The census: no log call takes a variable (or a field) that holds a path, nor the value
// of a slog attribute whose key names one, except through logsafe: Dir for a project folder (its name), Path for a tool or data location (the home folder as "~"). The sinks redact what is left
// (Writer); this keeps the lines themselves free of it, so a sink that is not wrapped does not leak.

var pathWords = map[string]bool{"dir": true, "dirs": true, "path": true, "paths": true, "folder": true, "folders": true, "cwd": true, "root": true, "exe": true, "socket": true,
	"worktree": true, "workdir": true, "home": true, "file": true, "filepath": true, "bin": true, "wd": true, "dest": true}

var camelWord = regexp.MustCompile(`[A-Z]+[a-z0-9]*|[a-z0-9]+`)

// lastWord is the last word of a name: "workingDir", "WorkingDir" and "working_dir" are all "dir".
func lastWord(name string) string {
	ws := camelWord.FindAllString(name, -1)
	if len(ws) == 0 {
		return ""
	}
	return strings.ToLower(ws[len(ws)-1])
}

func isPathName(name string) bool { return pathWords[lastWord(name)] }

// pathLike reports whether the argument is a plain variable or field whose name says it holds a path.
func pathLike(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.Ident:
		return isPathName(x.Name) && !isErrName(x.Name)
	case *ast.SelectorExpr:
		return isPathName(x.Sel.Name) && !isErrName(x.Sel.Name)
	case *ast.StarExpr:
		return pathLike(x.X)
	case *ast.ParenExpr:
		return pathLike(x.X)
	}
	return false
}

// isErrName is an error value (errNoLogPath, ErrBadPath), which says what went wrong and is not a place.
func isErrName(n string) bool {
	return len(n) > 3 && (strings.HasPrefix(n, "err") || strings.HasPrefix(n, "Err")) && n[3] >= 'A' && n[3] <= 'Z'
}

func isLogsafeCall(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == "logsafe"
}

// isBase is filepath.Base(...): the name alone is what a log may carry of a path.
func isBase(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == "filepath" && sel.Sel.Name == "Base"
}

// slogStyle reports a call that takes a message and then key, value pairs (slog.Info, logger.Warn); the others (log.Printf, Println) take a format and its operands.
func slogStyle(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	switch sel.Sel.Name {
	case "Info", "Warn", "Error", "Debug":
		return true
	}
	return false
}

// isURLPath is a request's URL path (r.URL.Path), which is an address and not a place on this computer.
func isURLPath(e ast.Expr) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Path" {
		return false
	}
	inner, ok := sel.X.(*ast.SelectorExpr)
	return ok && inner.Sel.Name == "URL"
}

// isCount is len(...): the number of folders, not one of them.
func isCount(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	id, ok := call.Fun.(*ast.Ident)
	return ok && id.Name == "len"
}

// exempt is a call carrying a "logsafe-ok: <why>" comment on its line or the line above: a value named like a path that is not one (a request's URL path).
func exempt(fset *token.FileSet, file *ast.File, call *ast.CallExpr) bool {
	line := fset.Position(call.Pos()).Line
	for _, cg := range file.Comments {
		for _, c := range cg.List {
			if l := fset.Position(c.Pos()).Line; (l == line || l == line-1) && strings.Contains(c.Text, "logsafe-ok: ") {
				return true
			}
		}
	}
	return false
}

// pathLogArgs are the lines in file where a log call is given a path other than through logsafe, as "line: the argument".
func pathLogArgs(fset *token.FileSet, file *ast.File) []string {
	var out []string
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || !isLogCall(call) {
			return true
		}
		// fmt.* is not a log call; what it builds is the caller's to wrap.
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "fmt" {
				return true
			}
		}
		if exempt(fset, file, call) {
			return true
		}
		kv := slogStyle(call)
		for i, a := range call.Args {
			if isBase(a) || isLogsafeCall(a) || isURLPath(a) || isCount(a) {
				continue
			}
			bad := pathLike(a)
			// an slog value: the key before it names what it is (the message is argument 0, then key, value, key, value)
			if kv && i >= 2 && i%2 == 0 {
				if lit, ok := call.Args[i-1].(*ast.BasicLit); ok && lit.Kind == token.STRING && isPathName(strings.Trim(lit.Value, "\"`")) {
					bad = true
				}
			}
			if bad {
				out = append(out, itoa(fset.Position(call.Pos()).Line)+": "+exprText(a))
			}
		}
		return true
	})
	return out
}

func exprText(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return exprText(x.X) + "." + x.Sel.Name
	case *ast.CallExpr:
		return exprText(x.Fun) + "(...)"
	case *ast.BasicLit:
		return x.Value
	}
	return "expression"
}

func TestThePathLikeCensusFindsWhatItIsForAndLeavesTheRest(t *testing.T) {
	for name, tc := range map[string]struct {
		src  string
		want int
	}{
		"a directory variable":        {`package x; func f() { log.Printf("in %s", workDir) }`, 1},
		"a field":                     {`package x; func f() { log.Printf("at %s", team.WorkingDir) }`, 1},
		"an slog attribute by key":    {`package x; func f() { slog.Warn("m", "workdir", wd(x)) }`, 1},
		"by the key, snake case":      {`package x; func f() { slog.Warn("m", "working_dir", v) }`, 1},
		"a path variable":             {`package x; func f() { slog.Info("m", "from", tmuxBin) }`, 1},
		"a message ending in folder":  {`package x; func f() { slog.Warn("could not read the uploads folder", "error", err) }`, 0},
		"a count of folders":          {`package x; func f() { slog.Debug("m", "dirs", len(dirToAgents)) }`, 0},
		"a request's URL path":        {`package x; func f() { slog.Info("m", "path", r.URL.Path) }`, 0},
		"an error value":              {`package x; func f() { log.Printf("%v", errNoLogPath) }`, 0},
		"filepath.Base":               {`package x; func f() { slog.Info("m", "path", filepath.Base(p)) }`, 0},
		"exempt, with its reason":     {"package x; func f() {\n// logsafe-ok: a URL path\nslog.Info(\"m\", \"path\", p) }", 0},
		"a comment without a reason":  {"package x; func f() {\n// logsafe-ok\nslog.Info(\"m\", \"path\", p) }", 1},
		"a logger method":             {`package x; func f() { p.logger.Warn("m", "dir", d) }`, 1},
		"through Dir":                 {`package x; func f() { log.Printf("in %s", logsafe.Dir(workDir)) }`, 0},
		"through Path":                {`package x; func f() { slog.Info("m", "exe", logsafe.Path(exe)) }`, 0},
		"the base name":               {`package x; func f() { log.Printf("in %s", filepath.Base(workDir)) }`, 0},
		"an attribute key not a path": {`package x; func f() { slog.Info("m", "session_id", id, "agent", name) }`, 0},
		"a profile is not a file":     {`package x; func f() { log.Printf("%s", profile) }`, 0},
		"a count":                     {`package x; func f() { log.Printf("%d", len(dirs)) }`, 0},
		"an error":                    {`package x; func f() { log.Printf("failed: %v", err) }`, 0},
		"a message that says folder":  {`package x; func f() { log.Printf("the folder is not a git checkout") }`, 0},
	} {
		fset, f := parse(t, tc.src)
		assert.Len(t, pathLogArgs(fset, f), tc.want, name)
	}
}

func TestNoLogCallTakesAPathExceptThroughLogsafe(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	var bad []string
	require.NoError(t, filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", "testdata":
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
			return nil
		}
		rel, _ := filepath.Rel(root, filepath.ToSlash(p))
		for _, l := range pathLogArgs(fset, f) {
			bad = append(bad, filepath.ToSlash(rel)+":"+l)
		}
		return nil
	}))
	sort.Strings(bad)
	assert.Empty(t, bad, "a log call is given a path: write it with logsafe.Dir (a project folder) or logsafe.Path (a tool or data location)")
}

// unwrappedSinks are the log.SetOutput calls in file whose sink is not wrapped in logsafe.Writer, as "line".
func unwrappedSinks(fset *token.FileSet, file *ast.File) []string {
	var out []string
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) != 1 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "SetOutput" {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); !ok || id.Name != "log" {
			return true
		}
		arg, ok := call.Args[0].(*ast.CallExpr)
		if ok {
			if s, ok := arg.Fun.(*ast.SelectorExpr); ok {
				if id, ok := s.X.(*ast.Ident); ok && id.Name == "logsafe" && (s.Sel.Name == "Writer" || s.Sel.Name == "PathWriter") {
					return true
				}
			}
		}
		out = append(out, itoa(fset.Position(call.Pos()).Line))
		return true
	})
	return out
}

func TestEveryLogSinkIsWrappedInTheRedactingWriter(t *testing.T) {
	for name, tc := range map[string]struct {
		src  string
		want int
	}{
		"wrapped":           {`package x; func f() { log.SetOutput(logsafe.Writer(out)) }`, 0},
		"bare":              {`package x; func f() { log.SetOutput(os.Stderr) }`, 1},
		"a different wrap":  {`package x; func f() { log.SetOutput(io.MultiWriter(a, b)) }`, 1},
		"another SetOutput": {`package x; func f() { cmd.SetOutput(os.Stderr) }`, 0},
	} {
		fset, f := parse(t, tc.src)
		assert.Len(t, unwrappedSinks(fset, f), tc.want, name)
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	var bad []string
	require.NoError(t, filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", "testdata":
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
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		for _, l := range unwrappedSinks(fset, f) {
			bad = append(bad, filepath.ToSlash(rel)+":"+l)
		}
		return nil
	}))
	assert.Empty(t, bad, "a log sink is not wrapped: log.SetOutput(logsafe.Writer(...)), so a profile folder, user name or computer name never reaches a log file")
}
