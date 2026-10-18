package routes

import (
	"bytes"
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

	"github.com/cdknorow/coral/internal/agentmodels"
	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/store/storetest"
)

// #4065: GET /api/agents/models answers at once from the cache (the curated list until the CLI
// has listed its own), fills it in the background, and POST .../refresh is rate limited with a
// marked 429. An unknown type is a 400.
func TestAgentCLIModelsRoutes(t *testing.T) {
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	h := NewSystemHandler(db, &config.Config{LogDir: t.TempDir()})
	cli := filepath.Join(t.TempDir(), "claude")
	require.NoError(t, os.WriteFile(cli, []byte("#!/bin/sh\n"), 0o755))
	h.models = agentmodels.NewService(func(string) (string, error) { return cli, nil })
	h.models.SetFetcher(func(ctx context.Context, family, path string) ([]agentmodels.Model, string, error) {
		return []agentmodels.Model{{Value: "opus", DisplayName: "Opus 5.5", Efforts: []string{"high"}}}, "account", nil
	})
	t.Cleanup(h.models.Wait)

	get := func(typ string) (int, agentmodels.Result) {
		rec := httptest.NewRecorder()
		h.GetAgentCLIModels(rec, httptest.NewRequest("GET", "/api/agents/models?type="+typ, nil))
		var res agentmodels.Result
		_ = json.Unmarshal(rec.Body.Bytes(), &res)
		return rec.Code, res
	}
	code, res := get("claude")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "builtin", res.Source)
	assert.True(t, res.Refreshing)
	h.models.Wait()
	_, res = get("claude")
	assert.Equal(t, "account", res.Source)
	require.Len(t, res.Models, 1)
	assert.Equal(t, "opus", res.Models[0].Value)
	code, _ = get("terminal")
	assert.Equal(t, http.StatusBadRequest, code)
	code, _ = get("")
	assert.Equal(t, http.StatusBadRequest, code)

	refresh := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.RefreshAgentCLIModels(rec, httptest.NewRequest("POST", "/api/agents/models/refresh?type=claude", nil))
		return rec
	}
	assert.Equal(t, http.StatusAccepted, refresh().Code)
	h.models.Wait()
	rec := refresh()
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Equal(t, "action", rec.Header().Get("X-Coral-Limit"), "a per-action limit, not an overloaded server")
	assert.Contains(t, rec.Body.String(), "models_refresh_limit")
}

// #4065 (Security's condition 3): a launch's model goes to --model as one argument, so a value
// that isn't a model id is refused, on a launch and on a team member, before anything starts.
func TestLaunchRefusesAModelThatIsNotAModelID(t *testing.T) {
	for _, team := range []bool{false, true} {
		for model, ok := range map[string]bool{"opus": true, "claude-opus-5-5": true, "--dangerously-skip-permissions": false, "opus --yolo": false, "a\nb": false} {
			cfg := &config.Config{LogDir: t.TempDir()}
			db, err := storetest.Open(t, filepath.Join(t.TempDir(), "test.db"))
			require.NoError(t, err)
			stubCLIPaths(t, store.NewSessionStore(db))
			terminal := newMockTerminal()
			h := NewSessionsHandler(db, cfg, nil, terminal, nil)
			body := map[string]any{"working_dir": t.TempDir(), "agent_type": "claude", "model": model}
			path, handle := "/api/sessions/launch", h.Launch
			if team {
				body = map[string]any{"board_name": "t", "working_dir": t.TempDir(),
					"agents": []map[string]any{{"name": "Dev", "agent_type": "claude", "model": model}}}
				path, handle = "/api/sessions/launch-team", h.LaunchTeam
			}
			b, _ := json.Marshal(body)
			rec := httptest.NewRecorder()
			handle(rec, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b)))
			cmds := strings.Join(terminal.sentCommands(), "\n")
			if ok {
				assert.Equal(t, http.StatusOK, rec.Code, "%q team=%v: %s", model, team, rec.Body.String())
				assert.Contains(t, cmds, model, "%q team=%v", model, team)
			} else {
				assert.Equal(t, http.StatusBadRequest, rec.Code, "%q team=%v", model, team)
				assert.Contains(t, rec.Body.String(), "model must be a model id")
				assert.Empty(t, cmds, "nothing launched for %q team=%v", model, team)
			}
			db.Close()
		}
	}
}
