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

// BranchMissingError is a base branch the repository does not have (#6701). Its message is one plain sentence; the fields let a dashboard show it under the field and offer the branches.
type BranchMissingError struct {
	Branch   string   `json:"-"`
	Dir      string   `json:"-"`
	Branches []string `json:"branches"`
	Default  string   `json:"default_branch"` // the repository's current branch, "" when it has none
}

func (e *BranchMissingError) Error() string {
	list := "none"
	if len(e.Branches) > 0 {
		shown, more := e.Branches, ""
		if len(shown) > 20 {
			shown, more = shown[:20], fmt.Sprintf(" and %d more", len(shown)-20)
		}
		list = strings.Join(shown, ", ") + more
	}
	return fmt.Sprintf("The base branch %s does not exist in %s. Its branches: %s.", e.Branch, e.Dir, list)
}

// branchMissing names the branches that exist.
func branchMissing(ctx context.Context, repoDir, name string) error {
	out, _ := git(ctx, repoDir, "for-each-ref", "--format=%(refname:short)", "--sort=refname", "refs/heads")
	cur, _ := git(ctx, repoDir, "symbolic-ref", "--short", "-q", "HEAD")
	e := &BranchMissingError{Branch: name, Dir: repoDir, Branches: strings.Fields(out), Default: cur}
	if e.Branches == nil {
		e.Branches = []string{}
	}
	return e
}
