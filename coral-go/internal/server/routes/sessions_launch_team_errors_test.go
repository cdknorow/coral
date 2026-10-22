package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/tracking"
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

// The branch the launch made for that worktree goes too; a branch a previous run left is kept.
func TestTeamLaunchWhereNoMemberStartsRemovesTheBranchItMade(t *testing.T) {
	server, _, _, ss := setupSessionsTestServerWithConfig(t, config.Load(t.TempDir()))
	stubCLIPaths(t, ss)
	require.NoError(t, ss.SetSetting(context.Background(), "cli_path_claude", filepath.Join(t.TempDir(), "no-such-cli")))
	repo := t.TempDir()
	git := func(args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", repo, "-c", "user.email=t@t", "-c", "user.name=t"}, args...)...).CombinedOutput()
		require.NoError(t, err, string(out))
		return string(out)
	}
	git("init", "-q", "-b", "main")
	git("commit", "-q", "--allow-empty", "-m", "init")
	launch := func() string {
		status, out := postTeam(t, server.URL, map[string]any{"board_name": "br-team", "working_dir": repo, "worktree": true, "base_branch": "main",
			"agent_type": "claude", "agents": []map[string]any{{"name": "A", "prompt": "x"}}})
		require.Equal(t, http.StatusUnprocessableEntity, status, "%v", out)
		return out["board"].(string)
	}
	key := launch()
	assert.NotContains(t, git("branch", "--list", "coral-team/"+key), "coral-team/", "the branch this launch made is removed")

	// A branch that was there before is the user's: kept.
	require.NoError(t, ss.SetSetting(context.Background(), "cli_path_claude", stubCLI(t)))
	status, out := postTeam(t, server.URL, map[string]any{"board_name": "br-team2", "working_dir": t.TempDir(), "agent_type": "claude",
		"agents": []map[string]any{{"name": "A", "prompt": "x"}}})
	require.Equal(t, http.StatusOK, status, "%v", out)
	boardID := out["board_id"].(string)
	git("branch", "coral-team/"+boardID)
	require.NoError(t, ss.SetSetting(context.Background(), "cli_path_claude", filepath.Join(t.TempDir(), "no-such-cli")))
	status, out = postTeam(t, server.URL, map[string]any{"board_id": boardID, "board_name": boardID, "working_dir": repo, "worktree": true, "base_branch": "main",
		"agent_type": "claude", "agents": []map[string]any{{"name": "B", "prompt": "y"}}})
	require.Equal(t, http.StatusUnprocessableEntity, status, "%v", out)
	assert.Contains(t, git("branch", "--list", "coral-team/"+boardID), "coral-team/"+boardID, "a branch that was already there stays")
}

// #5762: the rollback of a launch that started nobody deletes the board it made only while nothing else has used it: a member that joined between the
// failure and the rollback (an agent joins a board by itself, or another launch on it) keeps the board, with the member.
func TestTeamLaunchRollbackKeepsABoardAMemberJoinedMeanwhile(t *testing.T) {
	_, h, _, _ := setupSessionsTestServerWithConfig(t, config.Load(t.TempDir()))
	ctx := context.Background()
	rollBack := func(id string, made bool) {
		h.rollBackTeamLaunch(ctx, launchTeamRequest{BoardName: id}, 0, "", false, made)
	}

	// nothing joined: the board this request made is taken back
	unused, err := h.bs.CreateBoard(ctx, "raced", "tester")
	require.NoError(t, err)
	rollBack(unused.ID, true)
	_, err = h.bs.GetBoard(ctx, unused.ID)
	assert.Error(t, err, "an unused board is deleted")

	// a member joined meanwhile: the board stays, with the member
	used, err := h.bs.CreateBoard(ctx, "raced too", "tester")
	require.NoError(t, err)
	_, err = h.bs.Subscribe(ctx, used.ID, "Late", "Developer", "claude-raced-late", nil, "all")
	require.NoError(t, err)
	rollBack(used.ID, true)
	_, err = h.bs.GetBoard(ctx, used.ID)
	assert.NoError(t, err, "the board a member joined is kept")
	subs, err := h.bs.ListSubscribers(ctx, used.ID)
	require.NoError(t, err)
	assert.Len(t, subs, 1, "and so is the member")

	// a board this request did not make is never deleted, used or not
	theirs, err := h.bs.CreateBoard(ctx, "theirs", "tester")
	require.NoError(t, err)
	rollBack(theirs.ID, false)
	_, err = h.bs.GetBoard(ctx, theirs.ID)
	assert.NoError(t, err)
}

// A configured CLI path that can't be run (an error carrying a Hub text, #115) is a setup problem: a team whose members all fail on it is a 422,
// not a 500.
type brokenCLIPathError struct{}

func (brokenCLIPathError) Error() string   { return "claude CLI path \"x\" can't be run" }
func (brokenCLIPathError) HubText() string { return "the claude CLI path set in Settings can't be run" }

func TestAConfiguredCLIPathThatCantBeRunIsASetupProblem(t *testing.T) {
	assert.Equal(t, tracking.FailProviderUnrunnable, classifyLaunchError(brokenCLIPathError{}))
	assert.Equal(t, tracking.FailProviderUnrunnable, classifyLaunchError(fmt.Errorf("launching: %w", brokenCLIPathError{})), "wrapped too")
	assert.NotEqual(t, tracking.FailProviderUnrunnable, classifyLaunchError(errors.New("something else broke")))
	assert.True(t, allConfigurationFailures([]string{classifyLaunchError(brokenCLIPathError{})}))
}
