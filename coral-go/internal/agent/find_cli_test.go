package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	at "github.com/cdknorow/coral/internal/agenttypes"
)

func writeFakeExe(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, []byte("#!/bin/sh\necho fake\n"), 0o755))
	return p
}

// FindCLI follows launch (#3169): the configured path first, then PATH, then
// the common install locations; a configured path that is not an executable is
// an error, never PATH's binary (#4432, Security's #4431 SHOULD 4); with none,
// an error that says where to set it.
func TestFindCLI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake binaries")
	}
	t.Setenv("HOME", t.TempDir()) // no ~/.local/bin or nvm installs from the real home
	onPath := t.TempDir()
	t.Setenv("PATH", onPath)
	configured := writeFakeExe(t, t.TempDir(), "my-claude")

	// The configured path is used although PATH has no claude at all (the
	// macOS app started from Finder).
	got, err := FindCLI(map[string]string{CLIPathSettingKey(at.Claude): configured}, at.Claude)
	require.NoError(t, err)
	assert.Equal(t, configured, got)

	// A configured path that does not exist, or is not executable, is an error naming it,
	// although PATH has the binary: the unwrapped CLI never runs in its place.
	inPath := writeFakeExe(t, onPath, "codex")
	notExec := filepath.Join(t.TempDir(), "codex-wrapper")
	require.NoError(t, os.WriteFile(notExec, []byte("#!/bin/sh\n"), 0o644))
	for _, broken := range []string{"/no/such/codex", notExec, t.TempDir()} {
		got, err = FindCLI(map[string]string{CLIPathSettingKey(at.Codex): broken}, at.Codex)
		require.Error(t, err, broken)
		assert.Empty(t, got)
		assert.Contains(t, err.Error(), broken)
		assert.Contains(t, err.Error(), "Settings > CLI paths")
	}
	// The agy and gemini pair share a path: a broken one set for gemini stops agy too.
	writeFakeExe(t, onPath, "agy")
	_, err = FindCLI(map[string]string{CLIPathSettingKey(at.Gemini): "/no/such/agy"}, at.Agy)
	require.Error(t, err)

	// No setting: PATH.
	got, err = FindCLI(nil, at.Codex)
	require.NoError(t, err)
	assert.Equal(t, inPath, got)

	// Nowhere: an error naming the setting.
	_, err = FindCLI(nil, "zz-not-a-real-agent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Settings > CLI paths")
}
