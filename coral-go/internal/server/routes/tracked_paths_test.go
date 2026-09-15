package routes

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// trackedPaths replaced a full `git ls-files` of the repository, so it has to
// answer exactly what that answered for the paths it is asked about.
func TestTrackedPathsAnswersOnlyAboutTheGivenPaths(t *testing.T) {
	repo := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		require.NoError(t, err, "%s", out)
	}
	write := func(rel, content string) {
		t.Helper()
		full := filepath.Join(repo, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0755))
		require.NoError(t, os.WriteFile(full, []byte(content), 0644))
	}

	runGit("init")
	runGit("config", "user.email", "test@example.com")
	runGit("config", "user.name", "Coral Test")
	write("tracked.go", "package main\n")
	write("nested/also_tracked.txt", "hello\n")
	runGit("add", "tracked.go", "nested/also_tracked.txt")
	runGit("commit", "-m", "initial")

	write("untracked.go", "package main\n")
	write("nested/untracked.txt", "new\n")

	ctx := context.Background()

	got := trackedPaths(ctx, repo, []string{
		"tracked.go", "nested/also_tracked.txt", "untracked.go", "nested/untracked.txt",
	})
	want := map[string]bool{"tracked.go": true, "nested/also_tracked.txt": true}
	for _, p := range []string{"tracked.go", "nested/also_tracked.txt"} {
		if !got[p] {
			t.Errorf("%q should be reported tracked", p)
		}
	}
	for _, p := range []string{"untracked.go", "nested/untracked.txt"} {
		if got[p] {
			t.Errorf("%q should not be reported tracked", p)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d tracked paths, want %d: %v", len(got), len(want), got)
	}
}

// The empty case must not shell out at all — it is the common case, since most
// refreshes have no agent-edited paths left to classify.
func TestTrackedPathsWithNoCandidatesIsEmpty(t *testing.T) {
	got := trackedPaths(context.Background(), t.TempDir(), nil)
	if len(got) != 0 {
		t.Fatalf("expected no tracked paths, got %v", got)
	}
}

// A path that does not exist is simply absent, not an error: ls-files exits 0
// with no output for an unmatched pathspec.
func TestTrackedPathsToleratesMissingPaths(t *testing.T) {
	repo := t.TempDir()
	out, err := exec.Command("git", "-C", repo, "init").CombinedOutput()
	require.NoError(t, err, "%s", out)

	got := trackedPaths(context.Background(), repo, []string{"never_existed.go"})
	if len(got) != 0 {
		t.Fatalf("expected no tracked paths, got %v", got)
	}
}
