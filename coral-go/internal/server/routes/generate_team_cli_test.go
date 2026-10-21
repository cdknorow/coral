package routes

import (
	"context"
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
	"github.com/subgentic/coral/internal/store"
)

// Team generation runs the configured Claude CLI: with claude only at the
// cli_path_claude setting and not on PATH (the macOS app from Finder) the job
// starts instead of answering 503 "Claude CLI not found" (#3169).
func TestGenerateTeamUsesConfiguredClaudePath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake binary")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir()+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")
	fake := filepath.Join(t.TempDir(), "claude-custom")
	require.NoError(t, os.WriteFile(fake, []byte("#!/bin/sh\necho '{}'\n"), 0o755))
	h := newSystemHandlerForTest(t)
	post := func() int {
		w := httptest.NewRecorder()
		h.GenerateTeam(w, httptest.NewRequest(http.MethodPost, "/api/teams/generate", strings.NewReader(`{"directive":"build a thing"}`)))
		return w.Code
	}
	if agent.FindCLIInCommonPaths("claude") == "" { // a machine with claude in a common location finds it anyway
		assert.Equal(t, http.StatusServiceUnavailable, post(), "no setting and no claude on PATH: 503")
	}
	require.NoError(t, store.NewSessionStore(h.db).SetSetting(context.Background(), agent.CLIPathSettingKey(at.Claude), fake))
	code := post()
	assert.True(t, code >= 200 && code < 300, "the configured path starts the job (got %d)", code)
}

// The prerequisite check by type finds a configured CLI path: no "Claude CLI
// not found" warning when launch would run it fine (#3169).
func TestCLICheckByTypeUsesConfiguredPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake binary")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir()+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")
	fake := filepath.Join(t.TempDir(), "claude-custom")
	require.NoError(t, os.WriteFile(fake, []byte("#!/bin/sh\necho 9.9.9 (Claude Code)\n"), 0o755))
	h := newSystemHandlerForTest(t)
	require.NoError(t, store.NewSessionStore(h.db).SetSetting(context.Background(), agent.CLIPathSettingKey(at.Claude), fake))
	w := httptest.NewRecorder()
	h.CLICheck(w, httptest.NewRequest(http.MethodGet, "/api/system/cli-check?type=claude", nil))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"found":true`)
	assert.Contains(t, w.Body.String(), fake)
}
