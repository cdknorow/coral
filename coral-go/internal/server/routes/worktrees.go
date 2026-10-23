package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/gitutil"
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
}

// entries are the worktrees on disk that Coral made, newest first: a team's (its record says it has one) and a job run's (its run recorded one), each still in a repository's worktree list.
func (h *WorktreesHandler) entries(ctx context.Context) []worktreeEntry {
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
			out = append(out, h.describe(ctx, worktreeEntry{ID: "team-" + strconv.FormatInt(t.ID, 10), Kind: "team", Name: t.Name, Path: t.WorkingDir,
				RepoPath: cfg.WorkingDir, Branch: gitutil.TeamBranch(filepath.Base(t.WorkingDir)), InUse: t.Status != "stopped"}))
		}
	}
	if runs, err := h.sched.ListRunsWithWorktree(ctx); err == nil {
		for _, r := range runs {
			job, _ := h.sched.GetScheduledJob(ctx, r.JobID)
			if job == nil || r.WorktreePath == nil || job.RepoPath == "" {
				continue
			}
			name := job.Name
			out = append(out, h.describe(ctx, worktreeEntry{ID: "run-" + strconv.FormatInt(r.ID, 10), Kind: "job", Name: name, Path: *r.WorktreePath,
				RepoPath: job.RepoPath, Branch: "coral/job-run-" + strconv.FormatInt(r.ID, 10), InUse: r.Status == "pending" || r.Status == "running"}))
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
	return kept
}

// describe fills the entry's dirty and unmerged flags from git, each read with its own bound.
func (h *WorktreesHandler) describe(ctx context.Context, e worktreeEntry) worktreeEntry {
	if _, err := os.Stat(e.Path); err != nil {
		return e
	}
	gctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if out, err := gitutil.Command(gctx, e.Path, "status", "--porcelain", "--untracked-files=all").Output(); err == nil {
		e.Dirty = strings.TrimSpace(string(out)) != ""
	}
	repo := e.RepoPath
	if repo == "" || !gitutil.IsWorkTree(gctx, repo) {
		repo = gitutil.RepoOfWorktree(gctx, e.Path)
	}
	if repo != "" {
		e.RepoPath = repo
		e.Unmerged = gitutil.UnmergedCommits(gctx, repo, e.Branch)
	}
	return e
}

// List answers the worktrees.
// GET /api/worktrees
func (h *WorktreesHandler) List(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"worktrees": h.entries(r.Context())})
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
	for _, e := range h.entries(r.Context()) {
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
