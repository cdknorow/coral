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

// WorktreeOutcome is what taking a worktree away did: Removed when the folder and its registration are gone, and Kept saying what stayed and why (the worktree with unsaved changes, a branch with commits
// that are merged nowhere). Coral never deletes a person's work without asking (#6701).
type WorktreeOutcome struct {
	Removed bool   `json:"removed"`
	Kept    string `json:"kept,omitempty"`
}

// RemoveWorktree takes a worktree out of the repository that owns it, only when nothing in it is unsaved: a worktree with uncommitted or untracked changes stays, and so does its branch while it has
// commits that no other branch holds; a clean worktree goes (folder and registration, git worktree prune) and its branch with it when it holds nothing of its own. repoDir is where git runs: the team's or
// the job's own repository (a worktree's parent folder is not one); empty, or no longer a repository, it is found from the worktree itself. A worktree already gone is not an error.
func RemoveWorktree(ctx context.Context, repoDir, path, branch string) (WorktreeOutcome, error) {
	if repoDir == "" || !IsWorkTree(ctx, repoDir) {
		repoDir = RepoOfWorktree(ctx, path)
	}
	_, statErr := os.Stat(path)
	gone := os.IsNotExist(statErr)
	if repoDir == "" {
		if gone {
			return WorktreeOutcome{Removed: true}, nil // nothing there and no repository to unregister it from
		}
		return WorktreeOutcome{}, fmt.Errorf("%s is not a git worktree of a repository Coral knows", path)
	}
	if !gone {
		if out, err := Command(ctx, path, "status", "--porcelain", "--untracked-files=all").Output(); err != nil {
			return WorktreeOutcome{Kept: "its state could not be read: " + oneLine(err.Error())}, nil
		} else if trimmed := strings.TrimSpace(string(out)); trimmed != "" {
			files := strings.Count(trimmed, "\n") + 1
			return WorktreeOutcome{Kept: fmt.Sprintf("the worktree has %d uncommitted or untracked file(s); it and its branch are kept", files)}, nil
		}
		if out, err := Command(ctx, repoDir, "worktree", "remove", "--", path).CombinedOutput(); err != nil {
			Command(ctx, repoDir, "worktree", "prune").Run()
			return WorktreeOutcome{Kept: "the worktree is kept: " + oneLine(string(out))}, nil
		}
	}
	// A folder that is gone leaves its registration: prune clears it, so the next launch finds the name free.
	Command(ctx, repoDir, "worktree", "prune").Run()
	out := WorktreeOutcome{Removed: true}
	if branch != "" && refExists(ctx, repoDir, "refs/heads/"+branch) {
		if n := UnmergedCommits(ctx, repoDir, branch); n > 0 {
			out.Kept = fmt.Sprintf("the branch %s is kept: %d commit(s) on it are merged nowhere", branch, n)
		} else if err := Command(ctx, repoDir, "branch", "-D", "--", branch).Run(); err != nil {
			out.Kept = "the branch " + branch + " is kept: it could not be deleted"
		}
	}
	return out, nil
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
