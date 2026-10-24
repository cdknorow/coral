package gitutil

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/subgentic/coral/internal/clitext"
)

// ErrNoCommits: a repository with no commit has no branch to start a worktree from.
var ErrDetached = errors.New("the repository is on a detached HEAD, which is no branch to start from: name the base branch")

var ErrBareRepo = errors.New("the repository is bare: it has no working folder to start a worktree from; use a clone")

// ErrNotARepo: git could not read the folder as a repository (not one, or a path git cannot open).
var ErrNotARepo = errors.New("git cannot read this folder as a repository: check the path")

var ErrNoCommits = errors.New("the repository has no commits yet: make a first commit, or launch without a worktree")

// ResolveBase is the branch a team's or a job's worktree starts from (#6701). A branch the caller names is used as it is, except "main": that is what a request, a stored team and a stored job
// carried when nobody chose (the default Coral wrote), so it stands only where the repository has a main, and otherwise (a repository on master or any other branch) the choice falls to the
// repository's current branch, as an empty name does ("HEAD" when it is detached). ErrNoCommits for a repository with nothing to start from. The name is still checked by the caller (ValidRef).
func ResolveBase(ctx context.Context, repoDir, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested != "" && requested != "main" {
		if ValidRef(requested) == nil && !refExists(ctx, repoDir, requested) {
			// A branch that exists only on the remote starts from the remote's: older gits guess it, newer ones refuse (Windows git 2.56, #6717).
			if remote := "origin/" + requested; refExists(ctx, repoDir, "refs/remotes/"+remote) {
				return remote, nil
			}
			return "", branchMissing(ctx, repoDir, requested) // said plainly, before git is asked to start from it
		}
		return requested, nil
	}
	if requested == "main" && refExists(ctx, repoDir, "main") {
		return "main", nil
	}
	if !refExists(ctx, repoDir, "HEAD") {
		// "No commits yet" is said only of a repository git can read: a bare one or an unreadable path (Windows' 260-character limit) are not that (QA's #6722).
		if bare, err := git(ctx, repoDir, "rev-parse", "--is-bare-repository"); err == nil && bare == "true" {
			return "", ErrBareRepo
		} else if err != nil {
			return "", ErrNotARepo
		}
		return "", ErrNoCommits
	}
	if branch, err := git(ctx, repoDir, "symbolic-ref", "--short", "-q", "HEAD"); err == nil && branch != "" {
		return branch, nil
	}
	return "", ErrDetached
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
	more     int
}

func (e *BranchMissingError) Error() string {
	list := "none"
	if len(e.Branches) > 0 {
		list = strings.Join(e.Branches, ", ")
		if e.more > 0 {
			list += fmt.Sprintf(" and %d more", e.more)
		}
	}
	return fmt.Sprintf("The base branch %s does not exist in %s. Its branches: %s.", e.Branch, e.Dir, list)
}

// branchText is a branch name from the repository or the request as text to show: control and invisible characters taken out, at most 100 characters.
func branchText(s string) string {
	s = clitext.CleanName(s)
	if r := []rune(s); len(r) > 100 {
		s = string(r[:100]) + "..."
	}
	return s
}

// branchMissing names the branches that exist: at most 20, each cleaned, the rest counted.
func branchMissing(ctx context.Context, repoDir, name string) error {
	out, _ := git(ctx, repoDir, "for-each-ref", "--format=%(refname:short)", "--sort=refname", "refs/heads")
	cur, _ := git(ctx, repoDir, "symbolic-ref", "--short", "-q", "HEAD")
	var all []string
	for _, b := range strings.Fields(out) {
		// Coral's own branches (a team's coral-team/<board>, a job run's coral/job-run-<id>) are not bases to offer: kept worktrees would crowd the list (#6817, UI Accessibility's #6806).
		if strings.HasPrefix(b, "coral-team/") || strings.HasPrefix(b, "coral/job-run-") {
			continue
		}
		all = append(all, b)
	}
	e := &BranchMissingError{Branch: branchText(name), Dir: repoDir, Branches: []string{}, Default: branchText(cur)}
	for i, b := range all {
		if i == 20 {
			e.more = len(all) - 20
			break
		}
		e.Branches = append(e.Branches, branchText(b))
	}
	return e
}

// CurrentBranch is the branch the repository at dir is on, "" when it has no commit yet or HEAD is detached (the dashboard prefills Base Branch with it, #6701).
func CurrentBranch(ctx context.Context, dir string) string {
	if !refExists(ctx, dir, "HEAD") {
		return ""
	}
	branch, _ := git(ctx, dir, "symbolic-ref", "--short", "-q", "HEAD")
	return branchText(branch)
}
