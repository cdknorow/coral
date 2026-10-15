package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/store/storetest"
)

// #3536 (the operator's decision): a launch with board_name starts a NEW board with its own
// id and that label; the same name again is 409 board_name_exists with the candidates; then
// board_id joins one and new_board starts another with the same label.
func TestLaunchBoardChoice(t *testing.T) {
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	bs, err := board.NewStore(filepath.Join(t.TempDir(), "board.db"))
	require.NoError(t, err)
	t.Cleanup(func() { bs.Close() })
	terminal := newMockTerminal()
	handler := NewSessionsHandler(db, &config.Config{LogDir: t.TempDir()}, nil, terminal, bs)
	handler.SetTeamStore(store.NewTeamStore(db))
	ctx := context.Background()
	idRe := regexp.MustCompile(`^b-[a-z2-7]{10}$`)

	call := func(fn http.HandlerFunc, body map[string]any) (int, map[string]any) {
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		fn(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	team := func(extra map[string]any) map[string]any {
		body := map[string]any{"working_dir": t.TempDir(), "agents": []map[string]any{{"name": "Dev", "prompt": "build"}}}
		for k, v := range extra {
			body[k] = v
		}
		return body
	}
	boardsLabelled := func(label string) []board.Board {
		b, err := bs.BoardsLabelled(ctx, label)
		require.NoError(t, err)
		return b
	}

	code, out := call(handler.LaunchTeam, team(map[string]any{"board_name": "alpha"}))
	require.Equal(t, http.StatusOK, code, out)
	first := boardsLabelled("alpha")
	require.Len(t, first, 1)
	assert.Regexp(t, idRe, first[0].ID, "a launched team's board has its own id")
	teams, err := store.NewTeamStore(db).ListTeams(ctx, "")
	require.NoError(t, err)
	require.Len(t, teams, 1)
	assert.Equal(t, first[0].ID, teams[0].Name, "the team row is keyed by the board id")
	promptFile := regexp.MustCompile(`'([^']*coral_prompt_[^']*\.txt)'`).FindStringSubmatch(strings.Join(terminal.sentCommands(), "\n"))
	require.NotNil(t, promptFile, "the launch command reads its prompt from a file")
	prompt, err := os.ReadFile(promptFile[1])
	require.NoError(t, err)
	assert.Contains(t, string(prompt), `message board "alpha"`, "the agent's prompt shows the label, not the id")

	// The same name again: a choice, nothing created.
	code, out = call(handler.LaunchTeam, team(map[string]any{"board_name": "alpha"}))
	assert.Equal(t, http.StatusConflict, code, out)
	assert.Equal(t, "board_name_exists", out["code"])
	cands, _ := out["candidates"].([]any)
	require.Len(t, cands, 1)
	assert.Equal(t, first[0].ID, cands[0].(map[string]any)["board_id"])
	assert.Len(t, boardsLabelled("alpha"), 1)

	// Start another with the same label.
	code, out = call(handler.LaunchTeam, team(map[string]any{"board_name": "alpha", "new_board": true}))
	require.Equal(t, http.StatusOK, code, out)
	both := boardsLabelled("alpha")
	require.Len(t, both, 2)
	assert.NotEqual(t, both[0].ID, both[1].ID)

	// A single agent: the name exists (twice) -> 409; board_id joins; an unknown id -> 404.
	code, out = call(handler.Launch, map[string]any{"working_dir": t.TempDir(), "agent_type": "claude", "board_name": "alpha"})
	assert.Equal(t, http.StatusConflict, code, out)
	assert.Len(t, out["candidates"], 2)
	code, out = call(handler.Launch, map[string]any{"working_dir": t.TempDir(), "agent_type": "claude", "board_id": first[0].ID, "display_name": "QA"})
	require.Equal(t, http.StatusOK, code, out)
	subs, err := bs.ListSubscribers(ctx, first[0].ID)
	require.NoError(t, err)
	assert.Len(t, subs, 2, "Dev from the team launch, and the joined agent")
	code, out = call(handler.Launch, map[string]any{"working_dir": t.TempDir(), "agent_type": "claude", "board_id": "b-nosuchboard"})
	assert.Equal(t, http.StatusNotFound, code, out)
	assert.Equal(t, "board_not_found", out["code"])

	// A legacy board (id = name) counts as taken too.
	_, err = bs.Subscribe(ctx, "legacy-team", "Lead", "Lead", "", nil, nil, "all")
	require.NoError(t, err)
	code, out = call(handler.LaunchTeam, team(map[string]any{"board_name": "legacy-team"}))
	assert.Equal(t, http.StatusConflict, code, out)
	code, out = call(handler.LaunchTeam, team(map[string]any{"board_id": "legacy-team"}))
	assert.Equal(t, http.StatusOK, code, out)
}
