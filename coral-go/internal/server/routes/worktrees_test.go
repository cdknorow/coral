package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/subgentic/coral/internal/coralnames"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	"github.com/subgentic/coral/internal/store/storetest"
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
	assert.Equal(t, coralnames.TeamBranch("my team"), h.teamBranchOf(filepath.Join(root, "my team")))
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

// Security's #6757 LOW (#6760): a git that answers the status read and then stalls on the unmerged-commits read leaves the entry not checked (the unmerged count is a cautious guess, not an answer), never
// checked: true.
func TestWorktreeEntryIsNotCheckedWhenTheUnmergedReadStalls(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake git uses a shell and named pipes")
	}
	realGit, err := exec.LookPath("git")
	require.NoError(t, err)
	repo, git := repoOn(t, "master", true)
	wt := filepath.Join(t.TempDir(), "worktree")
	git("worktree", "add", "-b", "stalls", wt, "master")
	require.NoError(t, os.WriteFile(filepath.Join(wt, "unsaved.txt"), []byte("mine"), 0o644))

	// Signal when the unmerged read actually begins. Keep its read blocked on
	// a pipe until context cancellation: no sleep and no 1.5s status-read race.
	fakeDir := t.TempDir()
	readyPath := filepath.Join(fakeDir, "ready")
	holdPath := filepath.Join(fakeDir, "hold")
	require.NoError(t, exec.Command("mkfifo", readyPath, holdPath).Run())
	ready, err := os.OpenFile(readyPath, os.O_RDWR, 0o600)
	require.NoError(t, err)
	defer ready.Close()
	hold, err := os.OpenFile(holdPath, os.O_RDWR, 0o600)
	require.NoError(t, err)
	defer hold.Close()
	quote := func(v string) string { return "'" + strings.ReplaceAll(v, "'", "'\"'\"'") + "'" }
	script := "#!/bin/sh\nfor a in \"$@\"; do\n  if [ \"$a\" = for-each-ref ]; then\n    printf 1 > " + quote(readyPath) + "\n    read ignored < " + quote(holdPath) + "\n    exit 1\n  fi\ndone\nexec " + quote(realGit) + " \"$@\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(fakeDir, "git"), []byte(script), 0o755))
	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reached := make(chan error, 1)
	go func() { var b [1]byte; _, err := io.ReadFull(ready, b[:]); reached <- err }()
	done := make(chan struct{})
	var entry worktreeEntry
	go func() {
		defer close(done)
		entry = probeWorktree(ctx, worktreeEntry{Path: wt, RepoPath: repo, Branch: "stalls"})
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("git probe did not stop after cancellation")
		}
	}()
	select {
	case err := <-reached:
		require.NoError(t, err)
	case <-done:
		t.Fatal("git probe ended before reaching the unmerged read")
	case <-time.After(30 * time.Second):
		t.Fatal("git probe did not reach the unmerged read")
	}
	cancel() // exercise the precise cancellation boundary, after status succeeded
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("git probe did not stop")
	}
	assert.True(t, entry.Dirty, "status answered before cancellation")
	assert.False(t, entry.Checked, "the interrupted unmerged read is not checked")
}

// UI Accessibility's #6938 HIGH (#6942): the dashboard's Kill Team kills agent by agent, and only the team route marks the team stopped, so a worktree kept after the single kills (unsaved work) was "in use" for good and had
// no Remove. A team's worktree is in use while a live session is in it, whichever way its agents were stopped.
func TestKeptTeamWorktreeIsNotInUseOnceItsAgentsWereKilledOneByOne(t *testing.T) {
	url, cfg, h := teamServerH(t)
	repo, _ := repoOn(t, "master", true)
	status, out := postTeam(t, url, map[string]any{"board_name": "singlekills", "working_dir": repo, "worktree": true, "agent_type": "claude", "agents": []map[string]any{{"name": "A", "prompt": "x"}, {"name": "B", "prompt": "x"}}})
	require.Equal(t, http.StatusOK, status, "%v", out)
	wt := filepath.Join(cfg.CoralDir(), "worktrees", out["board_id"].(string))
	require.NoError(t, os.WriteFile(filepath.Join(wt, "unsaved.txt"), []byte("mine"), 0o644)) // unsaved work keeps the worktree after the last kill

	inUse := func() bool {
		list := listWorktrees(t, url)
		require.Len(t, list, 1)
		return list[0]["in_use"].(bool)
	}
	ss := store.NewSessionStore(h.db)
	live, err := ss.GetAllLiveSessions(t.Context())
	require.NoError(t, err)
	require.Len(t, live, 2)
	assert.True(t, inUse(), "two agents are in it")
	for i, ls := range live {
		resp, err := http.Post(url+"/api/sessions/live/"+ls.AgentType+"-"+ls.SessionID+"/kill", "application/json", strings.NewReader(`{"agent_type":"`+ls.AgentType+`","session_id":"`+ls.SessionID+`"}`))
		require.NoError(t, err)
		resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		if i == 0 {
			assert.True(t, inUse(), "one agent is still in it")
		}
	}
	assert.True(t, inUse(), "a team just made with no live session could still be launching: in use for the launch's bound (#6942)")
	_, err = h.db.Exec(`UPDATE teams SET created_at = ?`, time.Now().Add(-lifecycleTimeout-time.Hour).UTC().Format(store.ISOFormat))
	require.NoError(t, err)
	assert.False(t, inUse(), "past the bound, every agent killed: nothing is in it, so Remove is offered")
	assert.FileExists(t, filepath.Join(wt, "unsaved.txt"), "kept: it has unsaved work")
}

// The grace of a launch in progress: a team that is not stopped is in use for lifecycleTimeout plus a few seconds a member from its creation, and never once it is stopped (#6942).
func TestTeamLaunchingIsBoundedByTheLaunchTime(t *testing.T) {
	now := time.Now()
	at := func(ago time.Duration) string { return now.Add(-ago).UTC().Format(store.ISOFormat) }
	team := func(status string, ago time.Duration, agents int) store.Team {
		cfg := `{"agents":[` + strings.TrimSuffix(strings.Repeat(`{},`, agents), ",") + `]}`
		return store.Team{Status: status, CreatedAt: at(ago), ConfigJSON: cfg}
	}
	assert.True(t, teamLaunching(team("running", time.Second, 2), now), "just made")
	assert.True(t, teamLaunching(team("running", lifecycleTimeout+5*time.Second, 3), now), "within the bound of 3 members")
	assert.False(t, teamLaunching(team("running", lifecycleTimeout+time.Hour, 3), now), "past the bound")
	assert.False(t, teamLaunching(team("stopped", time.Second, 2), now), "a stopped team needs no grace")
	assert.True(t, teamLaunching(store.Team{Status: "running", CreatedAt: "not a time"}, now), "an unreadable creation time fails closed")
}

// A live-session list that cannot be read fails closed (#6942, QA): the entry is in use, so Remove refuses it, rather than offering a folder an agent may be working in.
func TestWorktreeListFailsClosedWhenTheLiveSessionsCannotBeRead(t *testing.T) {
	cfg := config.Load(t.TempDir())
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "main.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	dir := filepath.Join(cfg.CoralDir(), "worktrees", "b-failclosed")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	_, err = store.NewTeamStore(db).CreateTeam(t.Context(), &store.Team{Name: "fc", ConfigJSON: "{}", Status: "stopped", WorkingDir: dir, IsWorktree: 1}) // stopped, so only the failed read can make it in use
	require.NoError(t, err)
	h := NewWorktreesHandler(db, cfg)

	entries := h.entries(t.Context(), false)
	require.Len(t, entries, 1)
	assert.False(t, entries[0].InUse, "a readable, empty live list: nothing in it")

	broken, err := storetest.Open(t, filepath.Join(t.TempDir(), "broken.db"))
	require.NoError(t, err)
	require.NoError(t, broken.Close())
	h.sess = store.NewSessionStore(broken) // reads fail
	entries = h.entries(t.Context(), false)
	require.Len(t, entries, 1)
	assert.True(t, entries[0].InUse, "a live list that cannot be read counts as in use")
}

// Security's #6949 LOW (#6959): a resurrect relaunches a stopped team long after its creation and recreates its worktree before any member is registered; the team record stays "stopped" meanwhile. The worktree it has just
// recreated is clean, so git's no-force rule would let Remove take it. While the resurrect is under way the entry is in use and Remove refuses it.
func TestWorktreeRecreatedByAResurrectIsInUseUntilItReturns(t *testing.T) {
	url, cfg := teamServer(t)
	git, id, wt := launchWorktreeTeam(t, url, cfg, "resurrectinuse")
	require.NoError(t, os.WriteFile(filepath.Join(wt, "work.txt"), []byte("work"), 0o644))
	git("-C", wt, "add", "work.txt")
	git("-C", wt, "commit", "-q", "-m", "work")
	stopTeam(t, url, id) // clean: its worktree is removed, the branch with its commit stays
	require.NoDirExists(t, wt)

	var during struct {
		listed, inUse bool
		removeState   string
	}
	afterResurrectWorktreeHook = func() {
		list := listWorktrees(t, url)
		if len(list) == 1 {
			during.listed, during.inUse = true, list[0]["in_use"].(bool)
			_, out := removeWorktree(t, url, list[0]["id"].(string))
			during.removeState, _ = out["state"].(string)
		}
	}
	t.Cleanup(func() { afterResurrectWorktreeHook = nil })
	status, body := apiPost(t, url+"/api/teams/detail/"+id+"/resurrect")
	require.Equal(t, http.StatusOK, status, body)

	assert.True(t, during.listed, "the recreated worktree is listed")
	assert.True(t, during.inUse, "in use while the resurrect is under way, though its team record says stopped")
	assert.Equal(t, "kept", during.removeState, "Remove refuses it")
	assert.DirExists(t, wt)
	assert.False(t, teamResurrecting(1), "the mark ends when the resurrect returns")
}

func TestBeginTeamResurrectCountsOverlappingResurrects(t *testing.T) {
	end1 := beginTeamResurrect(42)
	end2 := beginTeamResurrect(42)
	assert.True(t, teamResurrecting(42))
	end1()
	assert.True(t, teamResurrecting(42), "one is still under way")
	end2()
	assert.False(t, teamResurrecting(42))
}

// QA's note on #6911 (#7026): a kept job-run worktree of a job whose repository changed since, or is gone, is still Coral's to list and to remove: the folder is the one the scheduler made for the run, and its own git file says
// which repository it belongs to, not the job's current repo_path.
func TestKeptJobRunWorktreeIsStillListedAfterTheJobsRepositoryChanged(t *testing.T) {
	url, cfg, h := teamServerH(t)
	repo, git := repoOn(t, "master", true)
	other, _ := repoOn(t, "master", true)
	sched := store.NewScheduleStore(h.db)
	job, err := sched.CreateScheduledJob(t.Context(), &store.ScheduledJob{Name: "nightly", CronExpr: "0 9 * * *", RepoPath: repo, Prompt: "p", AgentType: "claude"})
	require.NoError(t, err)
	runID, err := sched.CreateScheduledRun(t.Context(), job.ID, "2026-10-23T09:00:00Z", "completed")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(cfg.CoralDir(), "worktrees"), 0o755))
	wt := filepath.Join(cfg.CoralDir(), "worktrees", fmt.Sprintf("job-run-%d-%s", runID, filepath.Base(repo)))
	git("worktree", "add", "-b", fmt.Sprintf("coral/job-run-%d", runID), "--", wt, "master")
	require.NoError(t, sched.UpdateScheduledRun(t.Context(), runID, map[string]interface{}{"worktree_path": wt}))
	require.Len(t, listWorktrees(t, url), 1, "listed while the job still names the repository")

	_, err = sched.UpdateScheduledJob(t.Context(), job.ID, map[string]interface{}{"repo_path": other}) // the job now names another repository
	require.NoError(t, err)
	list := listWorktrees(t, url)
	require.Len(t, list, 1, "still listed: the folder is the run's, whatever the job names now")
	assert.Equal(t, "job", list[0]["kind"])
	assert.Equal(t, "nightly", list[0]["name"])
	realRepo, err := filepath.EvalSymlinks(repo) // git answers with the resolved path (/private/tmp on a Mac)
	require.NoError(t, err)
	assert.Equal(t, realRepo, list[0]["repo_path"], "its own repository, from the worktree's git file")

	code, out := removeWorktree(t, url, list[0]["id"].(string))
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "removed", out["state"])
	assert.NoDirExists(t, wt)
	assert.Empty(t, git("branch", "--list", fmt.Sprintf("coral/job-run-%d", runID)), "its branch went with it: from the right repository")
}

// A run folder is only taken for the run's own: another run's, a name that is not the scheduler's, and a folder outside the worktrees folder are not listed when the job's repository differs.
func TestAJobRunFolderThatIsNotTheRunsOwnIsNotListedAfterTheRepositoryChanged(t *testing.T) {
	url, cfg, h := teamServerH(t)
	repo, _ := repoOn(t, "master", true)
	other, _ := repoOn(t, "master", true)
	sched := store.NewScheduleStore(h.db)
	job, err := sched.CreateScheduledJob(t.Context(), &store.ScheduledJob{Name: "nightly", CronExpr: "0 9 * * *", RepoPath: other, Prompt: "p", AgentType: "claude"})
	require.NoError(t, err)
	runID, err := sched.CreateScheduledRun(t.Context(), job.ID, "2026-10-23T09:00:00Z", "completed")
	require.NoError(t, err)
	root := filepath.Join(cfg.CoralDir(), "worktrees")
	require.NoError(t, os.MkdirAll(root, 0o755))
	for _, wt := range []string{filepath.Join(root, "something-else"), filepath.Join(root, fmt.Sprintf("job-run-%d-x", runID+1)), filepath.Join(t.TempDir(), fmt.Sprintf("job-run-%d-x", runID))} {
		require.NoError(t, os.MkdirAll(wt, 0o755))
		require.NoError(t, sched.UpdateScheduledRun(t.Context(), runID, map[string]interface{}{"worktree_path": wt}))
		assert.Empty(t, listWorktrees(t, url), wt)
	}
	_ = repo
}

// The job's repository is gone altogether: the kept run folder is still listed (its work may be in it), not checked (git cannot read it), and a remove says plainly that git could not find the repository and leaves the folder (#7026).
func TestKeptJobRunWorktreeIsListedAndLeftWhenItsRepositoryIsGone(t *testing.T) {
	url, cfg, h := teamServerH(t)
	repo, git := repoOn(t, "master", true)
	sched := store.NewScheduleStore(h.db)
	job, err := sched.CreateScheduledJob(t.Context(), &store.ScheduledJob{Name: "nightly", CronExpr: "0 9 * * *", RepoPath: repo, Prompt: "p", AgentType: "claude"})
	require.NoError(t, err)
	runID, err := sched.CreateScheduledRun(t.Context(), job.ID, "2026-10-23T09:00:00Z", "completed")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(cfg.CoralDir(), "worktrees"), 0o755))
	wt := filepath.Join(cfg.CoralDir(), "worktrees", fmt.Sprintf("job-run-%d-%s", runID, filepath.Base(repo)))
	git("worktree", "add", "-b", fmt.Sprintf("coral/job-run-%d", runID), "--", wt, "master")
	require.NoError(t, os.WriteFile(filepath.Join(wt, "unsaved.txt"), []byte("mine"), 0o644))
	require.NoError(t, sched.UpdateScheduledRun(t.Context(), runID, map[string]interface{}{"worktree_path": wt}))
	other, _ := repoOn(t, "master", true)
	_, err = sched.UpdateScheduledJob(t.Context(), job.ID, map[string]interface{}{"repo_path": other})
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(repo))

	list := listWorktrees(t, url)
	require.Len(t, list, 1, "listed although its repository is gone")
	assert.Equal(t, false, list[0]["checked"], "not checked: git cannot read it")
	code, out := removeWorktree(t, url, list[0]["id"].(string))
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "failed", out["state"], "nothing is removed that git could not check")
	assert.Equal(t, "git could not find its repository", out["reason"], "the plain words for a repository that is gone")
	assert.FileExists(t, filepath.Join(wt, "unsaved.txt"))
}

// Security Windows's LOW on #7026 (#7035), the name check: a folder job-run-<id>-wrong whose git file names a repository of another name is not the run's, and is not listed.
func TestAJobRunFolderWhoseGitFileNamesARepositoryOfAnotherNameIsNotListed(t *testing.T) {
	url, cfg, h := teamServerH(t)
	repo, git := repoOn(t, "master", true)
	other, _ := repoOn(t, "master", true)
	sched := store.NewScheduleStore(h.db)
	job, err := sched.CreateScheduledJob(t.Context(), &store.ScheduledJob{Name: "nightly", CronExpr: "0 9 * * *", RepoPath: other, Prompt: "p", AgentType: "claude"})
	require.NoError(t, err)
	runID, err := sched.CreateScheduledRun(t.Context(), job.ID, "2026-10-23T09:00:00Z", "completed")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(cfg.CoralDir(), "worktrees"), 0o755))
	wt := filepath.Join(cfg.CoralDir(), "worktrees", fmt.Sprintf("job-run-%d-wrong", runID)) // not repo's base name
	git("worktree", "add", "-b", "coral/some-branch", "--", wt, "master")
	require.NoError(t, sched.UpdateScheduledRun(t.Context(), runID, map[string]interface{}{"worktree_path": wt}))
	assert.NotEqual(t, "wrong", filepath.Base(repo))
	assert.Empty(t, listWorktrees(t, url), "the folder's name is not the name of the repository its git file names")
}

// Security Windows's LOW on #7026 (#7035), the prune: a remove must not run git in a repository whose registration of the folder it cannot find. A planted run folder whose git file names another repository (named as that repository is)
// is listed, and a remove leaves that repository alone: its stale registration is still there, which git worktree prune would have taken.
func TestRemoveOfAJobRunFolderNotRegisteredInItsRepositoryTouchesNothingThere(t *testing.T) {
	url, cfg, h := teamServerH(t)
	victim, vgit := repoOn(t, "master", true)
	stale := filepath.Join(t.TempDir(), "stale")
	vgit("worktree", "add", "-b", "stalebranch", "--", stale, "master")
	require.NoError(t, os.RemoveAll(stale)) // a registration with no folder: what prune takes
	registrations := func() string { return vgit("worktree", "list", "--porcelain") }
	require.Contains(t, registrations(), "stale")
	adminDirs, err := filepath.Glob(filepath.Join(victim, ".git", "worktrees", "*"))
	require.NoError(t, err)
	require.Len(t, adminDirs, 1)

	sched := store.NewScheduleStore(h.db)
	job, err := sched.CreateScheduledJob(t.Context(), &store.ScheduledJob{Name: "nightly", CronExpr: "0 9 * * *", RepoPath: t.TempDir(), Prompt: "p", AgentType: "claude"})
	require.NoError(t, err)
	runID, err := sched.CreateScheduledRun(t.Context(), job.ID, "2026-10-23T09:00:00Z", "completed")
	require.NoError(t, err)
	root := filepath.Join(cfg.CoralDir(), "worktrees")
	require.NoError(t, os.MkdirAll(root, 0o755))
	planted := filepath.Join(root, fmt.Sprintf("job-run-%d-%s", runID, filepath.Base(victim)))
	require.NoError(t, os.MkdirAll(planted, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(planted, ".git"), []byte("gitdir: "+adminDirs[0]+"\n"), 0o644)) // its git file names the victim's stale registration
	require.NoError(t, sched.UpdateScheduledRun(t.Context(), runID, map[string]interface{}{"worktree_path": planted}))

	list := listWorktrees(t, url)
	if len(list) == 1 { // listed or not, a remove must leave the victim alone
		code, out := removeWorktree(t, url, list[0]["id"].(string))
		assert.Equal(t, http.StatusOK, code)
		assert.NotEqual(t, "removed", out["state"])
	}
	assert.Contains(t, registrations(), "stale", "the other repository's registration is still there: nothing was pruned in it")
	assert.DirExists(t, planted)
}

// #7056: after the list's read of a run folder's repository timed out (the entry has no repository), a remove must not let git work out the repository from the folder's own git file and run there
// unchecked. A planted run folder that names a repository which does not list it is kept as it is, and that repository is not pruned.
func TestRemoveOfARunFolderWhoseRepositoryReadTimedOutNeverTouchesAnotherRepository(t *testing.T) {
	url, cfg, h := teamServerH(t)
	other, otherGit := repoOn(t, "master", true) // the repository the planted folder names; it has a stale registration a prune would remove
	staleDir := filepath.Join(t.TempDir(), "stale")
	otherGit("worktree", "add", "-b", "stale-branch", "--", staleDir, "master")
	require.NoError(t, os.RemoveAll(staleDir))

	jobRepo, _ := repoOn(t, "master", true)
	sched := store.NewScheduleStore(h.db)
	job, err := sched.CreateScheduledJob(t.Context(), &store.ScheduledJob{Name: "nightly", CronExpr: "0 9 * * *", RepoPath: jobRepo, Prompt: "p", AgentType: "claude"})
	require.NoError(t, err)
	runID, err := sched.CreateScheduledRun(t.Context(), job.ID, "2026-10-23T09:00:00Z", "completed")
	require.NoError(t, err)
	root := filepath.Join(cfg.CoralDir(), "worktrees")
	require.NoError(t, os.MkdirAll(root, 0o755))
	planted := filepath.Join(root, "job-run-"+strconv.FormatInt(runID, 10)+"-"+filepath.Base(other)) // a run folder of the run, named for the repository its git file names
	require.NoError(t, os.MkdirAll(planted, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(planted, ".git"), []byte("gitdir: "+filepath.Join(other, ".git", "worktrees", "stale")+"\n"), 0o644))
	require.NoError(t, sched.UpdateScheduledRun(t.Context(), runID, map[string]interface{}{"worktree_path": planted}))

	// The first read of a run folder's repository does not answer (as when it timed out); the later ones, the remove's own among them, do.
	var reads atomic.Int32
	defer func(f func(context.Context, string) string) { worktreeRepoOf = f }(worktreeRepoOf)
	worktreeRepoOf = func(ctx context.Context, path string) string {
		if reads.Add(1) == 1 {
			return ""
		}
		return gitutil.RepoOfWorktree(ctx, path)
	}

	// The remove lists the folders itself, and that read is the one that stalls: the entry has no repository, and the remove goes on to RemoveWorktree, whose own read of the folder works.
	code, out := removeWorktree(t, url, "run-"+strconv.FormatInt(runID, 10))
	require.EqualValues(t, 1, reads.Load(), "the remove's own listing read the run folder's repository once, and got no answer")
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "kept", out["state"], fmt.Sprint(out))
	assert.DirExists(t, planted, "the planted folder is as it was")
	assert.Contains(t, otherGit("worktree", "list", "--porcelain"), "stale", "the repository it names was not pruned")
	assert.Contains(t, otherGit("branch", "--list", "stale-branch"), "stale-branch")
}

// Security Reviewer Windows's #6972 LOW (#6977): Remove read "not in use" and then ran git with nothing held, so a resurrect of the team that began in that gap reused the worktree and Remove then took it. A resurrect now waits
// for a Remove under way, and a Remove that finds a resurrect under way keeps the worktree.
func TestRemoveAndAResurrectOfTheSameTeamDoNotInterleave(t *testing.T) {
	url, cfg := teamServer(t)
	git, id, wt := launchWorktreeTeam(t, url, cfg, "removeraces")
	require.NoError(t, os.WriteFile(filepath.Join(wt, "work.txt"), []byte("work"), 0o644))
	git("-C", wt, "add", "work.txt")
	git("-C", wt, "commit", "-q", "-m", "work")
	stopTeam(t, url, id) // clean: its worktree is removed, the branch with its commit stays
	require.NoDirExists(t, wt)
	git("worktree", "add", "--", wt, "coral-team/"+id) // a clean worktree of the stopped team again: the one a person would Remove
	list := listWorktrees(t, url)
	require.Len(t, list, 1)
	require.Equal(t, false, list[0]["in_use"])

	resurrected := make(chan int, 1)
	afterRemoveCheckHook = func() { // Remove has found it clean and not in use; its git has not run yet
		go func() {
			status, _ := apiPost(t, url+"/api/teams/detail/"+id+"/resurrect")
			resurrected <- status
		}()
		time.Sleep(500 * time.Millisecond) // sleep-ok: the resurrect has every chance to start while Remove holds the check
	}
	t.Cleanup(func() { afterRemoveCheckHook = nil })
	code, out := removeWorktree(t, url, list[0]["id"].(string))
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "removed", out["state"])
	select {
	case status := <-resurrected:
		assert.Equal(t, http.StatusOK, status, "the resurrect ran after the removal")
	case <-time.After(30 * time.Second):
		t.Fatal("the resurrect never answered")
	}
	assert.DirExists(t, wt, "the resurrect's worktree is there: the removal did not run under it")
	assert.Contains(t, git("worktree", "list"), id)
}

// A Remove that finds the team's resurrect under way keeps the worktree, without waiting for it.
func TestRemoveKeepsAWorktreeWhileTheTeamIsBeingResurrected(t *testing.T) {
	url, cfg := teamServer(t)
	git, id, wt := launchWorktreeTeam(t, url, cfg, "removewhileresurrect")
	require.NoError(t, os.WriteFile(filepath.Join(wt, "work.txt"), []byte("work"), 0o644))
	git("-C", wt, "add", "work.txt")
	git("-C", wt, "commit", "-q", "-m", "work")
	stopTeam(t, url, id)
	git("worktree", "add", "--", wt, "coral-team/"+id)
	list := listWorktrees(t, url)
	require.Len(t, list, 1)

	var teamNum int64
	_, err := fmt.Sscanf(list[0]["id"].(string), "team-%d", &teamNum)
	require.NoError(t, err)
	mu := teamLock(teamNum) // as a resurrect holds it
	mu.Lock()
	defer mu.Unlock()
	code, out := removeWorktree(t, url, list[0]["id"].(string))
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "kept", out["state"])
	assert.Equal(t, "it is in use", out["reason"])
	assert.DirExists(t, wt)
}

// Remove reads the entry again under the team's lock (#6977, Security Reviewer Windows's note 1): a resurrect that ran between its first read and the lock has put agents in the worktree, and the first read's "not in use" is stale.
func TestRemoveReadsTheEntryAgainUnderTheTeamsLock(t *testing.T) {
	url, cfg := teamServer(t)
	git, id, wt := launchWorktreeTeam(t, url, cfg, "removereread")
	require.NoError(t, os.WriteFile(filepath.Join(wt, "work.txt"), []byte("work"), 0o644))
	git("-C", wt, "add", "work.txt")
	git("-C", wt, "commit", "-q", "-m", "work")
	stopTeam(t, url, id)
	git("worktree", "add", "--", wt, "coral-team/"+id)
	list := listWorktrees(t, url)
	require.Len(t, list, 1)
	require.Equal(t, false, list[0]["in_use"])

	beforeRemoveLockHook = func() { // the first read said not in use; now the team is resurrected, whole, before Remove has the lock
		beforeRemoveLockHook = nil
		status, body := apiPost(t, url+"/api/teams/detail/"+id+"/resurrect")
		require.Equal(t, http.StatusOK, status, body)
	}
	t.Cleanup(func() { beforeRemoveLockHook = nil })
	code, out := removeWorktree(t, url, list[0]["id"].(string))
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "kept", out["state"], "agents are in it now: the stale read is not acted on")
	assert.Equal(t, "it is in use", out["reason"])
	assert.DirExists(t, wt)
}

// Two resurrects of one team started together: the second checks the team's status again once it has the lock, finds the team running, and refuses, so the members are launched once (#6977, note 2).
func TestTwoResurrectsOfOneTeamLaunchItsMembersOnce(t *testing.T) {
	url, cfg, h := teamServerH(t)
	git, id, wt := launchWorktreeTeam(t, url, cfg, "resurrecttwice")
	require.NoError(t, os.WriteFile(filepath.Join(wt, "work.txt"), []byte("work"), 0o644))
	git("-C", wt, "add", "work.txt")
	git("-C", wt, "commit", "-q", "-m", "work")
	stopTeam(t, url, id)
	ss := store.NewSessionStore(h.db)
	before, err := ss.GetAllLiveSessions(t.Context())
	require.NoError(t, err)

	reached := make(chan struct{}, 1)
	release := make(chan struct{})
	afterResurrectWorktreeHook = func() { // the first resurrect holds the lock here until the second has started and is waiting for it
		select {
		case reached <- struct{}{}:
			<-release
		default:
		}
	}
	t.Cleanup(func() { afterResurrectWorktreeHook = nil })
	results := make(chan int, 2)
	post := func() {
		status, _ := apiPost(t, url+"/api/teams/detail/"+id+"/resurrect")
		results <- status
	}
	go post()
	<-reached
	go post()                          // passes the status check (still stopped) and waits for the lock
	time.Sleep(500 * time.Millisecond) // sleep-ok: the second resurrect has reached the lock
	close(release)
	got := []int{<-results, <-results}
	assert.ElementsMatch(t, []int{http.StatusOK, http.StatusBadRequest}, got, "one resurrect, one refusal")
	after, err := ss.GetAllLiveSessions(t.Context())
	require.NoError(t, err)
	assert.Equal(t, len(before)+1, len(after), "the team's one member was launched once")
}
