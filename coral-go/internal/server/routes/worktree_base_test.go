package routes

import (
	"context"
	"encoding/json"
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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/gitutil"
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

// stopTeam stops the team over the API and returns what the answer says of its worktree.
func stopTeam(t *testing.T, url, id string) map[string]any {
	t.Helper()
	status, body := apiPost(t, url+"/api/sessions/live/team/"+id+"/kill")
	require.Equal(t, http.StatusOK, status, body)
	var out struct {
		Worktree map[string]any `json:"worktree"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &out))
	require.NotNil(t, out.Worktree, "the stop answers about the worktree: %s", body)
	return out.Worktree
}

// #6701 (QA #6692): stopping a team takes its worktree away when nothing in it is unsaved: the folder, the repository's registration and the branch it held nothing on. The removal ran in the folder
// that holds the worktrees, which is no repository, so it only logged an error and left all three.
func TestStopTeamRemovesACleanWorktree(t *testing.T) {
	url, cfg := teamServer(t)
	git, id, wt := launchWorktreeTeam(t, url, cfg, "stopwt")
	require.DirExists(t, wt)
	require.Contains(t, git("worktree", "list"), id)

	got := stopTeam(t, url, id)
	assert.Equal(t, "removed", got["state"], "%v", got)
	assert.Equal(t, wt, got["path"])
	assert.NoDirExists(t, wt)
	assert.NotContains(t, git("worktree", "list"), id)
	assert.NotContains(t, git("branch", "--list", "coral-team/"+id), id, "a branch with nothing of its own goes with it")
}

// Coral never deletes a person's work without asking: a worktree with uncommitted or untracked changes stays, with its branch, and the answer says why.
func TestStopTeamKeepsAWorktreeWithUnsavedWork(t *testing.T) {
	for _, tc := range []struct{ name, file string }{{"untracked", "new.txt"}, {"modified", "README"}} {
		url, cfg := teamServer(t)
		git, id, wt := launchWorktreeTeam(t, url, cfg, "keep-"+tc.name)
		if tc.name == "modified" {
			require.NoError(t, os.WriteFile(filepath.Join(wt, tc.file), []byte("v1"), 0o644))
			git("-C", wt, "add", tc.file)
			git("-C", wt, "commit", "-q", "-m", "readme")
		}
		require.NoError(t, os.WriteFile(filepath.Join(wt, tc.file), []byte("unsaved work"), 0o644))

		got := stopTeam(t, url, id)
		assert.Equal(t, "kept", got["state"], "%s: %v", tc.name, got)
		assert.Equal(t, "it has uncommitted changes", got["reason"], tc.name)
		b, err := os.ReadFile(filepath.Join(wt, tc.file))
		require.NoError(t, err, tc.name)
		assert.Equal(t, "unsaved work", string(b), tc.name)
		assert.Contains(t, git("worktree", "list"), id, tc.name)
		assert.Contains(t, git("branch", "--list", "coral-team/"+id), id, tc.name)
	}
}

// A clean worktree whose branch has commits that are merged nowhere goes, and the branch stays.
func TestStopTeamKeepsABranchWithUnmergedCommits(t *testing.T) {
	url, cfg := teamServer(t)
	git, id, wt := launchWorktreeTeam(t, url, cfg, "keepbranch")
	require.NoError(t, os.WriteFile(filepath.Join(wt, "work.txt"), []byte("work"), 0o644))
	git("-C", wt, "add", "work.txt")
	git("-C", wt, "commit", "-q", "-m", "work")

	got := stopTeam(t, url, id)
	assert.Equal(t, "removed", got["state"], "%v", got)
	assert.Equal(t, "its branch is kept: it has commits that are not merged", got["reason"])
	assert.NoDirExists(t, wt)
	assert.Contains(t, git("branch", "--list", "coral-team/"+id), id, "the commits stay reachable")
}

// #6701: a base branch the repository does not have is a 400 with one plain sentence, the field, and the branches to pick from (Frontend's #6707 shape).
func TestTeamLaunchWithAMissingBaseBranchNamesTheBranches(t *testing.T) {
	url, _ := teamServer(t)
	repo, _ := repoOn(t, "master", true)
	status, out := postTeam(t, url, map[string]any{"board_name": "nobranch", "working_dir": repo, "worktree": true, "base_branch": "release", "agent_type": "claude", "agents": []map[string]any{{"name": "A", "prompt": "x"}}})
	assert.Equal(t, http.StatusBadRequest, status, "%v", out)
	assert.Equal(t, "The base branch release does not exist in "+repo+". Its branches: master.", out["error"])
	assert.Equal(t, "base_branch_missing", out["code"])
	assert.Equal(t, "base_branch", out["field"])
	assert.Equal(t, []any{"master"}, out["branches"])
	assert.Equal(t, "master", out["default_branch"])
}

// #6701: a stopped team resurrects into a worktree on its own branch (coral-team/<board>), with the commits the branch kept: it used to add the base branch itself, which is checked out in the repository.
func TestResurrectTeamRecreatesItsWorktreeOnItsOwnBranch(t *testing.T) {
	url, cfg := teamServer(t)
	git, id, wt := launchWorktreeTeam(t, url, cfg, "resurrectwt")
	require.NoError(t, os.WriteFile(filepath.Join(wt, "work.txt"), []byte("work"), 0o644))
	git("-C", wt, "add", "work.txt")
	git("-C", wt, "commit", "-q", "-m", "work")
	stopTeam(t, url, id)
	require.NoDirExists(t, wt)

	status, body := apiPost(t, url+"/api/teams/detail/"+id+"/resurrect")
	require.Equal(t, http.StatusOK, status, body)
	assert.FileExists(t, filepath.Join(wt, "work.txt"), "on the team's branch, with its commits")
	assert.Equal(t, "coral-team/"+id, strings.TrimSpace(git("-C", wt, "rev-parse", "--abbrev-ref", "HEAD")))
}

// A worktree folder deleted by hand leaves its registration: the resurrect (and so a launch) prunes it and goes on.
func TestResurrectTeamAfterTheWorktreeFolderWasDeleted(t *testing.T) {
	url, cfg := teamServer(t)
	git, id, wt := launchWorktreeTeam(t, url, cfg, "resurrectgone")
	status, body := apiPost(t, url+"/api/sessions/live/team/"+id+"/kill")
	require.Equal(t, http.StatusOK, status, body)
	git("worktree", "add", "-b", "coral-team/"+id, "--", wt, "master") // the stop's removal undone: a registration and a folder
	require.NoError(t, os.RemoveAll(wt))                               // and the folder deleted by hand: only the registration is left

	status, body = apiPost(t, url+"/api/teams/detail/"+id+"/resurrect")
	require.Equal(t, http.StatusOK, status, body)
	assert.DirExists(t, wt)
}

// A folder that is not empty and no worktree is the one thing refused, in one line that says what to do.
func TestResurrectTeamOverANonEmptyLeftoverFolder(t *testing.T) {
	url, cfg := teamServer(t)
	_, id, wt := launchWorktreeTeam(t, url, cfg, "resurrectfull")
	stopTeam(t, url, id)
	require.NoError(t, os.MkdirAll(wt, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(wt, "stray.txt"), []byte("x"), 0o644))

	status, body := apiPost(t, url+"/api/teams/detail/"+id+"/resurrect")
	assert.Equal(t, http.StatusBadRequest, status, body)
	assert.Contains(t, body, "left over from an earlier run and is not empty")
	assert.NotContains(t, body, "\\n")
	assert.FileExists(t, filepath.Join(wt, "stray.txt"), "nothing of the operator's is touched")
}

// #6701: a team whose board name git refuses in a branch name still gets its worktree: the folder keeps the name, the branch is a slug of it.
func TestTeamWorktreeForABoardNameGitRefusesInABranch(t *testing.T) {
	_, h, _, _ := setupSessionsTestServerWithConfig(t, config.Load(t.TempDir()))
	repo, git := repoOn(t, "master", true)
	for _, name := range []string{"my team", "wip~1", "what?"} {
		wd, path, created, failure := h.createTeamWorktree(t.Context(), launchTeamRequest{BoardName: name, WorkingDir: repo, Worktree: true})
		if failure != nil && strings.Contains(failure.Error(), "cannot name a worktree folder") {
			continue // a name the folder check refuses first (#5305) is not this case
		}
		require.NoError(t, failure, "%q", name)
		assert.Equal(t, filepath.Join(h.cfg.CoralDir(), "worktrees", name), wd)
		assert.Equal(t, wd, path)
		assert.True(t, created)
		assert.Contains(t, git("branch", "--list", gitutil.TeamBranch(name)), "coral-team/", "%q", name)
	}
}
