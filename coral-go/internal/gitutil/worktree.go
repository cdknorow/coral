package gitutil

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
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
		// No --force, and no second try: git itself refuses a worktree with modified or untracked files, a locked one, a broken one; that worktree stays and the person is told.
		if out, err := Command(ctx, repoDir, "worktree", "remove", "--", path).CombinedOutput(); err != nil {
			Command(ctx, repoDir, "worktree", "prune").Run()
			res.State = "kept"
			switch text := string(out); {
			case strings.Contains(text, "modified or untracked"):
				res.Reason = "it has uncommitted changes"
			case strings.Contains(text, "locked"):
				res.Reason = "the worktree is locked"
			default:
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

// maxWorktreePath is the longest worktree folder path accepted: git and Windows stop well before the 260 of MAX_PATH once a repository's own paths are added.
const maxWorktreePath = 200

// AddTeamWorktree makes the worktree at path on branch, from base when the branch is new, and says whether it made the branch (#6701). What an earlier run left is met, not tripped over: a registration whose
// folder is gone is pruned, a branch that exists is checked out (its commits kept) instead of made again, a worktree already at path is reused, and an empty folder is used. A folder that is not empty and
// no worktree is the one thing it refuses, in one line. Every failure is one plain line, not git's two.
func AddTeamWorktree(ctx context.Context, repoDir, path, branch, base string) (branchCreated bool, err error) {
	repoDir, path = filepath.Clean(repoDir), filepath.Clean(path)
	if len(path) > maxWorktreePath {
		return false, fmt.Errorf("the worktree's folder path is longer than %d characters: use a shorter board name or a shorter data folder", maxWorktreePath)
	}
	if info, statErr := os.Stat(path); statErr == nil && !info.IsDir() {
		return false, fmt.Errorf("the worktree's folder %s is a file: move or delete it, then launch again", path)
	}
	// Two names that differ only in case are one folder on a case-insensitive disk (macOS, Windows): the second would land in the first team's worktree.
	if entries, readErr := os.ReadDir(filepath.Dir(path)); readErr == nil {
		for _, e := range entries {
			if e.Name() != filepath.Base(path) && strings.EqualFold(e.Name(), filepath.Base(path)) {
				return false, fmt.Errorf("a worktree folder named %s exists, and a name that differs only in case is the same folder on this computer: use another board name", e.Name())
			}
		}
	}
	Command(ctx, repoDir, "worktree", "prune").Run()
	if entries, readErr := os.ReadDir(path); readErr == nil && len(entries) > 0 {
		if registered(ctx, repoDir, path) {
			return false, nil // the worktree is still there (a stop that kept it): the team goes on in it
		}
		return false, fmt.Errorf("the folder %s is left over from an earlier run and is not empty: move or delete it, then launch again", path)
	}
	// core.longpaths: Git for Windows stops at 260 characters unless told otherwise, and a repository with a deep tracked file failed the checkout (#6717).
	args := []string{"-c", "core.longpaths=true", "worktree", "add", "--", path, branch}
	if !refExists(ctx, repoDir, "refs/heads/"+branch) {
		args = []string{"-c", "core.longpaths=true", "worktree", "add", "-b", branch, "--", path, base}
		branchCreated = true
	}
	if out, addErr := Command(ctx, repoDir, args...).CombinedOutput(); addErr != nil {
		if branchCreated { // a failed add leaves no branch of ours behind: it holds nothing yet
			Command(ctx, repoDir, "worktree", "prune").Run()
			Command(ctx, repoDir, "branch", "-D", "--", branch).Run()
		}
		return false, fmt.Errorf("git worktree add: %s", oneLine(string(out)))
	}
	return branchCreated, nil
}

// IsRegisteredWorktree reports whether path is one of the repository's worktrees.
func IsRegisteredWorktree(ctx context.Context, repoDir, path string) bool {
	return registered(ctx, repoDir, path)
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
	return "coral-team/" + slug
}
