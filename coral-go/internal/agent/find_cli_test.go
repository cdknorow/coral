package agent

import (
	"bytes"
	"log/slog"
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
// the common install locations; a configured path that is not an executable
// falls back to the others; with none, an error that says where to set it.
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

	// A configured path that does not exist falls back to PATH, with a warning
	// naming it (launch would report it, so the difference is visible).
	inPath := writeFakeExe(t, onPath, "codex")
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	got, err = FindCLI(map[string]string{CLIPathSettingKey(at.Codex): "/no/such/codex"}, at.Codex)
	slog.SetDefault(prev)
	require.NoError(t, err)
	assert.Equal(t, inPath, got)
	assert.Contains(t, logs.String(), "configured=/no/such/codex", "the fallback is logged with the configured path")

	// No setting: PATH.
	got, err = FindCLI(nil, at.Codex)
	require.NoError(t, err)
	assert.Equal(t, inPath, got)

	// Nowhere: an error naming the setting.
	_, err = FindCLI(nil, "zz-not-a-real-agent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Settings > CLI Paths")
}
