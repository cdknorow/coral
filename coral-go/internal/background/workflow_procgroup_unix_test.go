//go:build !windows

package background

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store"
)

// alive reports whether pid still exists (kill 0 succeeds).
func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// readPID waits for a pid written to path by a test command.
func readPID(t *testing.T, path string) int {
	t.Helper()
	var pid int
	require.Eventually(t, func() bool {
		b, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(b)))
		return err == nil && pid > 0
	}, 5*time.Second, 10*time.Millisecond, "the command never wrote its pid")
	// Only this test's recorded process, if anything goes wrong.
	t.Cleanup(func() {
		if alive(pid) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	return pid
}

// A shell step that times out takes its whole process group with it: a
// backgrounded grandchild doesn't survive (audit HIGH-03). Before, only the
// step's /bin/sh was signalled and `sleep 600` kept running.
func TestShellStepTimeoutKillsTheProcessGroup(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "grandchild.pid")
	wr := NewWorkflowRunner(store.NewWorkflowStore(setupTestDB(t)), nil, nil, nil, nil, dir, "localhost", 8420)
	step := StepDef{Name: "hang", Type: "shell", TimeoutS: 1,
		Command: "sleep 600 & echo $! > '" + pidFile + "'; wait"}

	var res StepResult
	start := time.Now()
	err := wr.executeShellStep(context.Background(), 1, step, testStepFiles(t), dir, nil, &res)
	require.Error(t, err, "a timed-out step fails")
	assert.Less(t, time.Since(start), 30*time.Second, "the step returned at its timeout")

	pid := readPID(t, pidFile)
	assert.Eventually(t, func() bool { return !alive(pid) }, 10*time.Second, 50*time.Millisecond,
		"the backgrounded sleep (pid %d) survived the step's timeout", pid)
}

// Hook commands run in the directory they're given, in their own process
// group (audit LOW-01/02).
func TestHooksRunInTheirDirectoryAndProcessGroup(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(t.TempDir(), "hook.out")
	wr := NewWorkflowRunner(store.NewWorkflowStore(setupTestDB(t)), nil, nil, nil, nil, t.TempDir(), "localhost", 8420)
	hooks := map[string]interface{}{"StepComplete": []interface{}{map[string]interface{}{
		"hooks": []interface{}{map[string]interface{}{"type": "command",
			"command": "{ pwd -P; ps -o pgid= -p $$; echo $$; } > '" + out + "'"}},
	}}}
	wr.fireHooks(context.Background(), hooks, "StepComplete", dir, nil)

	b, err := os.ReadFile(out)
	require.NoError(t, err)
	lines := strings.Fields(string(b))
	require.Len(t, lines, 3, "pwd, pgid and pid: %q", b)
	want, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	assert.Equal(t, want, lines[0], "the hook ran in its directory")
	assert.Equal(t, lines[2], lines[1], "the hook's shell leads its own process group")
}
