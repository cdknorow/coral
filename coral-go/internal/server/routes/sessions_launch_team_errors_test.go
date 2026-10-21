package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/store"
)

// postTeam posts a team launch and returns the status and the decoded answer.
func postTeam(t *testing.T, base string, body map[string]any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(base+"/api/sessions/launch-team", "application/json", bytes.NewReader(b))
	require.NoError(t, err)
	defer resp.Body.Close()
	var out map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	return resp.StatusCode, out
}

func memberErrors(out map[string]any) (started, failed int) {
	for _, m := range out["agents"].([]any) {
		if _, bad := m.(map[string]any)["error"]; bad {
			failed++
		} else {
			started++
		}
	}
	return
}

// A team in which no member starts is an error, not ok:true over an empty team (#122): a setup problem (no CLI where the setting points)
// is a 422 with each member's error, and what the request made (the board, the team record) is taken back.
func TestTeamLaunchWhereNoMemberStartsIsAnErrorAndLeavesNothingBehind(t *testing.T) {
	server, h, _, ss := setupSessionsTestServerWithConfig(t, config.Load(t.TempDir()))
	h.SetTeamStore(store.NewTeamStore(h.db)) // the launch records a team; the rollback removes it
	stubCLIPaths(t, ss)
	missing := filepath.Join(t.TempDir(), "no-such-cli")
	require.NoError(t, ss.SetSetting(context.Background(), "cli_path_claude", missing))

	status, out := postTeam(t, server.URL, map[string]any{"board_name": "all-fail", "working_dir": t.TempDir(), "agent_type": "claude",
		"agents": []map[string]any{{"name": "A", "prompt": "x"}, {"name": "B", "prompt": "y"}}})
	assert.Equal(t, http.StatusUnprocessableEntity, status, "%v", out)
	assert.Equal(t, false, out["ok"])
	assert.Equal(t, "team_launch_failed", out["code"])
	assert.Equal(t, true, out["rolled_back"])
	assert.Contains(t, out["error"], "A:")
	assert.Contains(t, out["error"], "B:")
	started, failed := memberErrors(out)
	assert.Equal(t, 0, started)
	assert.Equal(t, 2, failed, "each member's error is in the answer")

	teams, err := h.teamStore.ListTeams(context.Background(), "")
	require.NoError(t, err)
	assert.Empty(t, teams, "no empty team row is left")
	labelled, err := h.bs.BoardsLabelled(context.Background(), "all-fail")
	require.NoError(t, err)
	assert.Empty(t, labelled, "the board this request made is taken back")
}

// A board that was already there is never deleted by a launch that starts nobody on it.
func TestTeamLaunchWhereNoMemberStartsKeepsABoardThatWasThere(t *testing.T) {
	server, h, _, ss := setupSessionsTestServerWithConfig(t, config.Load(t.TempDir()))
	stubCLIPaths(t, ss)
	status, out := postTeam(t, server.URL, map[string]any{"board_name": "kept", "working_dir": t.TempDir(), "agent_type": "claude",
		"agents": []map[string]any{{"name": "A", "prompt": "x"}}})
	require.Equal(t, http.StatusOK, status, "%v", out)
	boardID := out["board_id"].(string)

	require.NoError(t, ss.SetSetting(context.Background(), "cli_path_claude", filepath.Join(t.TempDir(), "no-such-cli")))
	status, out = postTeam(t, server.URL, map[string]any{"board_id": boardID, "board_name": boardID, "working_dir": t.TempDir(), "agent_type": "claude",
		"agents": []map[string]any{{"name": "C", "prompt": "z"}}})
	assert.Equal(t, http.StatusUnprocessableEntity, status, "%v", out)
	_, err := h.bs.GetBoard(context.Background(), boardID)
	assert.NoError(t, err, "the board that was already there stays")
}

// Some members start and some fail: still ok:true with each failure in its entry, and now said to be partial.
func TestTeamLaunchWherePartOfTheTeamStartsIsPartial(t *testing.T) {
	server, _, _, ss := setupSessionsTestServerWithConfig(t, config.Load(t.TempDir()))
	stubCLIPaths(t, ss)
	require.NoError(t, ss.SetSetting(context.Background(), "cli_path_codex", filepath.Join(t.TempDir(), "no-such-cli")))

	status, out := postTeam(t, server.URL, map[string]any{"board_name": "half", "working_dir": t.TempDir(),
		"agents": []map[string]any{{"name": "Good", "agent_type": "claude", "prompt": "x"}, {"name": "Bad", "agent_type": "codex", "prompt": "y"}}})
	assert.Equal(t, http.StatusOK, status, "%v", out)
	assert.Equal(t, true, out["ok"])
	assert.Equal(t, true, out["partial"])
	assert.EqualValues(t, 1, out["started"])
	assert.EqualValues(t, 1, out["failed"])
	started, failed := memberErrors(out)
	assert.Equal(t, 1, started)
	assert.Equal(t, 1, failed)
}

// Every member started: ok:true, no partial.
func TestTeamLaunchWhereEveryMemberStartsIsNotPartial(t *testing.T) {
	server, _, _, ss := setupSessionsTestServerWithConfig(t, config.Load(t.TempDir()))
	stubCLIPaths(t, ss)
	status, out := postTeam(t, server.URL, map[string]any{"board_name": "all-ok", "working_dir": t.TempDir(), "agent_type": "claude",
		"agents": []map[string]any{{"name": "A", "prompt": "x"}, {"name": "B", "prompt": "y"}}})
	assert.Equal(t, http.StatusOK, status, "%v", out)
	assert.Equal(t, true, out["ok"])
	assert.NotContains(t, out, "partial")
	started, failed := memberErrors(out)
	assert.Equal(t, 2, started)
	assert.Equal(t, 0, failed)
}

// A team whose members have no names starts nobody: a 400, not ok:true.
func TestTeamLaunchWithNoNamedMemberIsRefused(t *testing.T) {
	server, _, _, ss := setupSessionsTestServerWithConfig(t, config.Load(t.TempDir()))
	stubCLIPaths(t, ss)
	status, out := postTeam(t, server.URL, map[string]any{"board_name": "nameless", "working_dir": t.TempDir(), "agent_type": "claude",
		"agents": []map[string]any{{"prompt": "x"}}})
	assert.Equal(t, http.StatusBadRequest, status, "%v", out)
	assert.Equal(t, "no_named_agents", out["code"])
}

// The worktree a team launch made for a team that started nobody is removed, so the same launch can be made again.
func TestTeamLaunchWhereNoMemberStartsRemovesItsWorktree(t *testing.T) {
	cfg := config.Load(t.TempDir())
	server, _, _, ss := setupSessionsTestServerWithConfig(t, cfg)
	stubCLIPaths(t, ss)
	require.NoError(t, ss.SetSetting(context.Background(), "cli_path_claude", filepath.Join(t.TempDir(), "no-such-cli")))
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init"}} {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	launch := func() (int, map[string]any) {
		return postTeam(t, server.URL, map[string]any{"board_name": "wt-team", "working_dir": repo, "worktree": true, "base_branch": "main",
			"agent_type": "claude", "agents": []map[string]any{{"name": "A", "prompt": "x"}}})
	}
	status, out := launch()
	require.Equal(t, http.StatusUnprocessableEntity, status, "%v", out)
	boardKey := out["board"].(string)
	_, err := os.Stat(filepath.Join(cfg.CoralDir(), "worktrees", boardKey))
	assert.True(t, os.IsNotExist(err), "the empty team's worktree folder is gone: %v", err)
}
