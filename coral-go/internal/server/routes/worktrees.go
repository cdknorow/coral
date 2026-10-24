package routes

import (
	"context"
	"encoding/json"
	"github.com/subgentic/coral/internal/coralnames"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/gitutil"
	"github.com/subgentic/coral/internal/safego"
	"github.com/subgentic/coral/internal/store"
)

// WorktreesHandler lists the worktrees Coral made for teams and job runs that are still on disk (the ones a stop or a run kept because they hold unsaved work, #6701) and removes one by the same
// no-force rule a stop uses (#6736).
type WorktreesHandler struct {
	teams *store.TeamStore
	sched *store.ScheduleStore
	sess  *store.SessionStore
	cfg   *config.Config
}

func NewWorktreesHandler(db *store.DB, cfg *config.Config) *WorktreesHandler {
	return &WorktreesHandler{teams: store.NewTeamStore(db), sched: store.NewScheduleStore(db), sess: store.NewSessionStore(db), cfg: cfg}
}

// worktreeEntry is one Coral-made worktree. ID is opaque to the caller and names a team or a job run Coral knows: the remove asks for an id, never for a path.
type worktreeEntry struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"` // "team" or "job"
	Name     string `json:"name"` // the team's or the job's name
	Path     string `json:"path"`
	RepoPath string `json:"repo_path"`
	Branch   string `json:"branch"`
	Dirty    bool   `json:"dirty"`    // uncommitted or untracked changes
	Unmerged int    `json:"unmerged"` // commits on its branch that no other branch holds
	InUse    bool   `json:"in_use"`   // an agent of its team is live in it, or its run is still going
	teamID   int64  // a team entry's team, for the lock Remove and a resurrect share (#6977)
	Checked  bool   `json:"checked"` // false: git had not answered in time, so dirty and unmerged are not known (not "clean")
}

// entries are the worktrees on disk that Coral made, newest first: a team's (its record says it has one) and a job run's (its run recorded one), each still in a repository's worktree list.
func (h *WorktreesHandler) entries(ctx context.Context, probe bool) []worktreeEntry {
	// One budget for everything git is asked in building the list (a run folder's repository, then the probes): a git that never answers cannot hold it past worktreeListBudget (#6754, QA's #7031).
	ctx, cancelBudget := context.WithTimeout(ctx, worktreeListBudget)
	defer cancelBudget()
	out := []worktreeEntry{}
	root := coralnames.WorktreesRoot(h.cfg.CoralDir())
	live, liveErr := h.sess.GetAllLiveSessions(ctx) // a team's worktree is in use while a live session is in it, whichever way its agents were stopped (#6942)
	if teams, err := h.teams.ListTeams(ctx, ""); err == nil {
		for _, t := range teams {
			if t.IsWorktree != 1 || t.WorkingDir == "" || !coralnames.InWorktreesRoot(h.cfg.CoralDir(), t.WorkingDir) {
				continue // a team's folder outside Coral's worktrees folder is not Coral's to remove
			}
			var cfg struct {
				WorkingDir string `json:"working_dir"`
			}
			json.Unmarshal([]byte(t.ConfigJSON), &cfg)
			out = append(out, worktreeEntry{teamID: t.ID, ID: "team-" + strconv.FormatInt(t.ID, 10), Kind: "team", Name: t.Name, Path: t.WorkingDir,
				RepoPath: cfg.WorkingDir, Branch: coralnames.TeamBranch(filepath.Base(t.WorkingDir)), InUse: liveErr != nil || liveSessionIn(live, t) || teamLaunching(t, time.Now()) || teamResurrecting(t.ID)}) // a live list that cannot be read fails closed: in use
		}
	}
	if runs, err := h.sched.ListRunsWithWorktree(ctx); err == nil {
		for _, r := range runs {
			job, _ := h.sched.GetScheduledJob(ctx, r.JobID)
			if r.WorktreePath == nil {
				continue
			}
			path, repo, name := *r.WorktreePath, "", "a deleted job"
			if job != nil {
				name = job.Name
			}
			switch {
			case job != nil && job.RepoPath != "" && coralnames.IsJobRunWorktree(root, job.RepoPath, r.ID, path):
				repo = job.RepoPath
			case coralnames.IsJobRunFolder(root, r.ID, path):
				// The job's repository changed since (or the job is gone): the folder is still the run's own, and its git file says which repository it belongs to ("" when that repository is gone: it is listed, and a remove says
				// git cannot find it). Its name must be that repository's (#7026).
				rctx, cancelRead := context.WithTimeout(ctx, worktreeRepoReadTimeout)
				repo = worktreeRepoOf(rctx, path) // "" when git did not answer in time too: listed unchecked, like a gone repository, and a remove cannot go on without the repository
				cancelRead()
				if repo != "" && !coralnames.IsJobRunWorktree(root, repo, r.ID, path) {
					continue
				}
			default:
				continue // a run that made no worktree records the job's repository itself: not Coral's to list or remove
			}
			out = append(out, worktreeEntry{ID: "run-" + strconv.FormatInt(r.ID, 10), Kind: "job", Name: name, Path: path,
				RepoPath: repo, Branch: coralnames.JobRunBranch(r.ID), InUse: r.Status == "pending" || r.Status == "running"})
		}
	}
	kept := out[:0]
	for _, e := range out {
		if e.Path != "" {
			if _, err := os.Stat(e.Path); err == nil {
				kept = append(kept, e)
			}
		}
	}
	if probe {
		probeWorktrees(ctx, kept)
	}
	return kept
}

// teamLocks serialize a team's worktree Remove and its resurrect (#6977): Remove reads that the worktree is not in use and then runs git with nothing held, so a resurrect that began in that gap could lose the worktree it
// reuses. A resurrect holds the team's lock for its whole run; Remove takes it only if it is free (a resurrect under way means the worktree is in use) and keeps it from its check to the end of its git.
var teamLocks sync.Map // team id -> *sync.Mutex

func teamLock(id int64) *sync.Mutex {
	m, _ := teamLocks.LoadOrStore(id, &sync.Mutex{})
	return m.(*sync.Mutex)
}

// afterRemoveCheckHook runs in Remove between its in-use check and its git, with the team's lock held (a var for the tests).
var afterRemoveCheckHook func()

var (
	resurrectMu       sync.Mutex
	resurrectingTeams = map[int64]int{} // team id -> resurrects under way
	// afterResurrectWorktreeHook runs in a resurrect right after its worktree is recreated (a var for the tests).
	afterResurrectWorktreeHook func()
)

// beginTeamResurrect marks a team's resurrect as under way and returns what ends it. The team record stays "stopped" until its members are launched, and creation time says nothing about a relaunch long after, so the
// resurrect says so itself: the worktree it recreates is in use until it has returned (#6959).
func beginTeamResurrect(id int64) func() {
	resurrectMu.Lock()
	resurrectingTeams[id]++
	resurrectMu.Unlock()
	return func() {
		resurrectMu.Lock()
		defer resurrectMu.Unlock()
		if resurrectingTeams[id]--; resurrectingTeams[id] <= 0 {
			delete(resurrectingTeams, id)
		}
	}
}

func teamResurrecting(id int64) bool {
	resurrectMu.Lock()
	defer resurrectMu.Unlock()
	return resurrectingTeams[id] > 0
}

// teamLaunching reports whether a team that is not stopped is young enough for its launch to be under way: the launch holds the team for lifecycleTimeout plus lifecyclePerSession for each member, and in that time
// its agents are not registered yet, so no live session says the worktree is used. A clean fresh worktree could otherwise be removed (git finds nothing unsaved) under agents about to start in it (#6942). A team that
// is stopped never needs the grace; a creation time that cannot be read fails closed.
func teamLaunching(t store.Team, now time.Time) bool {
	if t.Status == "stopped" {
		return false
	}
	members := 20 // not named in the team's config: assume a large team
	var cfg struct {
		Agents []json.RawMessage `json:"agents"`
	}
	if json.Unmarshal([]byte(t.ConfigJSON), &cfg) == nil && len(cfg.Agents) > 0 {
		members = len(cfg.Agents)
	}
	for _, layout := range []string{store.ISOFormat, time.RFC3339Nano, "2006-01-02 15:04:05"} {
		if created, err := time.Parse(layout, t.CreatedAt); err == nil {
			return now.Sub(created) < lifecycleTimeout+time.Duration(members)*lifecyclePerSession
		}
	}
	return true
}

// liveSessionIn reports whether a live session belongs to the team or runs in its worktree. Killing a team's agents one by one (the dashboard's Kill Team) never marks the team stopped, so the team's own status says nothing.
func liveSessionIn(live []store.LiveSession, t store.Team) bool {
	dir := filepath.Clean(t.WorkingDir)
	within := func(p string) bool {
		p = filepath.Clean(p)
		return p == dir || strings.HasPrefix(p, dir+string(filepath.Separator))
	}
	for _, ls := range live {
		if ls.TeamID != nil && *ls.TeamID == t.ID {
			return true
		}
		if ls.WorkingDir != "" && within(ls.WorkingDir) || ls.WorktreePath != nil && *ls.WorktreePath != "" && within(*ls.WorktreePath) {
			return true
		}
	}
	return false
}

// worktreeProbeWorkers is how many entries are checked with git at once, and worktreeListBudget how long the whole list may take: an entry git has not answered for by then is returned as not
// checked (Checked false), never as clean (Security's #6736 LOW: one entry after another, 10 s each, a long list took minutes).
var (
	worktreeProbeWorkers = 4
	worktreeListBudget   = 8 * time.Second
	worktreeProbe        = probeWorktree
	// worktreeRepoReadTimeout bounds each read of a run folder's own repository (inside the list's budget).
	worktreeRepoReadTimeout = 5 * time.Second
	// worktreeRepoOf reads the repository a run folder's own git file names (a var for the tests: a read that timed out is a read that answered "").
	worktreeRepoOf = gitutil.RepoOfWorktree
)

// probeWorktrees fills each entry's dirty and unmerged flags and sets Checked, with a small worker pool inside the list's overall time limit.
func probeWorktrees(ctx context.Context, entries []worktreeEntry) {
	ctx, cancel := context.WithTimeout(ctx, worktreeListBudget)
	defer cancel()
	next := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < worktreeProbeWorkers; w++ {
		wg.Add(1)
		go func() {
			defer safego.Recover("server/routes/worktrees:probeWorktrees")
			defer wg.Done()
			for i := range next {
				entries[i] = worktreeProbe(ctx, entries[i])
			}
		}()
	}
feed:
	for i := range entries {
		select {
		case next <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(next)
	wg.Wait()
}

// probeWorktree fills one entry's flags from git, each read with its own bound; Checked only when both reads answered in time.
func probeWorktree(ctx context.Context, e worktreeEntry) worktreeEntry {
	gctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := gitutil.Command(gctx, e.Path, "status", "--porcelain", "--untracked-files=all").Output()
	if err != nil {
		return e
	}
	e.Dirty = strings.TrimSpace(string(out)) != ""
	repo := e.RepoPath
	if repo == "" || !gitutil.IsWorkTree(gctx, repo) {
		repo = gitutil.RepoOfWorktree(gctx, e.Path)
	}
	if repo != "" {
		e.RepoPath = repo
		e.Unmerged = gitutil.UnmergedCommits(gctx, repo, e.Branch)
	}
	e.Checked = gctx.Err() == nil
	return e
}

// List answers the worktrees.
// GET /api/worktrees
func (h *WorktreesHandler) List(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"worktrees": h.entries(r.Context(), true)})
}

// Remove takes one worktree away by the rule a stop uses: only when git finds nothing unsaved in it, and its branch only when no commit on it is unmerged. The answer is the outcome
// {state removed|kept|failed, path, branch, reason}; a worktree whose team or run is still going is kept ("it is in use").
// POST /api/worktrees/remove  {"id": "team-3"}
func (h *WorktreesHandler) Remove(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if err := decodeJSONMax(w, r, &body, 4096); err != nil || body.ID == "" {
		errBadRequest(w, "id is required")
		return
	}
	for _, e := range h.entries(r.Context(), false) {
		if e.ID != body.ID {
			continue
		}
		if e.teamID != 0 {
			mu := teamLock(e.teamID)
			if !mu.TryLock() { // a resurrect of this team is under way: the worktree is in use
				writeJSON(w, http.StatusOK, gitutil.WorktreeOutcome{State: "kept", Path: e.Path, Branch: e.Branch, Reason: "it is in use"})
				return
			}
			defer mu.Unlock()
			for _, fresh := range h.entries(r.Context(), false) { // read again under the lock: a resurrect may have finished since the list above
				if fresh.ID == e.ID {
					e = fresh
				}
			}
		}
		if e.InUse {
			writeJSON(w, http.StatusOK, gitutil.WorktreeOutcome{State: "kept", Path: e.Path, Branch: e.Branch, Reason: "it is in use"})
			return
		}
		if afterRemoveCheckHook != nil {
			afterRemoveCheckHook()
		}
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Second)
		defer cancel()
		// Containment, failing closed: Coral's own worktrees folder, or a registered worktree of the repository this id's job names; anything else is not removed.
		if !coralnames.InWorktreesRoot(h.cfg.CoralDir(), e.Path) && !gitutil.IsRegisteredWorktree(ctx, e.RepoPath, e.Path) {
			writeJSON(w, http.StatusOK, gitutil.WorktreeOutcome{State: "kept", Path: e.Path, Branch: e.Branch, Reason: "it is not a worktree Coral knows"})
			return
		}
		// A run's repository may be the one its own git file names (the job's repository changed since, #7026), which a planted folder could point anywhere: git is run in that repository only when it has this folder
		// registered as one of its worktrees, so a remove never prunes or touches a repository that does not know the folder (#7035).
		if e.Kind == "job" && e.RepoPath != "" && !gitutil.IsRegisteredWorktree(ctx, e.RepoPath, e.Path) { // no repository at all goes on to RemoveWorktree, which says plainly that git could not find it
			writeJSON(w, http.StatusOK, gitutil.WorktreeOutcome{State: "kept", Path: e.Path, Branch: e.Branch, Reason: "its repository does not list it as a worktree"})
			return
		}
		outcome, err := gitutil.RemoveWorktree(ctx, e.RepoPath, e.Path, e.Branch)
		if err != nil {
			outcome.State, outcome.Reason = "failed", "git could not find its repository"
		}
		writeJSON(w, http.StatusOK, outcome)
		return
	}
	errNotFound(w, "no such worktree")
}
