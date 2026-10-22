package executil

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"
)

// Every place Coral itself starts an agent CLI (or its shim) must set NoCwdSearch on the command (#183): on Windows a claude.cmd shim looks in the working folder first, and a probe
// runs with a project folder as its working folder. These are the files that start one; the function that builds the command must also call NoCwdSearch. A new place that starts a
// CLI belongs in this list.
//
// Each must also hide its console (HideWindow, or executil.Command, which does): a Coral server that has no console of its own (started by a scheduled task or a launcher) gives every
// console program it starts a visible window of its own otherwise (#211). The agent's own terminal (a PTY) is not started here and is not in this list.
var agentCLIStarts = []string{
	"agent/cli_version.go",
	"agentmodels/service.go",
	"agentsignin/agentsignin.go",
	"background/goals.go",
	"background/knowledge.go",
	"background/summarizer.go",
	"background/workflow_steps.go",
	"background/workflow_hooks.go",
	"server/routes/system_cli_check.go",
	"server/routes/system_team_import.go",
	"server/routes/themes.go",
}

func TestEveryAgentCLIStartDisablesTheCurrentFolderSearch(t *testing.T) {
	for _, rel := range agentCLIStarts {
		path := filepath.Join("..", filepath.FromSlash(rel))
		src, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("%s: %v", rel, err)
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, src, 0)
		if err != nil {
			t.Errorf("%s: %v", rel, err)
			continue
		}
		for _, d := range file.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			starts, hardens, plainStarts, hides := 0, 0, 0, 0
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
					if pkg, ok := sel.X.(*ast.Ident); ok {
						switch {
						case pkg.Name == "exec" && (sel.Sel.Name == "Command" || sel.Sel.Name == "CommandContext"):
							starts++
							plainStarts++ // executil.Command hides the window itself; exec.Command does not
						case pkg.Name == "executil" && sel.Sel.Name == "Command":
							starts++
						case pkg.Name == "executil" && sel.Sel.Name == "HideWindow":
							hides++
						case pkg.Name == "executil" && (sel.Sel.Name == "NoCwdSearch" || sel.Sel.Name == "WithNoCwdSearch"):
							hardens++
						}
					}
				}
				return true
			})
			if starts > 0 && hardens < starts {
				t.Errorf("%s: %s starts %d command(s) but calls NoCwdSearch %d time(s)", rel, fn.Name.Name, starts, hardens)
			}
			if hides < plainStarts {
				t.Errorf("%s: %s starts %d command(s) with exec.Command but calls HideWindow %d time(s) (use executil.Command, or HideWindow)", rel, fn.Name.Name, plainStarts, hides)
			}
		}
	}
}
