package routes

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/gitutil"
	"github.com/subgentic/coral/internal/store"
)

func listWorktrees(t *testing.T, url string) []map[string]any {
	t.Helper()
	resp, err := http.Get(url + "/api/worktrees")
	require.NoError(t, err)
	defer resp.Body.Close()
	var out struct {
		Worktrees []map[string]any `json:"worktrees"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	return out.Worktrees
}

func removeWorktree(t *testing.T, url, id string) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"id": id})
	resp, err := http.Post(url+"/api/worktrees/remove", "application/json", strings.NewReader(string(b)))
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

// #6736: a stop keeps a worktree with unsaved work; the list shows it (dirty, with its branch), and the remove takes it away only by the no-force rule.
func TestKeptWorktreesAreListedAndRemovedByTheNoForceRule(t *testing.T) {
	url, cfg := teamServer(t)
	git, id, wt := launchWorktreeTeam(t, url, cfg, "keptlist")
	require.NoError(t, os.WriteFile(filepath.Join(wt, "unsaved.txt"), []byte("mine"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(wt, "committed.txt"), []byte("c"), 0o644))
	git("-C", wt, "add", "committed.txt")
	git("-C", wt, "commit", "-q", "-m", "c")

	// running: listed, in use, and the remove keeps it
	list := listWorktrees(t, url)
	require.Len(t, list, 1)
	assert.Equal(t, "team", list[0]["kind"])
	assert.Equal(t, wt, list[0]["path"])
	assert.Equal(t, "coral-team/"+id, list[0]["branch"])
	assert.Equal(t, true, list[0]["in_use"])
	teamID := list[0]["id"].(string)
	code, out := removeWorktree(t, url, teamID)
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "kept", out["state"])
	assert.Equal(t, "it is in use", out["reason"])
	assert.FileExists(t, filepath.Join(wt, "unsaved.txt"))

	stopTeam(t, url, id) // kept: it has unsaved work
	list = listWorktrees(t, url)
	require.Len(t, list, 1)
	assert.Equal(t, false, list[0]["in_use"])
	assert.Equal(t, true, list[0]["dirty"])
	assert.EqualValues(t, 1, list[0]["unmerged"])

	// dirty: kept, with the reason, and the file survives
	code, out = removeWorktree(t, url, teamID)
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "kept", out["state"])
	assert.Equal(t, "it has uncommitted changes", out["reason"])
	assert.FileExists(t, filepath.Join(wt, "unsaved.txt"))
	assert.Len(t, listWorktrees(t, url), 1)

	// the person saves their work: the worktree goes, the branch with its unmerged commit stays
	git("-C", wt, "add", "unsaved.txt")
	git("-C", wt, "commit", "-q", "-m", "unsaved")
	code, out = removeWorktree(t, url, teamID)
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "removed", out["state"])
	assert.Equal(t, "its branch is kept: it has commits that are not merged", out["reason"])
	assert.NoDirExists(t, wt)
	assert.Contains(t, git("branch", "--list", "coral-team/"+id), id)
	assert.Empty(t, listWorktrees(t, url), "gone from the list")
}

// The client sends an id from the list, never a path: anything else is not found, and nothing is removed.
func TestRemoveWorktreeTakesOnlyAnIdFromTheList(t *testing.T) {
	url, cfg := teamServer(t)
	_, id, wt := launchWorktreeTeam(t, url, cfg, "idonly")
	require.NoError(t, os.WriteFile(filepath.Join(wt, "unsaved.txt"), []byte("mine"), 0o644))
	stopTeam(t, url, id) // kept
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "precious.txt"), []byte("p"), 0o644))
	for _, bad := range []string{wt, "../" + id, "team-999", "run-1", outside, "team-1/../../x", ""} {
		code, _ := removeWorktree(t, url, bad)
		assert.Contains(t, []int{http.StatusNotFound, http.StatusBadRequest}, code, "%q", bad)
	}
	assert.FileExists(t, filepath.Join(wt, "unsaved.txt"))
	assert.FileExists(t, filepath.Join(outside, "precious.txt"))
}

// A job run's worktree that was kept is listed and removed the same way.
func TestKeptJobRunWorktreeIsListedAndRemoved(t *testing.T) {
	url, cfg, h := teamServerH(t)
	repo, git := repoOn(t, "master", true)
	wt := filepath.Join(t.TempDir(), "job-run-1-repo")
	git("worktree", "add", "-b", "coral/job-run-1", "--", wt, "master")
	sched := store.NewScheduleStore(h.db)
	job, err := sched.CreateScheduledJob(t.Context(), &store.ScheduledJob{Name: "nightly", CronExpr: "0 9 * * *", RepoPath: repo, Prompt: "p", AgentType: "claude"})
	require.NoError(t, err)
	runID, err := sched.CreateScheduledRun(t.Context(), job.ID, "2026-10-23T09:00:00Z", "completed")
	require.NoError(t, err)
	require.NoError(t, sched.UpdateScheduledRun(t.Context(), runID, map[string]interface{}{"worktree_path": wt}))
	_ = cfg

	list := listWorktrees(t, url)
	require.Len(t, list, 1)
	assert.Equal(t, "job", list[0]["kind"])
	assert.Equal(t, "nightly", list[0]["name"])
	assert.Equal(t, "coral/job-run-1", list[0]["branch"])
	assert.Equal(t, false, list[0]["dirty"])

	code, out := removeWorktree(t, url, list[0]["id"].(string))
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "removed", out["state"])
	assert.NoDirExists(t, wt)
	assert.Empty(t, git("branch", "--list", "coral/job-run-1"), "nothing unmerged: the run's branch goes")
}

// QA's #6722: the single kill of the last agent in a team's clean worktree takes the empty team branch with it, as a stop does; a worktree that is not a team's names no branch.
func TestSingleKillTakesTheTeamBranchOfACleanWorktree(t *testing.T) {
	_, h, _, _ := setupSessionsTestServerWithConfig(t, config.Load(t.TempDir()))
	root := filepath.Join(h.cfg.CoralDir(), "worktrees")
	assert.Equal(t, "coral-team/b-abc123", h.teamBranchOf(filepath.Join(root, "b-abc123")))
	assert.Equal(t, gitutil.TeamBranch("my team"), h.teamBranchOf(filepath.Join(root, "my team")))
	assert.Empty(t, h.teamBranchOf(filepath.Join(t.TempDir(), "agent-wt")), "not Coral's worktrees folder")
	assert.Empty(t, h.teamBranchOf(filepath.Join(root, "nested", "x")))
}
