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

// #6388: a second launch-coral against a data folder another server holds writes nothing in it and exits non-zero.
func TestASecondLaunchCoralAgainstAHeldDataFolderTouchesNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the program")
	}
	bin, ok := locktest.Build(t, ".")
	if !ok {
		t.Skip("launch-coral cannot be built here")
	}
	f := locktest.New(t)
	before := f.Snapshot(t)
	cmd := exec.Command(bin, "--no-browser", t.TempDir())
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
			t.Fatal("launch-coral exited 0 against a held data folder")
		}
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("launch-coral did not exit")
	}
	if !strings.Contains(stderr.String(), "already running") {
		t.Errorf("stderr should say why: %q", stderr.String())
	}
	if same, why := locktest.Equal(before, f.Snapshot(t)); !same {
		t.Errorf("the losing launch-coral changed the data folder: %s", why)
	}
}
