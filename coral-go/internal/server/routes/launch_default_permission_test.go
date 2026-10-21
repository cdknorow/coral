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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/agent"
	at "github.com/subgentic/coral/internal/agenttypes"
)

// #5836: a Claude launch that nobody gave a permission mode starts in Auto, not in a bypass of every prompt (and not in prompts nobody is there to answer); what the person chose
// (flags, a saved default) stays theirs; the other CLIs start as before.
func TestAClaudeLaunchWithNoChosenPermissionModeStartsInAuto(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake CLI is a shell script")
	}
	rig := setupAgyTestRig(t)
	ctx := context.Background()
	fakeClaude := filepath.Join(t.TempDir(), "claude")
	require.NoError(t, os.WriteFile(fakeClaude, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	require.NoError(t, rig.ss.SetSetting(ctx, agent.CLIPathSettingKey(at.Claude), fakeClaude))

	launch := func(body map[string]any) (cmd string, stored string) {
		t.Helper()
		body["working_dir"] = rig.workdir
		b, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		rig.router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/sessions/launch", bytes.NewReader(b)))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var res map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res))
		ls, err := rig.ss.GetLiveSession(ctx, res["session_id"].(string))
		require.NoError(t, err)
		require.NotNil(t, ls)
		if ls.PermissionMode != nil {
			stored = *ls.PermissionMode
		}
		rig.term.mu.Lock()
		defer rig.term.mu.Unlock()
		var sent []string
		for name, cmds := range rig.term.sent { // this launch's own commands, not the last of any session's
			if strings.Contains(name, res["session_id"].(string)) {
				sent = cmds
			}
		}
		require.NotEmpty(t, sent, "keys: %v", rig.term.sent)
		return sent[len(sent)-1], stored
	}

	// nothing chosen: Auto, and Auto is what is stored for a restart or a team reset
	cmd, stored := launch(map[string]any{"agent_type": at.Claude})
	assert.Contains(t, cmd, "--permission-mode auto")
	assert.NotContains(t, cmd, "dangerously-skip-permissions")
	assert.NotContains(t, cmd, "bypassPermissions")
	assert.Equal(t, "auto", stored)

	// the person's own flags win: a mode, or a bypass
	cmd, stored = launch(map[string]any{"agent_type": at.Claude, "flags": []string{"--permission-mode", "plan"}})
	assert.Contains(t, cmd, "--permission-mode plan")
	assert.NotContains(t, cmd, "--permission-mode auto")
	assert.Equal(t, "plan", stored)
	cmd, stored = launch(map[string]any{"agent_type": at.Claude, "flags": []string{"--dangerously-skip-permissions"}})
	assert.NotContains(t, cmd, "--permission-mode auto")
	assert.Equal(t, "bypassPermissions", stored)

	// a saved default is the person's choice and applies as before (bypass stays bypass)
	require.NoError(t, rig.ss.SetSetting(ctx, "default_permission_mode", "bypassPermissions"))
	cmd, stored = launch(map[string]any{"agent_type": at.Claude})
	assert.NotContains(t, cmd, "--permission-mode auto")
	assert.Equal(t, "bypassPermissions", stored)
	require.NoError(t, rig.ss.SetSetting(ctx, "default_permission_mode", "default"))
	cmd, stored = launch(map[string]any{"agent_type": at.Claude})
	assert.NotContains(t, cmd, "--permission-mode", "a saved 'default' is the CLI's own prompts")
	assert.Equal(t, "default", stored)

	// another CLI with nothing chosen starts as it did: no Auto
	require.NoError(t, rig.ss.SetSetting(ctx, "default_permission_mode", ""))
	cmd, stored = launch(map[string]any{"agent_type": at.Agy})
	assert.NotContains(t, cmd, "--permission-mode auto")
	assert.Equal(t, "default", stored)
}

func TestTheDefaultLaunchPermissionModeIsAutoForClaudeOnly(t *testing.T) {
	assert.Equal(t, "auto", agent.DefaultLaunchPermissionMode("claude"))
	assert.Equal(t, "auto", agent.DefaultLaunchPermissionMode(""))
	for _, other := range []string{at.Codex, at.Agy, at.Antigravity, at.Gemini, "pi", "terminal"} {
		assert.Equal(t, "default", agent.DefaultLaunchPermissionMode(other), other)
	}
}

// A task run's auto-accept (the switch) adds Claude's Auto mode, not a bypass; a permission flag the run names itself stays; the other CLIs are as before (#5836).
func TestAnAutoAcceptingTaskRunStartsClaudeInAuto(t *testing.T) {
	assert.Equal(t, "--permission-mode auto", taskRunFlags("claude", "", true))
	assert.Equal(t, "--permission-mode auto", taskRunFlags("", "", true), "no agent type: Claude")
	assert.Equal(t, "--permission-mode auto --model x", taskRunFlags("claude", " --model x ", true))
	assert.Equal(t, "--permission-mode plan", taskRunFlags("claude", "--permission-mode plan", true), "the run's own mode stays")
	assert.Equal(t, "--permission-mode=acceptEdits", taskRunFlags("claude", "--permission-mode=acceptEdits", true))
	assert.Equal(t, "--dangerously-skip-permissions", taskRunFlags("claude", "--dangerously-skip-permissions", true), "a bypass the run names itself stays")
	assert.Equal(t, "--permission-mode auto", taskRunFlags("claude", "--permission-mode auto", true), "not added twice")
	assert.Equal(t, "--full-auto", taskRunFlags("codex", "", true))
	assert.Equal(t, "--dangerously-skip-permissions", taskRunFlags("agy", "", true))
	assert.Equal(t, "--dangerously-skip-permissions", taskRunFlags("gemini", "", true))
	assert.Equal(t, "--model x", taskRunFlags("claude", " --model x ", false), "no auto-accept: the caller's flags only")
}

// Through the hub a run in Auto mode keeps its prompt-style safety checks and needs no auto-accept switch; the auto-accept switch itself, and a bypass in the flags, still do.
func TestAutoModeThroughTheHubIsNotAnAutoAcceptRun(t *testing.T) {
	assert.False(t, hasAutoAcceptFlag("--permission-mode auto"), "Auto is not a bypass of the prompts")
	assert.False(t, hasAutoAcceptFlag("--permission-mode plan"))
	assert.True(t, hasAutoAcceptFlag("--permission-mode bypassPermissions"))
	assert.True(t, hasAutoAcceptFlag("--dangerously-skip-permissions"))
	assert.True(t, hasAutoAcceptFlag("--full-auto"))
}
