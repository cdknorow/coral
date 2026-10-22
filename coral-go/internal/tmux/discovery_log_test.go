package tmux

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #6004 W9: the tmux discovery lines were logged on every look (the status poll made one every 30 seconds) and carried the whole PATH, with the person's profile folders. A line is now logged
// once and again only when it changes, and none of them carries the PATH.

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	discoveryMu.Lock()
	lastDiscovery = ""
	discoveryMu.Unlock()
	t.Cleanup(func() {
		log.SetOutput(old)
		discoveryMu.Lock()
		lastDiscovery = ""
		discoveryMu.Unlock()
	})
	return &buf
}

func discoveryLines(buf *bytes.Buffer) []string {
	var out []string
	for _, l := range strings.Split(buf.String(), "\n") {
		if strings.Contains(l, "[tmux] discovery") {
			out = append(out, l)
		}
	}
	return out
}

func TestTheSameDiscoveryAnswerIsLoggedOnceNotOnEveryLook(t *testing.T) {
	buf := captureLog(t)
	t.Setenv("PATH", filepath.Join(t.TempDir(), "profile-folder-marker"))
	t.Setenv("CORAL_TMUX_BIN", "")
	for i := 0; i < 6; i++ {
		IsAvailable() // whatever this machine answers, it answers the same every time
	}
	lines := discoveryLines(buf)
	assert.Len(t, lines, 1, "six looks, one line: %v", lines)
}

func TestADifferentAnswerIsLoggedAgainAndNoLineCarriesThePath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake tmux is a shell script")
	}
	buf := captureLog(t)
	marker := filepath.Join(t.TempDir(), "profile-folder-marker")
	t.Setenv("PATH", marker)
	t.Setenv("SHELL", "/bin/zsh")
	t.Setenv("CORAL_TMUX_BIN", "")
	IsAvailable()
	IsAvailable()
	first := len(discoveryLines(buf))
	require.Equal(t, 1, first)

	fake := filepath.Join(t.TempDir(), "tmux")
	require.NoError(t, os.WriteFile(fake, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	t.Setenv("CORAL_TMUX_BIN", fake)
	for i := 0; i < 3; i++ {
		p, ok := IsAvailable()
		require.True(t, ok)
		assert.Equal(t, fake, p)
	}
	assert.Len(t, discoveryLines(buf), 2, "found where it was not found: one more line, then quiet")

	t.Setenv("CORAL_TMUX_BIN", "")
	IsAvailable()
	IsAvailable()
	assert.Len(t, discoveryLines(buf), 3, "and back to the first answer: one more")

	assert.NotContains(t, buf.String(), "profile-folder-marker", "no line carries the PATH")
	assert.NotContains(t, buf.String(), "path=", "nor a path= field for it")
}

func TestTheShellIsNamedByItsFileNameNotItsFolder(t *testing.T) {
	t.Setenv("SHELL", filepath.Join(string(filepath.Separator), "home", "someone", "bin", "fish"))
	assert.Equal(t, "fish", shellName())
}

// The failure line is the one that carried the PATH: make this machine find no tmux at all (no PATH entry, no common location, no login shell answer) and read it.
func TestTheFailureLineCarriesNeitherThePathNorTheShellsFolder(t *testing.T) {
	buf := captureLog(t)
	oldCommon, oldLogin := commonTmuxPaths, loginShellTmux
	commonTmuxPaths, loginShellTmux = nil, func() string { return "" }
	t.Cleanup(func() { commonTmuxPaths, loginShellTmux = oldCommon, oldLogin })
	t.Setenv("PATH", filepath.Join(t.TempDir(), "profile-folder-marker"))
	t.Setenv("SHELL", filepath.Join(string(filepath.Separator), "home", "someone", "bin", "fish"))
	t.Setenv("CORAL_TMUX_BIN", "")
	_, ok := IsAvailable()
	require.False(t, ok)
	lines := discoveryLines(buf)
	require.Len(t, lines, 1)
	assert.Contains(t, lines[0], "discovery failed")
	assert.Contains(t, lines[0], `shell="fish"`)
	assert.NotContains(t, buf.String(), "profile-folder-marker")
	assert.NotContains(t, buf.String(), "someone")
	assert.Contains(t, lines[0], "coral_tmux_bin_set=false")
}
