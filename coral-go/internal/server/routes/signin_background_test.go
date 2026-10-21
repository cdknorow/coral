//go:build unix

package routes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/agentsignin"
	"github.com/subgentic/coral/internal/claudestub"
)

// #5469: the routes that run Claude for a person (team generation, theme generation) never start a signed-out Claude: its sign-in flow would hold the login. The stand-in
// answers the status command and records any other start.
func signedOutClaude(t *testing.T) (bin, ran string) {
	t.Helper()
	claudestub.Isolate(t)
	dir := t.TempDir()
	bin, ran = filepath.Join(dir, "claude"), filepath.Join(dir, "ran")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\nif [ \"$1\" = auth ]; then echo '{\"loggedIn\":false}'; exit 1; fi\necho started >> '"+ran+"'\n"), 0o755))
	agentsignin.Forget("claude", bin)
	t.Cleanup(func() { agentsignin.Forget("claude", bin) })
	return bin, ran
}

func TestTeamGenerationDoesNotStartASignedOutClaude(t *testing.T) {
	t.Parallel() // no shared state: these ran one after another (#5622)
	bin, ran := signedOutClaude(t)
	result, msg := runTeamGeneration(t.Context(), bin, "prompt", "")
	assert.Nil(t, result)
	assert.Equal(t, "Claude is not signed in", msg)
	_, err := os.Stat(ran)
	assert.True(t, os.IsNotExist(err), "Claude was never started")
}

func TestThemeGenerationDoesNotStartASignedOutClaude(t *testing.T) {
	bin, ran := signedOutClaude(t)
	// Claude is the only CLI on this PATH, found by its name.
	t.Setenv("PATH", filepath.Dir(bin))
	h := &ThemesHandler{}
	req := httptest.NewRequest(http.MethodPost, "/api/themes/generate", strings.NewReader(`{"description":"calm ocean","agent_type":"claude"}`))
	rec := httptest.NewRecorder()
	h.GenerateTheme(rec, req)
	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "Claude is not signed in")
	assert.Contains(t, rec.Body.String(), "not_signed_in")
	_, err := os.Stat(ran)
	assert.True(t, os.IsNotExist(err), "Claude was never started")
}

// The system status says Claude is signed out once a background check found it so (one quiet line, #5469) and clears once a check finds it signed in (that nothing is said before
// any check is the agentsignin Snapshot test).
func TestSystemStatusCarriesClaudesSignIn(t *testing.T) {
	t.Parallel() // no shared state: these ran one after another (#5622)
	status := func(h *SystemHandler) map[string]any {
		rec := httptest.NewRecorder()
		h.Status(rec, httptest.NewRequest(http.MethodGet, "/api/system/status", nil))
		require.Equal(t, http.StatusOK, rec.Code)
		var out map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		return out
	}
	h := newSystemHandlerForTest(t)
	bin, _ := signedOutClaude(t)
	require.Error(t, agentsignin.RequireFresh(t.Context(), "claude", bin))
	out := status(h)
	assert.Equal(t, false, out["claude_signed_in"])
	assert.NotEmpty(t, out["claude_checked_at"])

	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\necho '{\"loggedIn\":true}'\n"), 0o755))
	require.NoError(t, agentsignin.RequireFresh(t.Context(), "claude", bin))
	assert.Equal(t, true, status(h)["claude_signed_in"], "cleared by a check that finds it signed in")
}
