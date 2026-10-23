package gitutil

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RepoOfWorktree is the repository a worktree belongs to (the folder holding its .git), "" when path is not a worktree of one.
func RepoOfWorktree(ctx context.Context, path string) string {
	common, err := git(ctx, path, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil || common == "" {
		return ""
	}
	return filepath.Dir(common)
}

// WorktreeOutcome is what taking a worktree away did, in the shape the stop answers with (#6701): State "removed" (the folder and its registration are gone), "kept" (unsaved work is in it) or "failed", and
// Reason, one short sentence, saying why it stayed or what else stayed (its branch, with commits merged nowhere). Path is filled by the caller. Coral never deletes a person's work without asking.
type WorktreeOutcome struct {
	State  string `json:"state"`
	Path   string `json:"path,omitempty"`
	Branch string `json:"branch,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// RemoveWorktree takes a worktree out of the repository that owns it, only when nothing in it is unsaved: a worktree with uncommitted or untracked changes stays, and so does its branch while it has
// commits that no other branch holds; a clean worktree goes (folder and registration, git worktree prune) and its branch with it when it holds nothing of its own. repoDir is where git runs: the team's or
// the job's own repository (a worktree's parent folder is not one); empty, or no longer a repository, it is found from the worktree itself. A worktree already gone is not an error.
func RemoveWorktree(ctx context.Context, repoDir, path, branch string) (WorktreeOutcome, error) {
	res := WorktreeOutcome{Path: path, Branch: branch}
	if repoDir == "" || !IsWorkTree(ctx, repoDir) {
		repoDir = RepoOfWorktree(ctx, path)
	}
	_, statErr := os.Stat(path)
	gone := os.IsNotExist(statErr)
	if repoDir == "" {
		if gone {
			res.State = "removed" // nothing there and no repository to unregister it from
			return res, nil
		}
		return res, fmt.Errorf("%s is not a git worktree of a repository Coral knows", path)
	}
	if !gone {
		out, err := Command(ctx, path, "status", "--porcelain", "--untracked-files=all").Output()
		if err != nil {
			res.State, res.Reason = "failed", "its state could not be read"
			return res, nil
		}
		if strings.TrimSpace(string(out)) != "" {
			res.State, res.Reason = "kept", "it has uncommitted changes"
			return res, nil
		}
		if out, err := Command(ctx, repoDir, "worktree", "remove", "--", path).CombinedOutput(); err != nil {
			Command(ctx, repoDir, "worktree", "prune").Run()
			res.State = "kept"
			if strings.Contains(string(out), "locked") {
				res.Reason = "the worktree is locked"
			} else {
				res.Reason = "git would not remove it"
			}
			return res, nil
		}
	}
	// A folder that is gone leaves its registration: prune clears it, so the next launch finds the name free.
	Command(ctx, repoDir, "worktree", "prune").Run()
	res.State = "removed"
	if branch != "" && refExists(ctx, repoDir, "refs/heads/"+branch) {
		if UnmergedCommits(ctx, repoDir, branch) > 0 {
			res.Reason = "its branch is kept: it has commits that are not merged"
		} else if err := Command(ctx, repoDir, "branch", "-D", "--", branch).Run(); err != nil {
			res.Reason = "its branch is kept: it could not be deleted"
		}
	}
	return res, nil
}

// UnmergedCommits counts the commits on branch that no other branch (local or remote) holds.
func UnmergedCommits(ctx context.Context, repoDir, branch string) int {
	refs, err := git(ctx, repoDir, "for-each-ref", "--format=%(refname)", "refs/heads", "refs/remotes")
	if err != nil {
		return 1 // unknown: treat as unmerged, never delete on a guess
	}
	args := []string{"rev-list", "--count", "refs/heads/" + branch}
	for _, ref := range strings.Fields(refs) {
		if ref != "refs/heads/"+branch {
			args = append(args, "^"+ref)
		}
	}
	n, err := git(ctx, repoDir, args...)
	if err != nil {
		return 1
	}
	var c int
	fmt.Sscanf(n, "%d", &c)
	return c
}

// oneLine is git's output as a single line, for a message the operator reads.
func oneLine(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(s, "fatal: ", "")), " ")
}

// AddTeamWorktree makes the worktree at path on branch, from base when the branch is new, and says whether it made the branch (#6701). What an earlier run left is met, not tripped over: a registration whose
// folder is gone is pruned, a branch that exists is checked out (its commits kept) instead of made again, a worktree already at path is reused, and an empty folder is used. A folder that is not empty and
// no worktree is the one thing it refuses, in one line. Every failure is one plain line, not git's two.
func AddTeamWorktree(ctx context.Context, repoDir, path, branch, base string) (branchCreated bool, err error) {
	Command(ctx, repoDir, "worktree", "prune").Run()
	if entries, readErr := os.ReadDir(path); readErr == nil && len(entries) > 0 {
		if registered(ctx, repoDir, path) {
			return false, nil // the worktree is still there (a stop that kept it): the team goes on in it
		}
		return false, fmt.Errorf("the folder %s is left over from an earlier run and is not empty: move or delete it, then launch again", path)
	}
	args := []string{"worktree", "add", "--", path, branch}
	if !refExists(ctx, repoDir, "refs/heads/"+branch) {
		args = []string{"worktree", "add", "-b", branch, "--", path, base}
		branchCreated = true
	}
	if out, addErr := Command(ctx, repoDir, args...).CombinedOutput(); addErr != nil {
		return false, fmt.Errorf("git worktree add: %s", oneLine(string(out)))
	}
	return branchCreated, nil
}

// registered reports whether path is one of the repository's worktrees.
func registered(ctx context.Context, repoDir, path string) bool {
	out, err := git(ctx, repoDir, "worktree", "list", "--porcelain")
	if err != nil {
		return false
	}
	want, _ := filepath.EvalSymlinks(path)
	for _, line := range strings.Split(out, "\n") {
		if p, ok := strings.CutPrefix(line, "worktree "); ok {
			if got, _ := filepath.EvalSymlinks(p); got == want || p == path {
				return true
			}
		}
	}
	return false
}
