package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/subgentic/coral/internal/background"
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
	cfg   *config.Config
}

func NewWorktreesHandler(db *store.DB, cfg *config.Config) *WorktreesHandler {
	return &WorktreesHandler{teams: store.NewTeamStore(db), sched: store.NewScheduleStore(db), cfg: cfg}
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
	InUse    bool   `json:"in_use"`   // its team is running or its run is still going
	Checked  bool   `json:"checked"`  // false: git had not answered in time, so dirty and unmerged are not known (not "clean")
}

// entries are the worktrees on disk that Coral made, newest first: a team's (its record says it has one) and a job run's (its run recorded one), each still in a repository's worktree list.
func (h *WorktreesHandler) entries(ctx context.Context, probe bool) []worktreeEntry {
	out := []worktreeEntry{}
	root := filepath.Join(h.cfg.CoralDir(), "worktrees")
	if teams, err := h.teams.ListTeams(ctx, ""); err == nil {
		for _, t := range teams {
			if t.IsWorktree != 1 || t.WorkingDir == "" || filepath.Clean(filepath.Dir(t.WorkingDir)) != root {
				continue // a team's folder outside Coral's worktrees folder is not Coral's to remove
			}
			var cfg struct {
				WorkingDir string `json:"working_dir"`
			}
			json.Unmarshal([]byte(t.ConfigJSON), &cfg)
			out = append(out, worktreeEntry{ID: "team-" + strconv.FormatInt(t.ID, 10), Kind: "team", Name: t.Name, Path: t.WorkingDir,
				RepoPath: cfg.WorkingDir, Branch: gitutil.TeamBranch(filepath.Base(t.WorkingDir)), InUse: t.Status != "stopped"})
		}
	}
	if runs, err := h.sched.ListRunsWithWorktree(ctx); err == nil {
		for _, r := range runs {
			job, _ := h.sched.GetScheduledJob(ctx, r.JobID)
			if job == nil || r.WorktreePath == nil || job.RepoPath == "" || !background.IsRunWorktreeFolder(root, job.RepoPath, r.ID, *r.WorktreePath) {
				continue // a run that made no worktree records the job's repository itself: not Coral's to list or remove
			}
			name := job.Name
			out = append(out, worktreeEntry{ID: "run-" + strconv.FormatInt(r.ID, 10), Kind: "job", Name: name, Path: *r.WorktreePath,
				RepoPath: job.RepoPath, Branch: "coral/job-run-" + strconv.FormatInt(r.ID, 10), InUse: r.Status == "pending" || r.Status == "running"})
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

// worktreeProbeWorkers is how many entries are checked with git at once, and worktreeListBudget how long the whole list may take: an entry git has not answered for by then is returned as not
// checked (Checked false), never as clean (Security's #6736 LOW: one entry after another, 10 s each, a long list took minutes).
var (
	worktreeProbeWorkers = 4
	worktreeListBudget   = 8 * time.Second
	worktreeProbe        = probeWorktree
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
		if e.InUse {
			writeJSON(w, http.StatusOK, gitutil.WorktreeOutcome{State: "kept", Path: e.Path, Branch: e.Branch, Reason: "it is in use"})
			return
		}
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Second)
		defer cancel()
		// Containment, failing closed: Coral's own worktrees folder, or a registered worktree of the repository this id's job names; anything else is not removed.
		if filepath.Clean(filepath.Dir(e.Path)) != filepath.Join(h.cfg.CoralDir(), "worktrees") && !gitutil.IsRegisteredWorktree(ctx, e.RepoPath, e.Path) {
			writeJSON(w, http.StatusOK, gitutil.WorktreeOutcome{State: "kept", Path: e.Path, Branch: e.Branch, Reason: "it is not a worktree Coral knows"})
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
