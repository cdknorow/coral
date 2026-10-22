package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The real binary on Windows: exit 2 with the one line, before anything a server does.
func TestWindowsRefusesAForcedTmuxBackendAtStartup(t *testing.T) {
	if args := os.Getenv("CORAL_TEST_MAIN_ARGS_TMUX"); args != "" {
		os.Args = append([]string{"coral"}, strings.Split(args, " ")...)
		main()
		return
	}
	if runtime.GOOS != "windows" {
		t.Skip("only Windows refuses it")
	}
	dataDir, home := t.TempDir(), t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWindowsRefusesAForcedTmuxBackendAtStartup$")
	cmd.Env = append(cleanEnv(), "CORAL_TEST_MAIN_ARGS_TMUX=--no-browser --backend tmux", "CORAL_DATA_DIR="+dataDir, "HOME="+home,
		"CORAL_PORT="+strconv.Itoa(closedPort(t)), "CORAL_HOST=127.0.0.1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	require.True(t, errors.As(err, &exit), "it must exit non-zero: %v", err)
	assert.Equal(t, 2, exit.ExitCode(), stderr.String())
	assert.Contains(t, stderr.String(), "coral: tmux isn't supported on Windows")
	assert.NotContains(t, stderr.String()+stdout.String(), "[STARTUP]", "no server startup")
	entries, _ := os.ReadDir(dataDir)
	assert.Empty(t, entries, "nothing was written to the data folder")
}
