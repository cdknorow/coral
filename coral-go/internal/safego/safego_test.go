package safego

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fastBackoff(t *testing.T) {
	t.Helper()
	old := []time.Duration{loopBackoff, loopBackoffMax, loopHealthy}
	loopBackoff, loopBackoffMax, loopHealthy = time.Millisecond, 5*time.Millisecond, time.Hour
	t.Cleanup(func() { loopBackoff, loopBackoffMax, loopHealthy = old[0], old[1], old[2] })
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// #3998 (Security's #3994 finding 2): a panic in a goroutine is recovered and counted, and the
// process (this test binary: an unrecovered panic would end it) keeps running.
func TestRecoveredGoroutinesKeepTheProcess(t *testing.T) {
	before := Panics()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer Recover("test literal")
		panic("boom")
	}()
	<-done
	Go("test Go", func() { panic("boom 2") })
	waitFor(t, "two recovered panics", func() bool { return Panics() >= before+2 })
}

// Restart runs fn again after a panic, and stops once fn returns normally.
func TestRestartAfterPanicOnly(t *testing.T) {
	fastBackoff(t)
	var runs atomic.Int32
	Restart("test restart", func() {
		if runs.Add(1) < 3 {
			panic("again")
		}
	})
	waitFor(t, "three runs", func() bool { return runs.Load() == 3 })
	time.Sleep(20 * time.Millisecond)
	if n := runs.Load(); n != 3 {
		t.Errorf("ran %d times after a normal return", n)
	}
}

// Loop restarts a service after a panic or a return, until ctx ends.
func TestLoopRestartsUntilDone(t *testing.T) {
	fastBackoff(t)
	ctx, cancel := context.WithCancel(context.Background())
	var runs atomic.Int32
	Loop(ctx, "test loop", func() {
		if runs.Add(1)%2 == 0 {
			panic("odd")
		}
	})
	waitFor(t, "several restarts", func() bool { return runs.Load() >= 4 })
	cancel()
	n := runs.Load()
	time.Sleep(30 * time.Millisecond)
	if runs.Load() > n+1 {
		t.Errorf("kept restarting after ctx ended")
	}
}

// Every go statement in the server's code (internal/, pkg/ and the server programs, tests aside) starts with
// defer safego.Recover(...), so a new bare one fails here (the bug class stays fixed).
func TestEveryGoroutineRecovers(t *testing.T) {
	var bad []string
	n := 0
	// internal/, pkg/, and the programs that run the server in their own process.
	for _, root := range []string{"..", "../../pkg", "../../cmd/coral", "../../cmd/coral-tray", "../../cmd/launch-coral"} {
		err := filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if fi.IsDir() && (fi.Name() == "testdata" || fi.Name() == "node_modules") {
				return filepath.SkipDir
			}
			if fi.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			self := filepath.Base(filepath.Dir(p)) == "safego"
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, p, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(f, func(x ast.Node) bool {
				g, ok := x.(*ast.GoStmt)
				if !ok {
					return true
				}
				n++
				if !startsWithRecover(g, self) {
					bad = append(bad, fset.Position(g.Pos()).String())
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if n < 50 {
		t.Fatalf("found only %d go statements: is the walk looking in the right place?", n)
	}
	if len(bad) > 0 {
		t.Errorf("a goroutine that can end the server with a panic: start it with go func() { defer safego.Recover(\"name\"); ... }() (or safego.Go):\n%s", strings.Join(bad, "\n"))
	}
}

func startsWithRecover(g *ast.GoStmt, self bool) bool {
	lit, ok := g.Call.Fun.(*ast.FuncLit)
	if !ok || len(lit.Body.List) == 0 {
		return false
	}
	d, ok := lit.Body.List[0].(*ast.DeferStmt)
	if !ok {
		return false
	}
	switch fn := d.Call.Fun.(type) {
	case *ast.SelectorExpr:
		pkg, ok := fn.X.(*ast.Ident)
		return ok && pkg.Name == "safego" && fn.Sel.Name == "Recover"
	case *ast.Ident:
		return self && fn.Name == "Recover"
	}
	return false
}
