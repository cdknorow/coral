package gitutil

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createTestRepo creates a git repo with at least 2 commits for testing.
func createTestRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := t.TempDir()
	ctx := context.Background()

	run := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v failed: %s", args, out)
	}

	run("init", "-b", "main")
	os.WriteFile(filepath.Join(dir, "file1.txt"), []byte("first"), 0644)
	run("add", ".")
	run("commit", "-m", "first commit")

	os.WriteFile(filepath.Join(dir, "file2.txt"), []byte("second"), 0644)
	run("add", ".")
	run("commit", "-m", "second commit")

	return dir
}

// createTestRepoWithBranch creates a repo with main + feature branch.
func createTestRepoWithBranch(t *testing.T) string {
	t.Helper()
	dir := createTestRepo(t)
	ctx := context.Background()

	run := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v failed: %s", args, out)
	}

	run("checkout", "-b", "feature")
	os.WriteFile(filepath.Join(dir, "feature.txt"), []byte("feature"), 0644)
	run("add", ".")
	run("commit", "-m", "feature commit")

	return dir
}

// ── GetDiffBase Tests ───────────────────────────────────────────────

func TestGetDiffBase_PreviousCommit(t *testing.T) {
	dir := createTestRepo(t)
	ctx := context.Background()

	result := GetDiffBase(ctx, dir, "previous_commit")
	assert.Equal(t, "HEAD~1", result, "should return HEAD~1 when previous commit exists")
}

func TestGetDiffBase_PreviousCommit_SingleCommit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := t.TempDir()
	ctx := context.Background()

	run := func(args ...string) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v failed: %s", args, out)
	}

	run("init", "-b", "main")
	os.WriteFile(filepath.Join(dir, "only.txt"), []byte("only"), 0644)
	run("add", ".")
	run("commit", "-m", "only commit")

	result := GetDiffBase(ctx, dir, "previous_commit")
	assert.Equal(t, "HEAD", result, "should fallback to HEAD when no previous commit")
}

func TestGetDiffBase_BranchPoint_OnMain(t *testing.T) {
	dir := createTestRepo(t)
	ctx := context.Background()

	result := GetDiffBase(ctx, dir, "branch_point")
	assert.Equal(t, "HEAD", result, "should return HEAD when on main branch")
}

func TestGetDiffBase_BranchPoint_OnFeature(t *testing.T) {
	dir := createTestRepoWithBranch(t)
	ctx := context.Background()

	result := GetDiffBase(ctx, dir, "branch_point")
	assert.NotEqual(t, "HEAD", result, "should return merge-base hash on feature branch")
	assert.NotEqual(t, "HEAD~1", result, "should return merge-base hash, not HEAD~1")
	// The result should be a commit hash (40 hex chars)
	assert.Len(t, result, 40, "merge-base should be a full commit hash")
}

func TestGetDiffBase_DefaultMode_IsBranchPoint(t *testing.T) {
	dir := createTestRepo(t)
	ctx := context.Background()

	// Empty mode should behave like "branch_point"
	resultEmpty := GetDiffBase(ctx, dir, "")
	resultExplicit := GetDiffBase(ctx, dir, "branch_point")
	assert.Equal(t, resultExplicit, resultEmpty, "empty mode should default to branch_point")
}

func TestGetDiffBase_MainHead_OnMainBranch(t *testing.T) {
	dir := createTestRepo(t)
	ctx := context.Background()

	result := GetDiffBase(ctx, dir, "main_head")
	assert.Equal(t, "main", result, "should return 'main' when on main branch and main exists")
}

func TestGetDiffBase_MainHead_OnFeatureBranch(t *testing.T) {
	dir := createTestRepoWithBranch(t)
	ctx := context.Background()

	// On feature branch, main_head should still return "main"
	result := GetDiffBase(ctx, dir, "main_head")
	assert.Equal(t, "main", result, "should return 'main' even when on feature branch")
}

func TestGetDiffBase_NotAGitRepo(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	result := GetDiffBase(ctx, dir, "previous_commit")
	assert.Equal(t, "HEAD", result, "should return HEAD for non-git directory")
}

func TestParseRepoName(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"git@github.com:cdknorow/coral.git", "cdknorow/coral"},
		{"git@github.com:org/repo", "org/repo"},
		{"https://github.com/cdknorow/coral.git", "cdknorow/coral"},
		{"https://github.com/cdknorow/coral", "cdknorow/coral"},
		{"https://gitlab.com/org/sub/repo.git", "org/sub/repo"},
		{"", ""},
		{"not-a-url", ""},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, ParseRepoName(tt.input), "ParseRepoName(%q)", tt.input)
	}
}

// #3558: credentials in a remote URL are never kept.
func TestStripURLCredentials(t *testing.T) {
	for in, want := range map[string]string{
		"https://user:ghp_TOKEN@github.com/org/repo.git":  "https://github.com/org/repo.git",
		"https://x-access-token:abc@github.com/org/r":     "https://github.com/org/r",
		"https://oauth2:glpat-xyz@gitlab.example/g/p.git": "https://gitlab.example/g/p.git",
		"ssh://git:pw@host:22/r.git":                      "ssh://host:22/r.git",
		"https://github.com/org/repo.git":                 "https://github.com/org/repo.git",
		"git@github.com:org/repo.git":                     "git@github.com:org/repo.git",
		"https://host/path/with@at":                       "https://host/path/with@at",
		"https://a:b@c@host/r":                            "https://host/r",
		"":                                                "",
		"https://host/org/r.git?access_token=X#frag":      "https://host/org/r.git",
		"https://host/r#t=X":                              "https://host/r",
		"https://user:pa#ss@host/r":                       "https://host/r",
		"https://user:pa?ss@host/r":                       "https://host/r",
		"https://tok@host?x=1":                            "https://host",
		"user:pass@host:org/repo.git":                     "host:org/repo.git",
		"git@host:org/r@v.git":                            "git@host:org/r@v.git",
		"/srv/git/repo@x:y":                               "/srv/git/repo@x:y",
	} {
		if got := StripURLCredentials(in); got != want {
			t.Errorf("StripURLCredentials(%q) = %q, want %q", in, got, want)
		}
	}
}

// #6144: the diff base of a repository with no commit is the empty tree, not HEAD (which does not exist); a repository with a commit keeps HEAD; a folder that is no repository keeps HEAD.
func TestGetDiffBaseWithNoCommit(t *testing.T) {
	if !Available() {
		t.Skip("git not installed")
	}
	repo := t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "init", "-q", "-b", "main").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	if got := GetDiffBase(context.Background(), repo, ""); got != emptyTreeSHA1 && got != emptyTreeSHA256 {
		t.Fatalf("a repo with no commit diffs against the empty tree, got %q", got)
	}
	for _, mode := range []string{"previous_commit", "main_head", "branch_point"} {
		if got := GetDiffBase(context.Background(), repo, mode); got != emptyTreeSHA1 && got != emptyTreeSHA256 {
			t.Fatalf("mode %s: the empty tree, got %q", mode, got)
		}
	}
	if got := GetDiffBase(context.Background(), t.TempDir(), ""); got != "HEAD" {
		t.Fatalf("a folder that is no repository keeps HEAD, got %q", got)
	}
	cmd := exec.Command("git", "-C", repo, "-c", "user.name=t", "-c", "user.email=t@example.test", "commit", "-q", "--allow-empty", "-m", "first")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v %s", err, out)
	}
	if got := GetDiffBase(context.Background(), repo, ""); got != "HEAD" {
		t.Fatalf("a repo with a commit keeps HEAD, got %q", got)
	}
}
