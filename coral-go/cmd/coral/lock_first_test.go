//go:build !windows

package main

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/subgentic/coral/internal/startup/locktest"
)

// #6388: the real binary. A second coral started against a data folder another server holds loses the lock before config.Load makes or tightens the folder, before the Terms marker and before
// coral.log is opened: it exits non-zero with a message on stderr, and the folder's modes, files and coral.log are exactly as they were. Isolated home and data folder: never the live one.
func TestASecondCoralAgainstAHeldDataFolderTouchesNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the server")
	}
	bin, ok := locktest.Build(t, ".")
	if !ok {
		t.Skip("coral cannot be built here")
	}
	f := locktest.New(t)
	before := f.Snapshot(t)

	cmd := exec.Command(bin, "--host", "127.0.0.1", "--port", "0", "--no-browser")
	cmd.Env = f.Env("0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("the second coral exited 0 against a held data folder")
		}
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("the second coral did not exit")
	}
	if !strings.Contains(stderr.String(), "already running") {
		t.Errorf("stderr should say why: %q", stderr.String())
	}
	if same, why := locktest.Equal(before, f.Snapshot(t)); !same {
		t.Errorf("the losing coral changed the data folder: %s", why)
	}
}
