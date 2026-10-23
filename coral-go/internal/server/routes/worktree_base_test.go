package routes

import (
	"context"
	"github.com/go-chi/chi/v5"
	"github.com/subgentic/coral/internal/store"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/config"
)

func repoOn(t *testing.T, branch string, commit bool) (string, func(args ...string) string) {
	t.Helper()
	repo := t.TempDir()
	git := func(args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", repo, "-c", "user.email=t@t", "-c", "user.name=t"}, args...)...).CombinedOutput()
		require.NoError(t, err, string(out))
		return string(out)
	}
	git("init", "-q", "-b", branch)
	if commit {
		git("commit", "-q", "--allow-empty", "-m", "init")
	}
	return repo, git
}

// #6701 (QA's #6692): a team launch with a worktree in a repository on master (or any branch but main) used to fail with "invalid reference: main" because the base branch defaulted to main.
func TestTeamLaunchWithAWorktreeInARepositoryNotOnMain(t *testing.T) {
	for _, base := range []string{"", "main"} { // nothing named, and the old default
		for _, branch := range []string{"master", "trunk"} {
			cfg := config.Load(t.TempDir())
			server, _, _, ss := setupSessionsTestServerWithConfig(t, cfg)
			stubCLIPaths(t, ss)
			require.NoError(t, ss.SetSetting(context.Background(), "cli_path_claude", stubCLI(t)))
			repo, git := repoOn(t, branch, true)
			body := map[string]any{"board_name": "wt-" + branch, "working_dir": repo, "worktree": true, "agent_type": "claude", "agents": []map[string]any{{"name": "A", "prompt": "x"}}}
			if base != "" {
				body["base_branch"] = base
			}
			status, out := postTeam(t, server.URL, body)
			require.Equal(t, http.StatusOK, status, "base %q on %s: %v", base, branch, out)
			id := out["board_id"].(string)
			assert.Contains(t, git("worktree", "list"), filepath.Join("worktrees", id), "the worktree is made")
			assert.Contains(t, git("branch", "--list", "coral-team/"+id), "coral-team/"+id, "on the team's branch, started from "+branch)
		}
	}
}

func TestTeamLaunchWithAWorktreeInARepositoryWithNoCommitsSaysSo(t *testing.T) {
	server, _, _, ss := setupSessionsTestServerWithConfig(t, config.Load(t.TempDir()))
	stubCLIPaths(t, ss)
	require.NoError(t, ss.SetSetting(context.Background(), "cli_path_claude", stubCLI(t)))
	repo, _ := repoOn(t, "master", false)
	status, out := postTeam(t, server.URL, map[string]any{"board_name": "wt-empty", "working_dir": repo, "worktree": true, "agent_type": "claude", "agents": []map[string]any{{"name": "A", "prompt": "x"}}})
	assert.GreaterOrEqual(t, status, 400, "%v", out)
	assert.Contains(t, strings.ToLower(toJSON(out)), "no commits yet", "one clear reason, not git's invalid reference")
	assert.NotContains(t, toJSON(out), "invalid reference")
}

func toJSON(m map[string]any) string {
	var b strings.Builder
	for k, v := range m {
		b.WriteString(k)
		b.WriteString("=")
		if s, ok := v.(string); ok {
			b.WriteString(s)
		}
		b.WriteString(";")
	}
	return b.String()
}

// launchWorktreeTeam launches a one-agent team with a worktree in a fresh repository (on master) and returns the repository's git, the board's id and the worktree's folder.
func launchWorktreeTeam(t *testing.T, serverURL string, cfg *config.Config, board string) (git func(args ...string) string, id, wt string) {
	t.Helper()
	repo, git := repoOn(t, "master", true)
	status, out := postTeam(t, serverURL, map[string]any{"board_name": board, "working_dir": repo, "worktree": true, "agent_type": "claude", "agents": []map[string]any{{"name": "A", "prompt": "x"}}})
	require.Equal(t, http.StatusOK, status, "%v", out)
	id = out["board_id"].(string)
	return git, id, filepath.Join(cfg.CoralDir(), "worktrees", id)
}

// teamServer serves launch, stop and resurrect of a team over a repository's worktree, backed by a team store.
func teamServer(t *testing.T) (url string, cfg *config.Config) {
	t.Helper()
	cfg = config.Load(t.TempDir())
	_, h, _, ss := setupSessionsTestServerWithConfig(t, cfg)
	h.SetTeamStore(store.NewTeamStore(h.db))
	stubCLIPaths(t, ss)
	require.NoError(t, ss.SetSetting(context.Background(), "cli_path_claude", stubCLI(t)))
	r := chi.NewRouter()
	r.Use(testIdentity(h.db))
	r.Post("/api/sessions/launch-team", h.LaunchTeam)
	r.Post("/api/sessions/live/team/{boardName}/kill", h.KillTeam)
	r.Post("/api/teams/detail/{name}/resurrect", h.ResurrectTeam)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv.URL, cfg
}

func apiPost(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader("{}"))
	require.NoError(t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// #6701 (QA #6692): stopping a team takes its worktree away: the folder and the repository's registration of it. The removal ran in the folder that holds the worktrees, which is no repository, so
// it only logged an error and left both.
func TestStopTeamRemovesItsWorktree(t *testing.T) {
	url, cfg := teamServer(t)
	git, id, wt := launchWorktreeTeam(t, url, cfg, "stopwt")
	require.DirExists(t, wt)
	require.Contains(t, git("worktree", "list"), id)

	status, body := apiPost(t, url+"/api/sessions/live/team/"+id+"/kill")
	require.Equal(t, http.StatusOK, status, body)
	require.Eventually(t, func() bool {
		_, err := os.Stat(wt)
		return os.IsNotExist(err) && !strings.Contains(git("worktree", "list"), id)
	}, 20*time.Second, 50*time.Millisecond, "the folder and the registration are gone")
	assert.Contains(t, git("branch", "--list", "coral-team/"+id), "coral-team/"+id, "the branch, with its commits, stays")
}
