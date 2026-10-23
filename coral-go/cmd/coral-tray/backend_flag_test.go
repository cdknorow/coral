package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/backendchoice"
)

// coral-tray takes its own --backend and passes it to the server it supervises: on Windows a forced tmux stops the tray with the same one line, exit 2, before it starts anything (#304).
func TestTrayRefusesAForcedTmuxBackendOnWindows(t *testing.T) {
	if args := os.Getenv("CORAL_TEST_TRAY_ARGS"); args != "" {
		os.Args = append([]string{"coral-tray"}, strings.Split(args, " ")...)
		main()
		return
	}
	if runtime.GOOS != "windows" {
		t.Skip("only Windows refuses it")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestTrayRefusesAForcedTmuxBackendOnWindows$")
	dir := t.TempDir()
	cmd.Env = append(os.Environ(), "CORAL_TEST_TRAY_ARGS=--no-browser --backend tmux", "CORAL_DATA_DIR="+dir, "HOME="+dir, "USERPROFILE="+dir)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	require.True(t, errors.As(err, &exit), "it must exit non-zero: %v", err)
	assert.Equal(t, 2, exit.ExitCode(), stderr.String())
	assert.Contains(t, stderr.String(), "coral-tray: tmux isn't supported on Windows")
	entries, _ := os.ReadDir(dir)
	assert.Empty(t, entries, "nothing was started or written")
}

// What a normal start passes (an empty --backend, or pty) is never refused.
func TestTrayAcceptsTheBackendsANormalStartPasses(t *testing.T) {
	for _, v := range []string{"", "pty"} {
		assert.Empty(t, backendchoice.FlagProblem("windows", v), "%q", v)
	}
}
