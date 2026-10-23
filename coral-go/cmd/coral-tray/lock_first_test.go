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

// #6388: the tray's server child against a data folder another server holds writes nothing in it and exits with the start-failed code (the tray, which runs it, shows the error).
func TestATrayServerChildAgainstAHeldDataFolderTouchesNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the program")
	}
	bin, ok := locktest.Build(t, ".")
	if !ok {
		t.Skip("coral-tray cannot be built here (it needs its platform libraries)")
	}
	f := locktest.New(t)
	before := f.Snapshot(t)
	cmd := exec.Command(bin, "--server-child", "--port", "0")
	cmd.Env = f.Env("0")
	var out bytes.Buffer
	cmd.Stderr, cmd.Stdout = &out, &out
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		ee, isExit := err.(*exec.ExitError)
		if !isExit || ee.ExitCode() != childExitStartFailed {
			t.Fatalf("the child should exit %d, got %v", childExitStartFailed, err)
		}
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("the tray's server child did not exit")
	}
	if !strings.Contains(out.String(), "already running") {
		t.Errorf("the output should say why: %q", out.String())
	}
	if same, why := locktest.Equal(before, f.Snapshot(t)); !same {
		t.Errorf("the losing child changed the data folder: %s", why)
	}
}
