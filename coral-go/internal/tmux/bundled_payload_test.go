package tmux

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBundledPayloadSmoke exercises the Go tmux client against a REAL bundled
// tmux payload copied into a temporary app-like layout (with a space in its
// path), with no Homebrew on PATH and its own short socket and home. It never
// starts an agent, sends a prompt, or touches the normal server or ~/.coral.
//
// It runs only when pointed at a payload:
//
//	CORAL_BUNDLED_TMUX_BIN=/path/to/Coral.app/Contents/MacOS/tmux
//	CORAL_BUNDLED_TMUX_TERMINFO=/path/to/Coral.app/Contents/Resources/terminfo
func TestBundledPayloadSmoke(t *testing.T) {
	srcBin := os.Getenv("CORAL_BUNDLED_TMUX_BIN")
	srcTerminfo := os.Getenv("CORAL_BUNDLED_TMUX_TERMINFO")
	if srcBin == "" || srcTerminfo == "" {
		t.Skip("set CORAL_BUNDLED_TMUX_BIN and CORAL_BUNDLED_TMUX_TERMINFO to run against a real payload")
	}
	ctx := context.Background()

	// App-like layout in a private temp dir, with spaces in the app name. The
	// payload is COPIED (not symlinked), so this also proves it is relocatable.
	root, err := os.MkdirTemp("", "coral-payload-")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(root) })
	root, _ = filepath.EvalSymlinks(root)
	macos := filepath.Join(root, "Coral Smoke.app", "Contents", "MacOS")
	resources := filepath.Join(root, "Coral Smoke.app", "Contents", "Resources")
	require.NoError(t, os.MkdirAll(macos, 0o755))
	require.NoError(t, os.MkdirAll(resources, 0o755))
	bundled := filepath.Join(macos, "tmux")
	require.NoError(t, exec.Command("cp", "-p", srcBin, bundled).Run())
	require.NoError(t, exec.Command("cp", "-R", srcTerminfo, filepath.Join(resources, "terminfo")).Run())
	coral := filepath.Join(macos, "coral")
	helper := filepath.Join(macos, "coral-board")
	for _, p := range []string{coral, helper} {
		require.NoError(t, os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755))
	}

	// Isolated environment: system-only PATH (no Homebrew), own HOME, no
	// override, discovery seams pointed at the temp layout.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("CORAL_TMUX_BIN", "")
	t.Setenv("TERMINFO", "")
	userTerminfo := filepath.Join(home, "user-terminfo")
	t.Setenv("TERMINFO_DIRS", userTerminfo+":/usr/share/terminfo")
	prevCommon, prevGOOS, prevExe := commonTmuxPaths, goos, executableFn
	commonTmuxPaths, goos = nil, "darwin"
	executableFn = func() (string, error) { return coral, nil }
	t.Cleanup(func() { commonTmuxPaths, goos, executableFn = prevCommon, prevGOOS, prevExe })

	// 1. Discovery finds the bundled binary, not a system one.
	found, ok := IsAvailable()
	require.True(t, ok)
	require.Equal(t, bundled, found)

	// Short socket directory (the unix socket path limit is small).
	sockDir, err := os.MkdirTemp("", "cbt-")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	c := NewClient(sockDir)
	require.Equal(t, bundled, c.TmuxBin)
	t.Cleanup(func() { _, _ = c.run(ctx, "kill-server") })

	// 2. Create a session running a harmless program (no agent, no prompt).
	const name = "claude-5b8f1c2e-9d3a-4e7b-8c11-0a2b3c4d5e6f"
	_, err = c.run(ctx, "new-session", "-d", "-s", name, "-x", "120", "-y", "40", `sh -c 'stty raw -echo; exec cat -v'`)
	require.NoError(t, err, "the bundled tmux starts a server through the client")
	time.Sleep(300 * time.Millisecond)
	require.True(t, c.HasSession(ctx, name))

	// 3. List.
	panes, err := c.ListPanes(ctx)
	require.NoError(t, err)
	require.Len(t, panes, 1)
	target := panes[0].Target
	assert.Contains(t, panes[0].SessionName, "claude-")

	// 4. Send text and capture it back.
	require.NoError(t, c.SendKeysToTarget(ctx, target, "payload smoke ok"))
	time.Sleep(300 * time.Millisecond)
	captured, err := c.CapturePaneTarget(ctx, target, 50)
	require.NoError(t, err)
	assert.Contains(t, captured, "payload smoke ok")

	// 5. Resize.
	require.NoError(t, c.ResizePaneTarget(ctx, target, 90, 30))
	width, err := c.run(ctx, "display-message", "-p", "-t", target, "#{window_width}")
	require.NoError(t, err)
	assert.Equal(t, "90", strings.TrimSpace(width))

	// 6. A new client on the same per-instance socket still sees the session.
	restarted := NewClient(sockDir)
	require.Equal(t, bundled, restarted.TmuxBin)
	assert.True(t, restarted.HasSession(ctx, name), "the session survives the Coral client being replaced")

	// 7. Helper discovery: a helper in the same bundle (no CORAL_TMUX_BIN, no
	// Homebrew) finds the bundled tmux and can talk to the running server.
	executableFn = func() (string, error) { return helper, nil }
	helperBin := BinaryFromEnv()
	assert.Equal(t, bundled, helperBin)
	out, err := exec.Command(helperBin, "-S", c.SocketPath, "list-sessions", "-F", "#{session_name}").Output()
	require.NoError(t, err)
	assert.Equal(t, name, strings.TrimSpace(string(out)))

	// 8. The attach command parses, names the bundled binary, preserves the
	// user's TERMINFO_DIRS and adds the bundle's; run it with attach swapped
	// for a non-interactive command to prove it executes under a shell.
	attach := c.AttachCommand(name)
	require.Contains(t, attach, userTerminfo+":/usr/share/terminfo:", "the user's list comes first and is preserved")
	require.Contains(t, attach, filepath.Join(root, "Coral Smoke.app", "Contents", "Resources", "terminfo"))
	require.Contains(t, attach, "'"+bundled+"'", "the quoted bundled binary")
	require.NoError(t, exec.Command("/bin/sh", "-n", "-c", attach).Run(), "the exact attach command parses")
	probe := strings.Replace(attach, "attach -t "+shellQuoteOrPlain(name), "list-sessions -F '#{session_name}'", 1)
	got, err := exec.Command("/bin/sh", "-c", probe).Output()
	require.NoError(t, err, "the command executes under /bin/sh with its quoting and env prefix")
	assert.Equal(t, name, strings.TrimSpace(string(got)))
}

func shellQuoteOrPlain(v string) string { return quoteIfNeeded(v) }
