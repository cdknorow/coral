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

// RemoveWorktree takes a worktree out of the repository that owns it: the folder goes, and so does its registration (git worktree prune). repoDir is where git runs: the team's or the job's own
// repository (a worktree's parent folder is not one, #6701); empty, or one that is no longer a repository, it is found from the worktree itself. Its branch stays: commits made on it are the
// operator's. A worktree already gone is not an error.
func RemoveWorktree(ctx context.Context, repoDir, path string) error {
	if repoDir == "" || !IsWorkTree(ctx, repoDir) {
		repoDir = RepoOfWorktree(ctx, path)
	}
	if repoDir == "" {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return nil // nothing there and no repository to unregister it from
		}
		return fmt.Errorf("%s is not a git worktree of a repository Coral knows", path)
	}
	out, err := Command(ctx, repoDir, "worktree", "remove", "--force", "--", path).CombinedOutput()
	// A folder that is gone leaves its registration: prune clears it, so the next launch finds the branch and the name free.
	Command(ctx, repoDir, "worktree", "prune").Run()
	if err != nil {
		if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
			return nil
		}
		return fmt.Errorf("git worktree remove: %s", oneLine(string(out)))
	}
	return nil
}

// oneLine is git's output as a single line, for a message the operator reads.
func oneLine(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(s, "fatal: ", "")), " ")
}
