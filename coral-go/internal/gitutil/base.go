package gitutil

import (
	"context"
	"errors"
	"fmt"
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
		if ValidRef(requested) == nil && !refExists(ctx, repoDir, requested) { // a branch that is not there is said plainly, before git is asked to start from it
			return "", branchMissing(ctx, repoDir, requested)
		}
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

// branchMissing is the plain answer for a base branch the repository does not have: it names the branches that exist.
func branchMissing(ctx context.Context, repoDir, name string) error {
	out, _ := git(ctx, repoDir, "for-each-ref", "--format=%(refname:short)", "--sort=refname", "refs/heads")
	branches := strings.Fields(out)
	if len(branches) == 0 {
		return fmt.Errorf("the branch %q does not exist in this repository", name)
	}
	more := ""
	if len(branches) > 20 {
		branches, more = branches[:20], fmt.Sprintf(" and %d more", len(branches)-20)
	}
	return fmt.Errorf("the branch %q does not exist in this repository; the branches that do: %s%s", name, strings.Join(branches, ", "), more)
}
