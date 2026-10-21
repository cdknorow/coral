package routes

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/agent"
	at "github.com/subgentic/coral/internal/agenttypes"
	"github.com/subgentic/coral/internal/store"
)

// pathCLI puts a "claude" on PATH that records that it ran (the operator's real CLI, in the test's words), and returns the marker.
func pathCLI(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake binary")
	}
	dir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "ran")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "claude"), []byte("#!/bin/sh\necho ran >> '"+marker+"'\necho 9.9.9 (Claude Code)\n"), 0o755))
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", dir+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")
	return marker
}

// #4354: the prerequisite check by type runs the CLI found the way launch finds it. A configured path that cannot be run, or
// settings that cannot be read, is reported as that: the check never falls back to the CLI on PATH, which on a machine with a
// configured path (a test server's mock agent, a pinned install) is the wrong binary, and which this check then RUNS (--version).
func TestCLICheckNeverRunsThePathCLIInsteadOfAConfiguredOne(t *testing.T) {
	marker := pathCLI(t)

	t.Run("a configured path that is missing", func(t *testing.T) {
		h := newSystemHandlerForTest(t)
		missing := filepath.Join(t.TempDir(), "gone", "claude")
		require.NoError(t, store.NewSessionStore(h.db).SetSetting(context.Background(), agent.CLIPathSettingKey(at.Claude), missing))
		w := httptest.NewRecorder()
		h.CLICheck(w, httptest.NewRequest(http.MethodGet, "/api/system/cli-check?type=claude", nil))
		require.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), `"found":false`)
		assert.Contains(t, w.Body.String(), missing, "it names the configured path it could not run")
		assert.Contains(t, w.Body.String(), `"configured":true`)
		_, err := os.Stat(marker)
		assert.True(t, os.IsNotExist(err), "the claude on PATH was run")
	})

	t.Run("settings that cannot be read", func(t *testing.T) {
		h := newSystemHandlerForTest(t)
		require.NoError(t, h.db.Close())
		w := httptest.NewRecorder()
		h.CLICheck(w, httptest.NewRequest(http.MethodGet, "/api/system/cli-check?type=claude", nil))
		assert.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())
		_, err := os.Stat(marker)
		assert.True(t, os.IsNotExist(err), "the claude on PATH was run")
	})

	t.Run("no configured path still checks PATH", func(t *testing.T) {
		// This subtest runs the real PATH CLI (--version) and asserts it ran. A
		// whole-module go test can make the probe's default 3 s bound fire before the
		// fork+exec completes, killing the stub before it records its run (#4818): the
		// marker goes missing though routing was correct. The assertion is that the
		// PATH CLI is the one run, not that it runs within 3 s, so give the probe room
		// (the stand-in prerequisite_analytics_test.go uses for the same reason).
		prevProbe := cliProbeTimeout
		cliProbeTimeout = 30 * time.Second
		t.Cleanup(func() { cliProbeTimeout = prevProbe })

		h := newSystemHandlerForTest(t)
		w := httptest.NewRecorder()
		h.CLICheck(w, httptest.NewRequest(http.MethodGet, "/api/system/cli-check?type=claude", nil))
		require.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), `"found":true`)
		_, err := os.Stat(marker)
		assert.NoError(t, err, "with nothing configured the PATH CLI is the one to check")
	})
}

// The theme generator picks a CLI the way launch does: a configured path is used, a configured path that cannot be run skips
// that CLI (it is never swapped for the one on PATH), and only a CLI with nothing configured is looked up on PATH (#4354).
func TestThemeCLIHonoursConfiguredPaths(t *testing.T) {
	marker := pathCLI(t) // a claude on PATH that records that it ran
	t.Run("configured and runnable", func(t *testing.T) {
		mine := filepath.Join(t.TempDir(), "my-claude")
		require.NoError(t, os.WriteFile(mine, []byte("#!/bin/sh\nexit 0\n"), 0o755))
		p, _ := resolveThemeCLI(map[string]string{agent.CLIPathSettingKey(at.Claude): mine}, "claude")
		assert.Equal(t, mine, p)
	})
	t.Run("configured but missing: not the PATH one", func(t *testing.T) {
		gone := filepath.Join(t.TempDir(), "gone", "claude")
		p, _ := resolveThemeCLI(map[string]string{agent.CLIPathSettingKey(at.Claude): gone}, "claude")
		if p != "" {
			// another CLI with nothing configured may be offered, never the configured claude's PATH stand-in
			assert.NotEqual(t, filepath.Base(p), "claude", "the claude on PATH was chosen over the configured path")
		}
	})
	t.Run("nothing configured: PATH as before", func(t *testing.T) {
		p, args := resolveThemeCLI(map[string]string{}, "claude")
		assert.Equal(t, "claude", filepath.Base(p))
		assert.Contains(t, args, "--print")
	})
	_, err := os.Stat(marker)
	assert.True(t, os.IsNotExist(err), "resolving a CLI never runs it")
}

// Handlers find an agent's CLI through agent.FindCLI (a configured path, then PATH only when none is set). A direct
// exec.LookPath in a handler is how a configured path got swapped for the CLI on PATH (#4354: the prerequisite check and the
// theme generator did). The two that remain are the launch pre-check, which resolves the configured path first, and the test seam.
func TestHandlersDoNotLookUpAgentCLIsOnPathThemselves(t *testing.T) {
	allowed := map[string]int{"sessions_launch_session.go": 1, "system_cli_check.go": 1}
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := parser.ParseFile(token.NewFileSet(), f, nil, 0)
		require.NoError(t, err)
		n := 0
		ast.Inspect(src, func(node ast.Node) bool {
			if sel, ok := node.(*ast.SelectorExpr); ok && sel.Sel.Name == "LookPath" {
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == "exec" {
					n++
				}
			}
			return true
		})
		assert.Equal(t, allowed[f], n, "%s: exec.LookPath in a handler: find agent CLIs with agent.FindCLI(settings, type)", f)
	}
}

// The model listing runs the CLI it finds (a fetch in the background): with a configured path that cannot be run, or settings
// that cannot be read, it lists nothing and runs nothing, rather than asking the CLI on PATH (#4354).
func TestModelListingNeverRunsThePathCLIWhenOneIsConfigured(t *testing.T) {
	marker := pathCLI(t)
	list := func(h *SystemHandler) {
		w := httptest.NewRecorder()
		h.GetAgentCLIModels(w, httptest.NewRequest(http.MethodGet, "/api/agents/models?type=claude", nil))
		require.Equal(t, http.StatusOK, w.Code)
		h.models.Wait()
	}
	t.Run("a configured path that is missing", func(t *testing.T) {
		h := newSystemHandlerForTest(t)
		require.NoError(t, store.NewSessionStore(h.db).SetSetting(context.Background(), agent.CLIPathSettingKey(at.Claude), filepath.Join(t.TempDir(), "gone", "claude")))
		list(h)
		list(h)
	})
	t.Run("settings that cannot be read", func(t *testing.T) {
		h := newSystemHandlerForTest(t)
		require.NoError(t, h.db.Close())
		list(h)
	})
	_, err := os.Stat(marker)
	assert.True(t, os.IsNotExist(err), "the claude on PATH was run by the model listing")
}

// Nothing in the program names an agent CLI to look up or run directly (exec.LookPath("claude"), exec.Command("codex", ...)):
// the binary comes from agent.FindCLI, which honours Settings > CLI paths (#4354, the sweep of cmd, internal and pkg).
func TestNoCodeRunsAnAgentCLIByBareName(t *testing.T) {
	t.Parallel() // no shared state: these ran one after another (#5622)
	agents := map[string]bool{"claude": true, "codex": true, "gemini": true, "agy": true, "pi": true, "antigravity": true}
	isAgent := func(e ast.Expr) bool {
		lit, ok := e.(*ast.BasicLit)
		return ok && lit.Kind == token.STRING && agents[strings.Trim(lit.Value, "`\"")]
	}
	for _, root := range []string{"../../../cmd", "../..", "../../../pkg"} {
		require.NoError(t, filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err == nil && info.IsDir() && filepath.Base(path) == "claudestub" {
				return filepath.SkipDir // test support (#5475): its LookPath("claude") is how it finds out whether a real CLI is reachable, so as to refuse to run there
			}
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			src, perr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			require.NoError(t, perr, path)
			ast.Inspect(src, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || len(call.Args) == 0 {
					return true
				}
				switch sel.Sel.Name {
				case "LookPath", "Command":
					assert.False(t, isAgent(call.Args[0]), "%s: %s of an agent CLI by bare name: use agent.FindCLI(settings, type)", path, sel.Sel.Name)
				case "CommandContext":
					if len(call.Args) > 1 {
						assert.False(t, isAgent(call.Args[1]), "%s: CommandContext of an agent CLI by bare name: use agent.FindCLI(settings, type)", path)
					}
				}
				return true
			})
			return nil
		}))
	}
}
