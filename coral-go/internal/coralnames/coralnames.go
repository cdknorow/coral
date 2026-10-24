// Package coralnames is the one place that names what Coral makes in a person's repositories and in its own data folder for worktrees: the branches (a team's coral-team/<board>, a job run's
// coral/job-run-<id>) and the folders (<data>/worktrees/<board>, <data>/worktrees/job-run-<id>-<repo>, the legacy <repo>_task_run_<id>). The worktree list, the removal's containment checks, the base-branch
// filter, the launch and the scheduler all read these functions, so a name is never built twice and two copies cannot drift apart (#6891). TestNoNameOfCoralsIsBuiltElsewhere fails when one is.
package coralnames

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

const (
	teamBranchPrefix   = "coral-team/"
	jobRunBranchPrefix = "coral/job-run-"
	worktreesDir       = "worktrees"
)

// TeamBranch is the branch a team's worktree is on: coral-team/ and the board's name, with every character git's branch names refuse (a space, "..", "~", "^", ":", "?", "*", "[", "\", "@{", a leading
// or trailing "." or "-", ".lock") replaced by "-" and a short hash of the name added when anything was replaced, so two names that differ only there get two branches. A name that is already a valid
// branch name is used as it is. The board and its worktree folder keep the name (#6701, Security's #6708 C1: a board named "my team" made every worktree launch fail).
func TeamBranch(board string) string {
	var b strings.Builder
	for _, r := range board {
		switch {
		case r == '.' || r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	slug := b.String()
	for strings.Contains(slug, "..") {
		slug = strings.ReplaceAll(slug, "..", ".-")
	}
	slug = strings.Trim(slug, ".-")
	slug = strings.TrimSuffix(slug, ".lock")
	slug = strings.Trim(slug, ".-")
	if slug != board || slug == "" {
		sum := sha256.Sum256([]byte(board))
		if slug == "" {
			slug = "team"
		}
		slug += "-" + hex.EncodeToString(sum[:3])
	}
	return teamBranchPrefix + slug
}

// JobRunBranch is the branch a job run's worktree is on.
func JobRunBranch(runID int64) string { return jobRunBranchPrefix + strconv.FormatInt(runID, 10) }

// IsCoralBranch reports whether name is a branch Coral made (a team's or a job run's): not one to offer as a base.
func IsCoralBranch(name string) bool {
	return strings.HasPrefix(name, teamBranchPrefix) || strings.HasPrefix(name, jobRunBranchPrefix)
}

// WorktreesRoot is the folder Coral's team worktrees and job-run worktrees go in: the "worktrees" folder of its data folder.
func WorktreesRoot(coralDir string) string { return filepath.Join(coralDir, worktreesDir) }

// TeamWorktree is a team's worktree folder, named by its board.
func TeamWorktree(coralDir, board string) string {
	return filepath.Join(WorktreesRoot(coralDir), board)
}

// InWorktreesRoot reports whether path is a folder directly inside the worktrees folder of coralDir (cleaned, not resolved through links).
func InWorktreesRoot(coralDir, path string) bool {
	return filepath.Clean(filepath.Dir(path)) == filepath.Clean(WorktreesRoot(coralDir))
}

// JobRunWorktree is where job run runID's worktree of repo goes: <root>/job-run-<id>-<repo name>, or beside the repository (the legacy <repo>_task_run_<id>) when root is empty.
func JobRunWorktree(root, repo string, runID int64) string {
	if root == "" {
		return fmt.Sprintf("%s_task_run_%d", repo, runID)
	}
	return filepath.Join(root, fmt.Sprintf("job-run-%d-%s", runID, filepath.Base(filepath.Clean(repo))))
}

// IsJobRunWorktree reports whether path is exactly the folder JobRunWorktree makes for run runID of a job on repo: under root, or the legacy one beside the repository, and never the repository itself. A run
// that made no worktree records the job's repository as its worktree path, which may be a person's own checkout: only a folder that passes this is ever listed or removed as a run's worktree (#6736, #6741).
func IsJobRunWorktree(root, repo string, runID int64, path string) bool {
	repo, path = filepath.Clean(repo), filepath.Clean(path)
	if repo == "" || repo == "." || path == repo {
		return false
	}
	if root != "" && filepath.Clean(filepath.Dir(path)) == filepath.Clean(root) && path == filepath.Clean(JobRunWorktree(root, repo, runID)) {
		return true
	}
	return filepath.Dir(path) == filepath.Dir(repo) && path == filepath.Clean(JobRunWorktree("", repo, runID))
}
