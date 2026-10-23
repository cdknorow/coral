package gitutil

import (
	"context"
	"errors"
	"strings"
)

// ErrNoCommits: a repository with no commit has no branch to start a worktree from.
var ErrNoCommits = errors.New("the repository has no commits yet: make a first commit, or launch without a worktree")

// ResolveBase is the branch a team's or a job's worktree starts from (#6701). A branch the caller names is used as it is, except "main": that is what a request, a stored team and a stored job
// carried when nobody chose (the default Coral wrote), so it stands only where the repository has a main, and otherwise (a repository on master or any other branch) the choice falls to the
// repository's current branch, as an empty name does ("HEAD" when it is detached). ErrNoCommits for a repository with nothing to start from. The name is still checked by the caller (ValidRef).
func ResolveBase(ctx context.Context, repoDir, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested != "" && requested != "main" {
		return requested, nil
	}
	if requested == "main" && refExists(ctx, repoDir, "main") {
		return "main", nil
	}
	if !refExists(ctx, repoDir, "HEAD") {
		return "", ErrNoCommits
	}
	if branch, err := git(ctx, repoDir, "symbolic-ref", "--short", "-q", "HEAD"); err == nil && branch != "" {
		return branch, nil
	}
	return "HEAD", nil // detached: its commit
}

// refExists reports whether name resolves to a commit in the repository at dir.
func refExists(ctx context.Context, dir, name string) bool {
	_, err := git(ctx, dir, "rev-parse", "--verify", "--quiet", name+"^{commit}")
	return err == nil
}
