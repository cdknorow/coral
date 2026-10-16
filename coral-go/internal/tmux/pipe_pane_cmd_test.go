package tmux

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// #3930 (Security's #3927 finding 4): the pipe-pane command quotes the log path as one word, so
// a data folder with an apostrophe, a space or shell characters gets exactly that file and runs
// nothing else.
func TestPipePaneCommandQuotesTheLogPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("tmux and sh are Unix")
	}
	dir := filepath.Join(t.TempDir(), "o'brien $(touch pwned) x")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "claude-1.log")
	cmd := exec.Command("sh", "-c", "printf hello | "+pipePaneCommand(log))
	cmd.Dir = t.TempDir()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	b, err := os.ReadFile(log)
	if err != nil || string(b) != "hello" {
		t.Fatalf("the log at the exact path: %q, %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(cmd.Dir, "pwned")); !os.IsNotExist(err) {
		t.Error("the path's text ran as a command")
	}
}
