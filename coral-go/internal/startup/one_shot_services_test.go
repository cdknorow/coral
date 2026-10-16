package startup

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runOnceJobs are startup's jobs that do their work and return: under safeGo (safego.Loop), which
// restarts a service that returns, they would run again every few seconds (#4033: the after-restart
// wake did, re-sending its notice). They start with safego.Restart (again only after a panic) or
// safego.Go.
var runOnceJobs = map[string]bool{"AutoWakeAfterRestart": true}

// TestRunOnceJobsAreNotRestartedServices reads startup.go: no run-once job is started by safeGo or
// safego.Loop, and the after-restart wake is started by safego.Restart.
func TestRunOnceJobsAreNotRestartedServices(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "startup.go", nil, 0)
	require.NoError(t, err)
	calls := func(n ast.Node) map[string]bool {
		got := map[string]bool{}
		ast.Inspect(n, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok {
				if sel, ok := c.Fun.(*ast.SelectorExpr); ok && runOnceJobs[sel.Sel.Name] {
					got[sel.Sel.Name] = true
				}
			}
			return true
		})
		return got
	}
	starter := func(c *ast.CallExpr) string {
		switch fn := c.Fun.(type) {
		case *ast.Ident:
			return fn.Name
		case *ast.SelectorExpr:
			if x, ok := fn.X.(*ast.Ident); ok {
				return x.Name + "." + fn.Sel.Name
			}
		}
		return ""
	}
	restarted := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		c, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch s := starter(c); s {
		case "safeGo", "safego.Loop":
			for job := range calls(c) {
				t.Errorf("%s: %s is a run-once job started by %s, which restarts it whenever it returns", fset.Position(c.Pos()), job, s)
			}
		case "safego.Restart", "safego.Go":
			for job := range calls(c) {
				restarted[job] = true
			}
		}
		return true
	})
	assert.True(t, restarted["AutoWakeAfterRestart"], "the after-restart wake starts with safego.Restart (or Go)")
}
