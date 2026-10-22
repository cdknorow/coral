package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/board/boardtest"
	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
)

// #3534 (A): a NEW board (subscribe, post, a task, a launch, a team launch) must take a name
// by board.NewNamePattern; a board that already exists keeps working under its old name.
func TestNewBoardNamesFollowTheRule(t *testing.T) {
	ctx := context.Background()
	bs, err := boardtest.Open(filepath.Join(t.TempDir(), "board.db"))
	require.NoError(t, err)
	t.Cleanup(func() { bs.Close() })
	// An existing board with a name from before the rule.
	_, err = bs.Subscribe(ctx, "Coral Go-To-Market Team", "Lead", "Lead", "", nil, "all")
	require.NoError(t, err)

	h := NewBoardHandler(bs)
	r := chi.NewRouter()
	r.Post("/api/board/{project}/subscribe", h.Subscribe)
	r.Post("/api/board/{project}/messages", h.PostMessage)
	r.Post("/api/board/{project}/tasks", h.CreateTask)
	do := func(path, project string, body map[string]any) (int, map[string]any) {
		b, _ := json.Marshal(body)
		req := httptest.NewRequest("POST", "/api/board/"+url.PathEscape(project)+path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	sub := map[string]any{"subscriber_id": "Dev", "job_title": "Dev"}
	post := map[string]any{"subscriber_id": "Operator", "as": "Operator", "content": "hi"}
	task := map[string]any{"subscriber_id": "Operator", "title": "t"}

	for _, bad := range []string{"new team", "team.v2", "@homelab/qa", "projects", "-x", "b-aaaaaaaaaa"} {
		for _, c := range []struct {
			path string
			body map[string]any
		}{{"/subscribe", sub}, {"/messages", post}, {"/tasks", task}} {
			code, out := do(c.path, bad, c.body)
			assert.Equal(t, http.StatusBadRequest, code, "%q %s: %v", bad, c.path, out)
			assert.Equal(t, "invalid_board_name", out["code"], "%q %s", bad, c.path)
		}
		exists, err := bs.BoardExists(ctx, bad)
		require.NoError(t, err)
		assert.False(t, exists, "%q: nothing was created", bad)
	}

	// The old board keeps working.
	code, out := do("/subscribe", "Coral Go-To-Market Team", sub)
	assert.Equal(t, http.StatusOK, code, out)
	code, out = do("/messages", "Coral Go-To-Market Team", post)
	assert.Equal(t, http.StatusOK, code, out)

	// A good new name works.
	code, out = do("/subscribe", "go-to-market_2", sub)
	assert.Equal(t, http.StatusOK, code, out)
}

func TestLaunchesRefuseANewBadBoardName(t *testing.T) {
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	bs, err := boardtest.Open(filepath.Join(t.TempDir(), "board.db"))
	require.NoError(t, err)
	t.Cleanup(func() { bs.Close() })
	terminal := newMockTerminal()
	handler := NewSessionsHandler(db, &config.Config{LogDir: t.TempDir()}, nil, terminal, bs)
	ctx := context.Background()

	call := func(fn http.HandlerFunc, path string, body map[string]any) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		fn(rec, req)
		return rec
	}
	rec := call(handler.LaunchTeam, "/api/sessions/launch-team", map[string]any{
		"board_name": "Sprint 42", "working_dir": t.TempDir(),
		"agents":       []map[string]any{{"name": "Dev", "prompt": "x"}},
		"working_mode": map[string]any{"mode": "none", "custom_instructions": "FROM-TEMPLATE"},
	})
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "invalid_board_name")
	wm, err := bs.GetWorkingMode(ctx, "Sprint 42")
	require.NoError(t, err)
	assert.NotEqual(t, "FROM-TEMPLATE", wm.CustomInstructions, "nothing written before the check")
	teams, _ := store.NewTeamStore(db).ListTeams(ctx, "")
	for _, tm := range teams {
		assert.NotEqual(t, "Sprint 42", tm.Name)
	}
	assert.Empty(t, terminal.sentCommands(), "no agent started")

	rec = call(handler.Launch, "/api/sessions/launch", map[string]any{
		"working_dir": t.TempDir(), "agent_type": "claude", "board_name": "auth.feature",
	})
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "invalid_board_name")
	assert.Empty(t, terminal.sentCommands())
	assert.True(t, strings.Contains(rec.Body.String(), "English letters (A-Z), digits"), "the rule says which letters (#297)")
}
