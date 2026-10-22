// Package gitutil provides shared git helper functions.
// All functions gracefully handle the case where git is not installed
// or the directory is not a git repository.
package gitutil

import (
	"bytes"
	"context"
	"errors"
	"github.com/subgentic/coral/internal/safefs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

var (
	gitAvailable     bool
	gitAvailableOnce sync.Once
)

// Available returns true if git is installed and accessible on PATH.
func Available() bool {
	gitAvailableOnce.Do(func() {
		_, err := exec.LookPath("git")
		gitAvailable = err == nil
	})
	return gitAvailable
}

// git runs a git command in the given directory and returns trimmed stdout.
// Returns ("", err) if git is not available or the command fails.
func git(ctx context.Context, workdir string, args ...string) (string, error) {
	if !Available() {
		return "", exec.ErrNotFound
	}
	out, err := Command(ctx, workdir, args...).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// IsWorkTree reports whether dir is inside a git working tree (and git is installed).
func IsWorkTree(ctx context.Context, dir string) bool {
	out, err := git(ctx, dir, "rev-parse", "--is-inside-work-tree")
	return err == nil && out == "true"
}

// ResolveGitRoot finds the git toplevel for a directory.
// If the directory itself isn't a git repo, checks one level of subdirectories.
// Returns the original workdir if no git repo is found.
func ResolveGitRoot(ctx context.Context, workdir string) string {
	if !Available() {
		return workdir
	}
	if root, err := git(ctx, workdir, "rev-parse", "--show-toplevel"); err == nil {
		return nativePath(root)
	}
	// workdir isn't a repo — check one level of subdirectories
	entries, _ := os.ReadDir(workdir)
	for _, e := range entries {
		if ctx.Err() != nil {
			return workdir
		}
		if !e.IsDir() {
			continue
		}
		sub := filepath.Join(workdir, e.Name())
		// A child repository must have a .git directory or worktree gitfile.
		// Avoid spawning Git for every ordinary directory in large workspaces.
		if _, err := os.Stat(filepath.Join(sub, ".git")); err != nil {
			continue
		}
		if root, err := git(ctx, sub, "rev-parse", "--show-toplevel"); err == nil {
			return nativePath(root)
		}
	}
	return workdir
}

// nativePath turns a path git printed into this system's form: on Windows git prints
// C:/Users/x/repo, which compared or joined with Windows paths (C:\Users\x\repo) never
// matches (#3631).
func nativePath(p string) string { return filepath.Clean(filepath.FromSlash(p)) }

// GetDiffBase returns the base ref for diffing.
//
// When mode is "previous_commit", returns "HEAD~1" to diff against the
// previous commit on the current branch. Falls back to "HEAD" if there
// is no previous commit.
//
// When mode is "branch_point" (or empty, the default), returns the
// merge-base with main/master on feature branches, or "HEAD" on
// main/master (uncommitted changes only).
func GetDiffBase(ctx context.Context, workdir, mode string) string {
	if !Available() {
		return "HEAD"
	}

	// A repository with no commit yet (a fresh "git init": HEAD points at a branch that does not exist) has no HEAD to diff against, and "git diff HEAD" fails: its changes are what the empty tree
	// becomes, so every file there is new (#6144).
	if _, err := git(ctx, workdir, "rev-parse", "--verify", "--quiet", "HEAD"); err != nil {
		if _, inRepo := git(ctx, workdir, "rev-parse", "--git-dir"); inRepo == nil {
			return emptyTree(ctx, workdir)
		}
	}

	if mode == "previous_commit" {
		// Check that HEAD~1 exists (at least 2 commits)
		if _, err := git(ctx, workdir, "rev-parse", "--verify", "HEAD~1"); err == nil {
			return "HEAD~1"
		}
		return "HEAD"
	}

	if mode == "main_head" {
		// Diff against main/master branch HEAD directly
		for _, branch := range []string{"main", "master"} {
			if _, err := git(ctx, workdir, "rev-parse", "--verify", branch); err == nil {
				return branch
			}
		}
		return "HEAD"
	}

	// Default: branch_point (merge-base)
	branch, err := git(ctx, workdir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "HEAD"
	}
	if branch == "main" || branch == "master" || branch == "HEAD" || branch == "" {
		return "HEAD"
	}
	for _, baseBranch := range []string{"main", "master"} {
		if base, err := git(ctx, workdir, "merge-base", baseBranch, "HEAD"); err == nil && base != "" {
			return base
		}
	}
	return "HEAD"
}

// The empty tree's object id, which a repository has whether or not the object is stored: the sha1 and the sha256 repositories' are different.
const (
	emptyTreeSHA1   = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
	emptyTreeSHA256 = "6ef19b41225c5369f1c104d45d8d85efa9b057b53b14b4b9b939dd74decc5321"
)

// emptyTree is the empty tree of the repository at workdir.
func emptyTree(ctx context.Context, workdir string) string {
	if format, err := git(ctx, workdir, "rev-parse", "--show-object-format"); err == nil && format == "sha256" {
		return emptyTreeSHA256
	}
	return emptyTreeSHA1
}

// ParseRepoName extracts a short "org/repo" name from a git remote URL.
// Handles SSH (git@github.com:org/repo.git) and HTTPS (https://github.com/org/repo.git).
// Returns "" if the URL is empty or unparseable.
func ParseRepoName(remoteURL string) string {
	if remoteURL == "" {
		return ""
	}
	u := strings.TrimSuffix(remoteURL, ".git")

	// SSH: git@github.com:org/repo
	if strings.HasPrefix(u, "git@") {
		parts := strings.SplitN(u, ":", 2)
		if len(parts) == 2 {
			return parts[1]
		}
	}

	// HTTPS: https://github.com/org/repo (or any host)
	if idx := strings.LastIndex(u, "://"); idx >= 0 {
		path := u[idx+3:]
		if slashIdx := strings.Index(path, "/"); slashIdx >= 0 {
			return path[slashIdx+1:]
		}
	}

	return ""
}

// ShowPrefix returns the path prefix of the workdir within the repo (e.g. "subdir/").
// Returns "" if at the repo root or if git is not available.
func ShowPrefix(ctx context.Context, workdir string) string {
	if !Available() {
		return ""
	}
	prefix, _ := git(ctx, workdir, "rev-parse", "--show-prefix")
	return prefix
}

// MaxNewFileStatFiles caps how many new files one scan will read to count
// lines. Past this the stat is not worth the I/O: a working tree with that
// many new files is not one anybody reads per-file counts off, and on a large
// repo the reads are the expensive part of refreshing the list.
const MaxNewFileStatFiles = 500

// MaxNewFileStatBytes caps how large a new file we will read just to count its
// lines for changed-file stats.
const MaxNewFileStatBytes = 2 << 20

// NewFileLineCountIn is NewFileLineCount for rel inside root: a symlinked folder on
// the way out of the root is refused too.
func NewFileLineCountIn(root *safefs.Root, rel string) int {
	data, err := root.ReadFile(rel, MaxNewFileStatBytes)
	if err != nil || len(data) == 0 || isBinary(data) {
		return 0
	}
	n := strings.Count(string(data), "\n")
	if data[len(data)-1] != '\n' {
		n++
	}
	return n
}

// binarySniffBytes matches the prefix git inspects when deciding whether a
// blob is binary.
const binarySniffBytes = 8000

// isBinary reports whether content looks binary, using git's heuristic: a NUL
// byte in the leading chunk.
func isBinary(data []byte) bool {
	if len(data) > binarySniffBytes {
		data = data[:binarySniffBytes]
	}
	return bytes.IndexByte(data, 0) >= 0
}

// StripURLCredentials removes the userinfo (user:password@ or token@) and any ?query or
// #fragment from a URL with a scheme ("https://user:TOKEN@host/repo.git?t=X" becomes
// "https://host/repo.git"), so a stored or served remote URL never carries a credential
// (#3558, Security's #3556 finding 2 and SHOULD 2). The userinfo is everything before the last
// "@" ahead of the first "/" after the scheme, so a "?" or "#" inside a password is removed with
// it. An scp-style remote keeps a plain user ("git@host:org/repo") and loses a password
// ("user:pass@host:org/repo" becomes "host:org/repo"; QA's #3558 review).
func StripURLCredentials(raw string) string {
	i := strings.Index(raw, "://")
	if i < 0 {
		return stripSCPCredentials(raw)
	}
	rest := raw[i+3:]
	end := len(rest)
	if j := strings.IndexByte(rest, '/'); j >= 0 {
		end = j
	}
	if at := strings.LastIndex(rest[:end], "@"); at >= 0 {
		rest = rest[at+1:]
	}
	if q := strings.IndexAny(rest, "?#"); q >= 0 {
		rest = rest[:q]
	}
	return raw[:i+3] + rest
}

// stripSCPCredentials drops a userinfo that holds a password (has a ':') from an scp-style
// remote, user:pass@host:path; the userinfo is everything before the last "@" ahead of the
// first "/". A local path (a leading "/", or a "\\" in the userinfo) is left alone.
func stripSCPCredentials(raw string) string {
	end := len(raw)
	if j := strings.IndexByte(raw, '/'); j >= 0 {
		end = j
	}
	at := strings.LastIndex(raw[:end], "@")
	if at < 0 {
		return raw
	}
	if user := raw[:at]; strings.Contains(user, ":") && !strings.Contains(user, "\\") {
		return raw[at+1:]
	}
	return raw
}

// ValidRef says why ref can't be given to git as a branch or revision, or nil (#3601, weak site
// W24): never one starting with '-', which git would read as an option, nor one with
// whitespace, a control character or a backslash, or longer than 255 bytes. A name like
// "main", "origin/main", "release-1.2" or a commit hash passes.
func ValidRef(ref string) error {
	switch {
	case ref == "":
		return errors.New("the branch is empty")
	case len(ref) > 255:
		return errors.New("the branch name is longer than 255 characters")
	case strings.HasPrefix(ref, "-"):
		return errors.New("a branch name can't start with '-'")
	}
	for _, c := range ref {
		if c <= ' ' || c == 0x7f || c == '\\' {
			return errors.New("a branch name can't contain spaces, control characters or a backslash")
		}
	}
	return nil
}
