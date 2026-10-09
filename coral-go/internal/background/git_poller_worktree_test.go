package background

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
)

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestQueryGitReportsWorktreeDivergence(t *testing.T) {
	main := t.TempDir()
	runGit(t, main, "init", "-q", "-b", "main")
	runGit(t, main, "commit", "-q", "--allow-empty", "-m", "base")

	wt := filepath.Join(t.TempDir(), "feature")
	runGit(t, main, "worktree", "add", "-q", "-b", "feature", wt)
	runGit(t, wt, "commit", "-q", "--allow-empty", "-m", "one")
	runGit(t, wt, "commit", "-q", "--allow-empty", "-m", "two")
	runGit(t, main, "commit", "-q", "--allow-empty", "-m", "main moves")

	info, err := queryGit(context.Background(), wt)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Worktree || info.Branch != "feature" || info.BaseBranch != "main" || info.Ahead != 2 || info.Behind != 1 {
		t.Fatalf("worktree info = %+v", info)
	}

	mainInfo, err := queryGit(context.Background(), main)
	if err != nil {
		t.Fatal(err)
	}
	if mainInfo.Worktree || mainInfo.Ahead != 0 || mainInfo.Behind != 0 || mainInfo.BaseBranch != "main" {
		t.Fatalf("main checkout info = %+v", mainInfo)
	}
}
