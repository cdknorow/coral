package tmux

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// typedValueNames are the variable names that hold what was typed, pasted, sent or launched in
// the tmux client and the terminal backends: argument lists, key names, text, payloads, prompts
// and command lines.
var typedValueNames = map[string]bool{
	"args": true, "keys": true, "key": true, "text": true, "data": true, "payload": true,
	"command": true, "cmdline": true, "cmdLine": true, "content": true, "prompt": true, "input": true,
	"literal": true, "chunk": true, "buf": true,
}

// reviewedLogValues are the calls below that pass one of those names, each reviewed: what it
// logs is not what was typed. Keyed file:function:name; a listed site that no longer exists
// fails too, so the list stays true.
var reviewedLogValues = map[string]string{}

// #4112 (Security's #4107 audit): no log line or error in the tmux client or the terminal
// backends carries typed text, pasted text, key names or a launch command line. A failed tmux
// command logged its whole argument list; errors reach the log through their callers. A new
// log.Printf, slog call or fmt.Errorf in internal/tmux or internal/ptymanager that passes a
// variable named like one that holds such a value fails here until it logs a length or a name
// instead, or is reviewed and listed above.
func TestNoTypedValuesInTmuxAndTerminalLogs(t *testing.T) {
	found := map[string]bool{}
	var bad []string
	for _, dir := range []string{".", filepath.Join("..", "ptymanager")} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, f, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok || !isLogOrErrorCall(call) {
						return true
					}
					for _, a := range call.Args {
						ast.Inspect(a, func(m ast.Node) bool {
							if c, isCall := m.(*ast.CallExpr); isCall && valueSafeCall(c) {
								return false // len(text), describeTmuxCommand(args): never the value itself
							}
							id, ok := m.(*ast.Ident)
							if !ok || !typedValueNames[id.Name] {
								return true
							}
							key := filepath.Base(f) + ":" + fn.Name.Name + ":" + id.Name
							found[key] = true
							if _, ok := reviewedLogValues[key]; !ok {
								bad = append(bad, key+" at "+fset.Position(id.Pos()).String())
							}
							return true
						})
					}
					return true
				})
			}
		}
	}
	sort.Strings(bad)
	for _, b := range bad {
		t.Errorf("a log line or error passes a typed value: %s", b)
	}
	for k := range reviewedLogValues {
		if !found[k] {
			t.Errorf("reviewed site %s no longer exists: drop it from reviewedLogValues", k)
		}
	}
}

// valueSafeCall: a call whose result says something about a value without being it.
func valueSafeCall(c *ast.CallExpr) bool {
	id, ok := c.Fun.(*ast.Ident)
	if !ok {
		return false
	}
	switch id.Name {
	case "len", "cap", "describeTmuxCommand", "logKey":
		return true
	}
	return false
}

// isLogOrErrorCall: log.Print*/Fatal*/Panic*, slog.* (and a logger's Info/Warn/Error/Debug), fmt.Errorf.
func isLogOrErrorCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	name := sel.Sel.Name
	if pkg, ok := sel.X.(*ast.Ident); ok {
		switch pkg.Name {
		case "log":
			return strings.HasPrefix(name, "Print") || strings.HasPrefix(name, "Fatal") || strings.HasPrefix(name, "Panic")
		case "slog":
			return true
		case "fmt":
			return name == "Errorf"
		}
	}
	switch name {
	case "Info", "Warn", "Error", "Debug", "InfoContext", "WarnContext", "ErrorContext", "DebugContext":
		return true
	}
	return false
}
