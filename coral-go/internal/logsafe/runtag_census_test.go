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

// The run tag of a tmux agent (CORAL_PTY_RUN, #6141) names which processes belong to the agent. It is not a credential, but it is the one thing that lets a process be ended with an agent, so it is kept out of
// Coral's own logs: the only place it appears is the launch line typed into the agent's terminal, like the other variables it exports. The census: no log call takes a variable, field or slog attribute named for it.

var runTagName = regexp.MustCompile(`(?i)^(run_?tag|pty_?run(_?tag)?|coral_?pty_?run)$`)

func runTagLike(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.Ident:
		return runTagName.MatchString(x.Name)
	case *ast.SelectorExpr:
		return runTagName.MatchString(x.Sel.Name)
	case *ast.StarExpr:
		return runTagLike(x.X)
	case *ast.ParenExpr:
		return runTagLike(x.X)
	}
	return false
}

// runTagLogArgs are the lines in file where a log call is given the run tag, as "line: the argument".
func runTagLogArgs(fset *token.FileSet, file *ast.File) []string {
	var out []string
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || !isLogCall(call) {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "fmt" {
				return true
			}
		}
		kv := slogStyle(call)
		for i, a := range call.Args {
			bad := runTagLike(a)
			if kv && i >= 2 && i%2 == 0 {
				if lit, ok := call.Args[i-1].(*ast.BasicLit); ok && lit.Kind == token.STRING && runTagName.MatchString(strings.Trim(lit.Value, "\"`")) {
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

func TestTheRunTagCensusFindsWhatItIsForAndLeavesTheRest(t *testing.T) {
	for name, tc := range map[string]struct {
		src  string
		want int
	}{
		"a variable":              {`package x; func f() { log.Printf("tag %s", runTag) }`, 1},
		"a field":                 {`package x; func f() { log.Printf("tag %s", params.RunTag) }`, 1},
		"an slog attribute value": {`package x; func f() { slog.Warn("m", "run_tag", t) }`, 1},
		"an slog variable":        {`package x; func f() { slog.Info("m", "tag", RunTag) }`, 1},
		"the variable name":       {`package x; func f() { log.Println(coralPtyRun) }`, 1},
		"a session id":            {`package x; func f() { slog.Warn("m", "session_id", sid) }`, 0},
		"a folder tag":            {`package x; func f() { log.Printf("tag %s", tag.Name) }`, 0},
		"an error":                {`package x; func f() { slog.Warn("m", "error", err) }`, 0},
		"fmt builds, not logs":    {`package x; func f() { _ = fmt.Sprintf("%s", runTag) }`, 0},
	} {
		fset, f := parse(t, tc.src)
		assert.Len(t, runTagLogArgs(fset, f), tc.want, name)
	}
}

func TestNoLogCallTakesTheRunTag(t *testing.T) {
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
		for _, l := range runTagLogArgs(fset, f) {
			bad = append(bad, filepath.ToSlash(rel)+":"+l)
		}
		return nil
	}))
	sort.Strings(bad)
	assert.Empty(t, bad, "a log call is given the run tag of a tmux agent: leave it out (its session id says which agent)")
}
