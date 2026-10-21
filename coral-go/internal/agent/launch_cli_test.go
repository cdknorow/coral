package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pathWithStub makes a runnable program called name, puts its folder alone on PATH, and returns its path.
func pathWithStub(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	file := name
	if runtime.GOOS == "windows" {
		file += ".exe"
	}
	p := filepath.Join(dir, file)
	require.NoError(t, os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	t.Setenv("PATH", dir)
	return p
}

// A configured cli_path_<agent> that is missing or not a program is refused with an error naming the setting and the path; the claude
// that is first on PATH is never run in its place (#115).
func TestLaunchCLIRefusesAConfiguredPathThatCannotBeRun(t *testing.T) {
	onPath := pathWithStub(t, "claude")
	key := CLIPathSettingKey("claude")

	t.Run("set and missing", func(t *testing.T) {
		gone := filepath.Join(t.TempDir(), "gone", "claude")
		got, err := LaunchCLI(map[string]string{key: gone}, "claude")
		require.Error(t, err)
		assert.Empty(t, got)
		assert.Contains(t, err.Error(), key)
		assert.Contains(t, err.Error(), gone)
		assert.NotContains(t, err.Error(), onPath)
	})
	t.Run("set and not executable", func(t *testing.T) {
		notProg := filepath.Join(t.TempDir(), "claude.txt") // no execute bit on Unix; not an executable extension on Windows
		require.NoError(t, os.WriteFile(notProg, []byte("not a program"), 0o644))
		got, err := LaunchCLI(map[string]string{key: notProg}, "claude")
		require.Error(t, err)
		assert.Empty(t, got)
		assert.Contains(t, err.Error(), key)
		assert.Contains(t, err.Error(), notProg)
	})
	t.Run("set and runnable", func(t *testing.T) {
		mine := pathWithStub(t, "my-claude")
		got, err := LaunchCLI(map[string]string{key: mine}, "claude")
		require.NoError(t, err)
		assert.Equal(t, mine, got)
	})
}

// With no cli_path set the program is the absolute path found on PATH, not a bare name looked up again by the agent's shell (#115).
func TestLaunchCLIUnsetResolvesToAnAbsolutePath(t *testing.T) {
	onPath := pathWithStub(t, "claude")
	got, err := LaunchCLI(map[string]string{}, "claude")
	require.NoError(t, err)
	assert.True(t, filepath.IsAbs(got), "got %q", got)
	assert.Equal(t, onPath, got)

	t.Setenv("PATH", t.TempDir()) // nothing on PATH: no error here (launch reports a missing CLI), and no invented path
	got, err = LaunchCLI(nil, "claude")
	require.NoError(t, err)
	assert.Empty(t, got)
}
