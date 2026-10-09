package background

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestQueryChangedFilesUnquotedPaths(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("config", "user.email", "t@t")
	git("config", "user.name", "t")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x\n"), 0o644)
	git("add", ".")
	git("commit", "-qm", "init")

	name := "Screenshot at 5.55.51 PM.png"
	if err := os.WriteFile(filepath.Join(dir, name), []byte("png\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("mv", "a.txt", "b.txt")

	files, err := queryChangedFiles(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, f := range files {
		got[f.Filepath] = true
	}
	if !got[name] {
		t.Errorf("unquoted untracked path missing: %v", got)
	}
	if got["a.txt"] {
		t.Errorf("rename should report only the new path: %v", got)
	}
}
