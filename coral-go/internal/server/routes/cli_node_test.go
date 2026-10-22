//go:build !windows

package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/agent"
	at "github.com/subgentic/coral/internal/agenttypes"
	"github.com/subgentic/coral/internal/store"
)

// #5899: an npm-installed CLI (a "#!/usr/bin/env node" script) under the macOS app's minimal PATH. The launch line, the CLI check and the version run it with its own node, per
// child and never process-wide.

// npmCodex is the shape Codex Test used for round 1: ~/.local/bin/codex linking to ~/prefix/bin/codex.js, the node beside the script in ~/prefix/bin (not on PATH).
func npmCodex(t *testing.T, withNode bool) (link string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "/usr/bin:/bin")
	if _, err := exec.LookPath("node"); err == nil {
		t.Skip("system node installed")
	}
	linkDir, prefix := filepath.Join(home, ".local", "bin"), filepath.Join(home, "prefix", "bin")
	for _, d := range []string{linkDir, prefix} {
		require.NoError(t, os.MkdirAll(d, 0o755))
	}
	shim := filepath.Join(prefix, "codex.js")
	require.NoError(t, os.WriteFile(shim, []byte("#!/usr/bin/env node\n"), 0o755))
	if withNode {
		require.NoError(t, os.WriteFile(filepath.Join(prefix, "node"), []byte("#!/bin/sh\necho NODE_PREPARED codex-cli 0.163.0\n"), 0o755))
	}
	link = filepath.Join(linkDir, "codex")
	require.NoError(t, os.Symlink(shim, link))
	return link
}

// Codex Test's route control (#5899 round 1): the default (unconfigured) resolution found the npm CLI, whose --version then exited 127 for want of node. The launch line now carries
// the node, so the CLI the default resolution chose runs.
func TestDefaultLaunchOfAnNpmCLIRunsWithItsOwnNode(t *testing.T) {
	link := npmCodex(t, true)
	cli, err := resolveLaunchCLI("codex", "")
	require.NoError(t, err)
	require.Equal(t, link, cli)

	line, err := launchCommand(agent.GetAgent(at.Codex), agent.LaunchParams{SessionID: "s1", SessionName: "codex-s1", WorkingDir: t.TempDir(), CLIPath: cli, CoralDir: t.TempDir()})
	require.NoError(t, err)
	assert.Contains(t, line, "command -v node", "the launch line carries the node fallback")
	nodeDir, err := filepath.EvalSymlinks(filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(link))), "prefix", "bin"))
	require.NoError(t, err)
	assert.Contains(t, line, nodeDir, "and the folder of the CLI's own node")

	// What the agent's shell runs: under the app's PATH, the CLI's node answers.
	run := exec.Command("/bin/sh", "-c", agent.WrapWithCLINode(cli+" --version", cli))
	run.Env = []string{"PATH=/usr/bin:/bin"}
	out, err := run.CombinedOutput()
	require.NoError(t, err, string(out))
	assert.Contains(t, string(out), "NODE_PREPARED")
	assert.Equal(t, "/usr/bin:/bin", os.Getenv("PATH"), "the process PATH is not touched")
}

// With no node anywhere the terminal says so (and the CLI check does), instead of "env: node: No such file or directory".
func TestLaunchOfAnNpmCLIWithNoNodeSaysNodeNotFound(t *testing.T) {
	link := npmCodex(t, false)
	line := agent.WrapWithCLINode(link+" --version", link)
	run := exec.Command("/bin/sh", "-c", line)
	run.Env = []string{"PATH=/usr/bin:/bin"}
	out, err := run.CombinedOutput()
	assert.Error(t, err)
	assert.Contains(t, string(out), "node not found")
	assert.NotContains(t, string(out), "No such file")
}

func TestCLICheckRunsAnNpmCLIWithItsOwnNodeAndSaysWhenThereIsNone(t *testing.T) {
	check := func(t *testing.T, link string) map[string]any {
		h := newSystemHandlerForTest(t)
		require.NoError(t, store.NewSessionStore(h.db).SetSetting(context.Background(), agent.CLIPathSettingKey(at.Codex), link))
		w := httptest.NewRecorder()
		h.CLICheck(w, httptest.NewRequest(http.MethodGet, "/api/system/cli-check?type=codex", nil))
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var out map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
		return out
	}
	out := check(t, npmCodex(t, true))
	assert.Equal(t, "ok", out["probe"], out)
	assert.Contains(t, out["version"], "0.163.0")
	assert.NotContains(t, out, "node_error")
	assert.Equal(t, "/usr/bin:/bin", os.Getenv("PATH"))

	out = check(t, npmCodex(t, false))
	assert.Equal(t, "failed", out["probe"], out)
	assert.True(t, strings.HasPrefix(out["node_error"].(string), "node not found"), out)
}
