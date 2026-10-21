package routes

import (
	"bytes"
	"context"
	"encoding/json"
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
	"github.com/subgentic/coral/internal/agentsignin"
	at "github.com/subgentic/coral/internal/agenttypes"
	"github.com/subgentic/coral/internal/claudestub"
)

// #5838 (UI/UX's first-run walk): a launch whose CLI is installed nowhere is a 422 with the fixed code cli_not_found, naming the CLI, the install command and the setting to point at an
// existing install, not a 500 (a server fault).
func TestALaunchWhoseCLIIsNotInstalledAnswers422CLINotFound(t *testing.T) {
	rig := setupAgyTestRig(t)
	t.Setenv("PATH", t.TempDir()) // nothing on PATH
	t.Setenv("HOME", t.TempDir()) // and no install in the common places under the home folder
	t.Setenv("USERPROFILE", t.TempDir())

	post := func(body map[string]any) (int, map[string]string) {
		body["working_dir"] = rig.workdir
		b, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		rig.router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/sessions/launch", bytes.NewReader(b)))
		var out map[string]string
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	for _, typ := range []string{at.Claude, at.Codex} {
		code, out := post(map[string]any{"agent_type": typ})
		assert.Equal(t, http.StatusUnprocessableEntity, code, typ)
		assert.Equal(t, "cli_not_found", out["code"], typ)
		assert.Contains(t, out["error"], "CLI not found", typ)
		assert.Contains(t, out["error"], "Install it:", typ)
		assert.Contains(t, out["error"], agent.CLIPathSettingKey(typ), typ+": names the setting to point at an existing install")
	}
}

func TestACLINotFoundErrorNamesTheCLIAndTheSetting(t *testing.T) {
	e := &agent.CLINotFoundError{AgentType: at.Claude, Binary: "claude", InstallCommand: "npm i -g x", InstallURL: "https://example.invalid/x"}
	require.Contains(t, e.Error(), "claude CLI not found. Install it: npm i -g x (the vendor's page: https://example.invalid/x)")
	assert.Contains(t, e.Error(), "Settings > CLI paths (the setting cli_path_claude)")
	assert.Equal(t, "provider_unavailable", classifyLaunchError(e), "the analytics category is unchanged")
	e.InstallURL = ""
	assert.NotContains(t, e.Error(), "vendor's page")
}

// #5838: after the first launch of a Claude agent the server asks once, in the background, whether Claude is signed in (the #5469 status check), so /api/system/status carries
// claude_signed_in on a fresh install, where no background job has asked yet, and the launch card can say "Claude is not signed in". Once per CLI, and never for another agent.
func TestTheFirstClaudeLaunchAsksOnceWhetherClaudeIsSignedIn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake CLI is a shell script")
	}
	claudestub.Isolate(t)
	t.Setenv("ANTHROPIC_API_KEY", "") // a user signed in by a key has no login to ask about
	rig := setupAgyTestRig(t)
	dir := t.TempDir()
	bin, asked := filepath.Join(dir, "claude"), filepath.Join(dir, "asked")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\nif [ \"$1\" = auth ]; then echo ask >> '"+asked+"'; echo '{\"loggedIn\":false}'; exit 1; fi\nexit 0\n"), 0o755))
	require.NoError(t, rig.ss.SetSetting(context.Background(), agent.CLIPathSettingKey(at.Claude), bin))
	agentsignin.Forget("claude", bin)
	signInProbed.Delete(bin)
	t.Cleanup(func() { agentsignin.Forget("claude", bin); signInProbed.Delete(bin) })

	started := time.Now()
	launch := func(typ string) {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"agent_type": typ, "working_dir": rig.workdir})
		rec := httptest.NewRecorder()
		rig.router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/sessions/launch", bytes.NewReader(b)))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	}
	launch(at.Agy)
	time.Sleep(200 * time.Millisecond)
	_, err := os.Stat(asked)
	assert.True(t, os.IsNotExist(err), "another agent's launch asks nothing")

	launch(at.Claude)
	require.Eventually(t, func() bool {
		st, ok := agentsignin.Snapshot("claude") // the newest answer over every Claude path, so other tests' earlier ones do not count
		return ok && !st.SignedIn && !st.CheckedAt.Before(started)
	}, 5*time.Second, 20*time.Millisecond, "the probe's answer is recorded")
	h := newSystemHandlerForTest(t)
	rec := httptest.NewRecorder()
	h.Status(rec, httptest.NewRequest(http.MethodGet, "/api/system/status", nil))
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Equal(t, false, out["claude_signed_in"], "present on a fresh install, and false: the card can say Claude is not signed in")
	assert.NotEmpty(t, out["claude_checked_at"])

	launch(at.Claude)
	time.Sleep(300 * time.Millisecond)
	raw, err := os.ReadFile(asked)
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(string(raw), "ask"), "once per CLI: a second launch asks nothing")
}
