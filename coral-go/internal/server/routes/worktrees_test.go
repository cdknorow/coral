package routes

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
	sched := store.NewScheduleStore(h.db)
	job, err := sched.CreateScheduledJob(t.Context(), &store.ScheduledJob{Name: "nightly", CronExpr: "0 9 * * *", RepoPath: repo, Prompt: "p", AgentType: "claude"})
	require.NoError(t, err)
	runID, err := sched.CreateScheduledRun(t.Context(), job.ID, "2026-10-23T09:00:00Z", "completed")
	require.NoError(t, err)
	require.EqualValues(t, 1, runID)
	require.NoError(t, os.MkdirAll(filepath.Join(cfg.CoralDir(), "worktrees"), 0o755))
	wt := filepath.Join(cfg.CoralDir(), "worktrees", "job-run-1-"+filepath.Base(repo)) // the folder the scheduler makes for this run
	git("worktree", "add", "-b", "coral/job-run-1", "--", wt, "master")
	require.NoError(t, sched.UpdateScheduledRun(t.Context(), runID, map[string]interface{}{"worktree_path": wt}))

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

// Security's #6741 (data loss): a run that made no worktree records the job's repository itself as its worktree path. When that repository is a person's own linked checkout, it is not Coral's
// to list, and a remove must never reach it. Also a folder that is not the one the scheduler makes for that run.
func TestAJobsOwnCheckoutIsNeverListedOrRemoved(t *testing.T) {
	url, cfg, h := teamServerH(t)
	main, git := repoOn(t, "master", true)
	own := filepath.Join(t.TempDir(), "feature") // the person's own linked worktree: a normal git worktree add ../feature
	git("worktree", "add", "-b", "feature", "--", own, "master")
	sched := store.NewScheduleStore(h.db)
	mk := func(name, repo, worktree string) int64 {
		job, err := sched.CreateScheduledJob(t.Context(), &store.ScheduledJob{Name: name, CronExpr: "0 9 * * *", RepoPath: repo, Prompt: "p", AgentType: "claude"})
		require.NoError(t, err)
		runID, err := sched.CreateScheduledRun(t.Context(), job.ID, "2026-10-23T09:00:00Z", "completed")
		require.NoError(t, err)
		require.NoError(t, sched.UpdateScheduledRun(t.Context(), runID, map[string]interface{}{"worktree_path": worktree}))
		return runID
	}
	mk("on-own-checkout", own, own) // a plain run: the repository itself is the "worktree"
	mk("on-plain-repo", main, main) // the same on an ordinary repository
	stray := filepath.Join(cfg.CoralDir(), "worktrees", "somewhere-else")
	require.NoError(t, os.MkdirAll(stray, 0o755))
	mk("wrong-folder", main, stray) // a folder in the worktrees folder that is not this run's

	assert.Empty(t, listWorktrees(t, url), "none of these is a worktree Coral made")
	for _, id := range []string{"run-1", "run-2", "run-3"} {
		code, _ := removeWorktree(t, url, id)
		assert.Equal(t, http.StatusNotFound, code, id)
	}
	assert.DirExists(t, own)
	assert.DirExists(t, stray)
	assert.Contains(t, git("worktree", "list"), "feature", "the person's checkout is still registered")
}

// Security's #6736 LOW (#6754): the list checks its entries with git a few at a time, inside one time limit; an entry git has not answered for is "not checked", never reported clean.
func TestWorktreeListProbesInParallelAndWithinABudget(t *testing.T) {
	url, cfg, h := teamServerH(t)
	teams := store.NewTeamStore(h.db)
	root := filepath.Join(cfg.CoralDir(), "worktrees")
	for i := 0; i < 12; i++ {
		dir := filepath.Join(root, "b-fake"+strconv.Itoa(i))
		require.NoError(t, os.MkdirAll(dir, 0o755))
		_, err := teams.CreateTeam(t.Context(), &store.Team{Name: "t" + strconv.Itoa(i), Status: "stopped", WorkingDir: dir, IsWorktree: 1, ConfigJSON: "{}"})
		require.NoError(t, err)
	}
	defer func(w int, b time.Duration, p func(context.Context, worktreeEntry) worktreeEntry) {
		worktreeProbeWorkers, worktreeListBudget, worktreeProbe = w, b, p
	}(worktreeProbeWorkers, worktreeListBudget, worktreeProbe)

	// A slow fake git: each check takes 250 ms. Twelve entries one after another would be 3 s; four at a time, under 1.2 s, and never more than four at once.
	var running, peak atomic.Int32
	worktreeProbe = func(ctx context.Context, e worktreeEntry) worktreeEntry {
		n := running.Add(1)
		defer running.Add(-1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		time.Sleep(250 * time.Millisecond)
		e.Checked = true
		return e
	}
	start := time.Now()
	list := listWorktrees(t, url)
	took := time.Since(start)
	require.Len(t, list, 12)
	assert.Less(t, took, 2*time.Second, "not one after another")
	assert.EqualValues(t, 4, peak.Load(), "four at a time, no more")
	for _, e := range list {
		assert.Equal(t, true, e["checked"])
	}

	// A git that never answers: the list returns at its budget, and the entries it did not reach say "not checked".
	worktreeListBudget = 400 * time.Millisecond
	worktreeProbe = func(ctx context.Context, e worktreeEntry) worktreeEntry {
		<-ctx.Done()
		return e
	}
	start = time.Now()
	list = listWorktrees(t, url)
	assert.Less(t, time.Since(start), 2*time.Second, "bounded as a whole")
	require.Len(t, list, 12)
	for _, e := range list {
		assert.Equal(t, false, e["checked"], "not answered in time is not checked, never clean")
	}
}
