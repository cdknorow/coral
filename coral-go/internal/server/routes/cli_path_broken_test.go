//go:build !windows

package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/agent"
	"github.com/cdknorow/coral/internal/agentmodels"
	at "github.com/cdknorow/coral/internal/agenttypes"
	"github.com/cdknorow/coral/internal/store"
)

// #4432 (Security's #4431 SHOULD 4): with a configured claude path that can't be run and a claude on PATH, the model
// list and team generation run nothing: the list stays the curated one and generation answers 503 naming the path.
func TestABrokenConfiguredPathRunsNoOtherCLIInRoutes(t *testing.T) {
	iso := t.TempDir()
	for _, k := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(k, iso)
	}
	t.Setenv("HOME", filepath.Join(iso, "home"))
	t.Setenv("USERPROFILE", filepath.Join(iso, "home"))
	t.Setenv("CORAL_DATA_DIR", filepath.Join(iso, "data"))
	bin := filepath.Join(iso, "bin")
	require.NoError(t, os.MkdirAll(bin, 0o700))
	calls := filepath.Join(iso, "calls.log")
	require.NoError(t, os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\necho \"claude $*\" >> '"+calls+"'\necho '{}'\n"), 0o700))
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	broken := filepath.Join(iso, "moved", "claude-wrapper")

	h := newSystemHandlerForTest(t)
	require.NoError(t, store.NewSessionStore(h.db).SetSetting(context.Background(), agent.CLIPathSettingKey(at.Claude), broken))

	h.models = h.newAgentModels() // the real resolver: settings, then FindCLI
	t.Cleanup(h.models.Wait)
	rec := httptest.NewRecorder()
	h.GetAgentCLIModels(rec, httptest.NewRequest(http.MethodGet, "/api/agents/models?type=claude", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	h.models.Wait()
	rec = httptest.NewRecorder()
	h.GetAgentCLIModels(rec, httptest.NewRequest(http.MethodGet, "/api/agents/models?type=claude", nil))
	var res agentmodels.Result
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res))
	assert.Equal(t, "builtin", res.Source, "the list is the curated one: no CLI was asked")

	w := httptest.NewRecorder()
	h.GenerateTeam(w, httptest.NewRequest(http.MethodPost, "/api/teams/generate", strings.NewReader(`{"directive":"build a thing"}`)))
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Contains(t, w.Body.String(), "claude-wrapper", "the answer names the configured path")

	data, err := os.ReadFile(calls)
	if !os.IsNotExist(err) {
		require.NoError(t, err)
		assert.Empty(t, strings.TrimSpace(string(data)), "the claude on PATH ran in place of the configured one")
	}
}
