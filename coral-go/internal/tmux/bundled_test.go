package tmux

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeBundle builds <root>/Coral.app/Contents/{MacOS/{coral,tmux},Resources/terminfo}
// and points the executable seam at its coral. It never starts a real program.
type fakeBundle struct {
	root, macos, tmuxBin, terminfo, coral string
}

func newFakeBundle(t *testing.T, tmuxScript string) *fakeBundle {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir()) // macOS temp dirs sit behind /var -> /private/var
	require.NoError(t, err)
	b := &fakeBundle{root: root}
	b.macos = filepath.Join(root, "Coral Test.app", "Contents", "MacOS") // a space: quoting must hold
	b.terminfo = filepath.Join(root, "Coral Test.app", "Contents", "Resources", "terminfo")
	require.NoError(t, os.MkdirAll(b.macos, 0o755))
	require.NoError(t, os.MkdirAll(b.terminfo, 0o755))
	b.coral = filepath.Join(b.macos, "coral")
	b.tmuxBin = filepath.Join(b.macos, "tmux")
	require.NoError(t, os.WriteFile(b.coral, []byte("#!/bin/sh\n"), 0o755))
	require.NoError(t, os.WriteFile(b.tmuxBin, []byte("#!/bin/sh\n"+tmuxScript+"\n"), 0o755))

	prevGOOS, prevExe := goos, executableFn
	goos = "darwin"
	executableFn = func() (string, error) { return b.coral, nil }
	t.Cleanup(func() { goos, executableFn = prevGOOS, prevExe })
	t.Setenv("CORAL_TMUX_BIN", "")
	t.Setenv("TERMINFO_DIRS", "")
	return b
}

func realPath(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	require.NoError(t, err)
	return r
}

func TestDiscoveryOrderOverrideThenBundledThenSystem(t *testing.T) {
	b := newFakeBundle(t, "exit 0")
	// A system tmux exists on PATH too.
	sysDir := t.TempDir()
	sysTmux := filepath.Join(sysDir, "tmux")
	require.NoError(t, os.WriteFile(sysTmux, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	t.Setenv("PATH", sysDir)

	// Bundled beats PATH.
	got, ok := IsAvailable()
	require.True(t, ok)
	assert.Equal(t, realPath(t, b.tmuxBin), realPath(t, got))

	// An explicit CORAL_TMUX_BIN override beats the bundle.
	override := filepath.Join(t.TempDir(), "my-tmux")
	require.NoError(t, os.WriteFile(override, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	t.Setenv("CORAL_TMUX_BIN", override)
	got, ok = IsAvailable()
	require.True(t, ok)
	assert.Equal(t, override, got)

	// An override that is not executable is ignored (logged), falling through to the bundle.
	t.Setenv("CORAL_TMUX_BIN", filepath.Join(t.TempDir(), "missing"))
	got, _ = IsAvailable()
	assert.Equal(t, realPath(t, b.tmuxBin), realPath(t, got))

	// Not Darwin: the bundle is not consulted; the system tmux is used.
	t.Setenv("CORAL_TMUX_BIN", "")
	goos = "linux"
	got, ok = IsAvailable()
	require.True(t, ok)
	assert.Equal(t, sysTmux, got)
	goos = "darwin"

	// No bundled tmux in the app: fall back to the system one.
	require.NoError(t, os.Remove(b.tmuxBin))
	got, _ = IsAvailable()
	assert.Equal(t, sysTmux, got)
}

func TestBundledTmuxFoundThroughSymlinkedExecutableAndSymlinkedTmux(t *testing.T) {
	b := newFakeBundle(t, "exit 0")
	link := filepath.Join(t.TempDir(), "coral") // like /usr/local/bin/coral -> the bundle
	require.NoError(t, os.Symlink(b.coral, link))
	executableFn = func() (string, error) { return link, nil }
	bin, terminfo := bundledTmux()
	assert.Equal(t, realPath(t, b.tmuxBin), realPath(t, bin))
	assert.Equal(t, realPath(t, b.terminfo), realPath(t, terminfo))

	// The bundled tmux itself being a symlink resolves to the real file.
	real := filepath.Join(t.TempDir(), "tmux-real")
	require.NoError(t, os.WriteFile(real, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	require.NoError(t, os.Remove(b.tmuxBin))
	require.NoError(t, os.Symlink(real, b.tmuxBin))
	bin, _ = bundledTmux()
	assert.Equal(t, realPath(t, real), realPath(t, bin))

	// Without a terminfo directory the bundled tmux is still used, with no terminfo env.
	require.NoError(t, os.RemoveAll(b.terminfo))
	_, terminfo = bundledTmux()
	assert.Empty(t, terminfo)
}

func envMap(env []string) map[string]string {
	m := map[string]string{}
	for _, kv := range env {
		if i := strings.IndexByte(kv, '='); i > 0 {
			m[kv[:i]] = kv[i+1:]
		}
	}
	return m
}

func TestTmuxEnvOnlyTouchesTheBundledTmuxAndKeepsUserSettings(t *testing.T) {
	b := newFakeBundle(t, "exit 0")
	assert.Nil(t, tmuxEnv("/opt/homebrew/bin/tmux"), "a system tmux inherits our environment unchanged")
	assert.Nil(t, tmuxEnv("tmux"))

	env := envMap(tmuxEnv(b.tmuxBin))
	assert.Equal(t, filepath.Clean(b.terminfo), env["TERMINFO_DIRS"])
	assert.Equal(t, b.tmuxBin, env["CORAL_TMUX_BIN"])

	// The user's own settings stay first and are never replaced.
	t.Setenv("TERMINFO", "/user/terminfo")
	t.Setenv("TERMINFO_DIRS", "/user/a:/user/b")
	env = envMap(tmuxEnv(b.tmuxBin))
	assert.Equal(t, "/user/a:/user/b:"+filepath.Clean(b.terminfo), env["TERMINFO_DIRS"])
	assert.Equal(t, "/user/terminfo", env["TERMINFO"])

	// Already listed: not duplicated. An existing CORAL_TMUX_BIN is not overwritten.
	t.Setenv("TERMINFO_DIRS", filepath.Clean(b.terminfo))
	t.Setenv("CORAL_TMUX_BIN", "/explicit/tmux")
	env = envMap(tmuxEnv(b.tmuxBin))
	assert.Equal(t, filepath.Clean(b.terminfo), env["TERMINFO_DIRS"])
	assert.Equal(t, "/explicit/tmux", env["CORAL_TMUX_BIN"])
}

// Every tmux operation runs the selected binary with the bundled environment.
func TestRealOperationsUseTheSelectedBundledBinaryAndEnvironment(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "calls.log")
	b := newFakeBundle(t, `echo "$0|$1 $2 $3|TD=$TERMINFO_DIRS|BIN=$CORAL_TMUX_BIN" >> `+logFile+`
case "$*" in
  *list-panes*) echo "claude-abc|claude-abc:0.0|/tmp|title" ;;
esac
exit 0`)
	c := NewClient(t.TempDir())
	require.Equal(t, realPath(t, b.tmuxBin), realPath(t, c.TmuxBin), "NewClient selects the bundled tmux")

	ctx := context.Background()
	panes, err := c.ListPanes(ctx)
	require.NoError(t, err)
	require.Len(t, panes, 1)
	_, err = c.run(ctx, "has-session", "-t", "x")
	require.NoError(t, err)
	_, err = c.loadPromptBuffer(ctx, "hello")
	require.NoError(t, err)

	data, err := os.ReadFile(logFile)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	require.GreaterOrEqual(t, len(lines), 3)
	for _, line := range lines {
		parts := strings.Split(line, "|")
		assert.Equal(t, realPath(t, b.tmuxBin), realPath(t, parts[0]), "the bundled binary ran: %s", line)
		assert.Equal(t, "TD="+filepath.Clean(b.terminfo), parts[2], line)
		assert.Equal(t, "BIN="+c.TmuxBin, parts[3], line)
	}
}

func TestAttachCommandNamesTheSelectedBinaryWithTerminfo(t *testing.T) {
	b := newFakeBundle(t, "exit 0")
	c := &Client{TmuxBin: b.tmuxBin, SocketPath: "/tmp/coral.sock"}
	cmd := c.AttachCommand("claude-abc")
	assert.Contains(t, cmd, "TERMINFO_DIRS='"+filepath.Clean(b.terminfo)+"'", "terminfo prefix is quoted (path has a space)")
	assert.Contains(t, cmd, "'"+b.tmuxBin+"' -S /tmp/coral.sock attach -t claude-abc")

	// A system tmux off PATH is named by its path; one on PATH stays bare "tmux".
	sysDir := t.TempDir()
	sys := filepath.Join(sysDir, "tmux")
	require.NoError(t, os.WriteFile(sys, []byte("#!/bin/sh\n"), 0o755))
	c = &Client{TmuxBin: sys, SocketPath: "/tmp/s"}
	assert.Equal(t, sys+" -S /tmp/s attach -t n", c.AttachCommand("n"))
	t.Setenv("PATH", sysDir)
	assert.Equal(t, "tmux -S /tmp/s attach -t n", c.AttachCommand("n"))
}

func TestBinaryFromEnvForHelpersInsidePanes(t *testing.T) {
	t.Setenv("CORAL_TMUX_BIN", "")
	t.Setenv("PATH", t.TempDir()) // no tmux anywhere on PATH
	prev, prevGOOS := commonTmuxPaths, goos
	commonTmuxPaths, goos = nil, "linux" // none in the common install locations, no bundle
	t.Cleanup(func() { commonTmuxPaths, goos = prev, prevGOOS })
	assert.Equal(t, "tmux", BinaryFromEnv())
	good := filepath.Join(t.TempDir(), "tmux")
	require.NoError(t, os.WriteFile(good, []byte("#!/bin/sh\n"), 0o755))
	t.Setenv("CORAL_TMUX_BIN", good)
	assert.Equal(t, good, BinaryFromEnv())
	t.Setenv("CORAL_TMUX_BIN", filepath.Join(t.TempDir(), "nope"))
	assert.Equal(t, "tmux", BinaryFromEnv(), "an unusable value falls back")
}

// Audit guard: no product code may run a literal "tmux" subprocess; they must
// use the selected binary (the client, or BinaryFromEnv inside panes).
func TestNoLiteralTmuxSubprocessesRemain(t *testing.T) {
	re := regexp.MustCompile(`(?:exec\.Command|exec\.CommandContext|exec)\(\s*(?:ctx\s*,\s*)?"tmux"`)
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	var offenders []string
	for _, dir := range []string{"cmd", "internal"} {
		_ = filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			data, _ := os.ReadFile(path)
			if re.Match(data) {
				offenders = append(offenders, strings.TrimPrefix(path, root))
			}
			return nil
		})
	}
	assert.Empty(t, offenders, "literal tmux subprocesses bypass the bundled/overridden tmux")
}

// A running server from a different tmux version must be surfaced, never killed.
func TestProtocolMismatchIsSurfacedAndTheOldServerIsNeverKilled(t *testing.T) {
	resetIncompat()
	t.Cleanup(resetIncompat)
	logFile := filepath.Join(t.TempDir(), "calls.log")
	b := newFakeBundle(t, `echo "$*" >> `+logFile+`
echo "protocol version mismatch (client 8, server 7)" >&2
exit 1`)
	c := NewClient(t.TempDir())
	panes, err := c.ListPanes(context.Background())
	require.NoError(t, err)
	assert.Empty(t, panes)
	_, err = c.run(context.Background(), "has-session", "-t", "x")
	assert.Error(t, err)

	msg, ok := IncompatibleServer()
	require.True(t, ok)
	assert.Contains(t, msg, "different tmux version")
	assert.Contains(t, msg, "CORAL_TMUX_BIN")
	assert.Contains(t, msg, "will not stop it")
	assert.Contains(t, msg, "Restarting Coral alone does not")
	assert.Contains(t, msg, "tmux -S <socket> kill-server")
	assert.Contains(t, msg, c.SocketPath)
	assert.Contains(t, msg, b.tmuxBin)

	data, _ := os.ReadFile(logFile)
	assert.NotContains(t, string(data), "kill", "Coral must not kill the old server")
}

// Real tmux on a private socket: a session created through the selected
// (bundled-layout) binary survives the Coral client being replaced.
func TestSessionSurvivesClientRestartThroughBundledTmux(t *testing.T) {
	realTmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not available")
	}
	b := newFakeBundle(t, "exit 0")
	require.NoError(t, os.Remove(b.tmuxBin))
	require.NoError(t, os.Symlink(realTmux, b.tmuxBin)) // the "bundled" tmux is the real one

	dir, err := os.MkdirTemp("", "coral-bt-")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	ctx := context.Background()

	first := NewClient(dir)
	require.Equal(t, realPath(t, b.tmuxBin), realPath(t, first.TmuxBin))
	if out, err := exec.Command(first.TmuxBin, "-S", first.SocketPath, "new-session", "-d", "-s", "smoke", "sleep 30").CombinedOutput(); err != nil {
		t.Skipf("tmux cannot create sessions here: %v %s", err, out)
	}
	t.Cleanup(func() { exec.Command(first.TmuxBin, "-S", first.SocketPath, "kill-server").Run() })
	time.Sleep(200 * time.Millisecond)
	require.True(t, first.HasSession(ctx, "smoke"))

	restarted := NewClient(dir) // a fresh client on the same per-instance socket
	assert.Equal(t, first.SocketPath, restarted.SocketPath)
	assert.True(t, restarted.HasSession(ctx, "smoke"), "the session is still there after the client restarted")
	assert.Contains(t, restarted.AttachCommand("smoke"), "attach -t smoke")
}

// A helper inside the app bundle, talking to a pre-existing server that never
// had CORAL_TMUX_BIN injected, still finds the bundled tmux.
func TestBinaryFromEnvFallsBackToTheBundleThenPath(t *testing.T) {
	b := newFakeBundle(t, "exit 0")
	assert.Equal(t, realPath(t, b.tmuxBin), realPath(t, BinaryFromEnv()))
	goos = "linux"
	sysDir := t.TempDir()
	sys := filepath.Join(sysDir, "tmux")
	require.NoError(t, os.WriteFile(sys, []byte("#!/bin/sh\n"), 0o755))
	t.Setenv("PATH", sysDir)
	assert.Equal(t, sys, BinaryFromEnv(), "no bundle: tmux on PATH")
}

func TestAttachCommandQuotesEverythingAndKeepsTheUsersTerminfoList(t *testing.T) {
	b := newFakeBundle(t, "exit 0")
	t.Setenv("TERMINFO_DIRS", "/user/a:"+b.terminfo+":/user/b") // bundle already listed
	c := &Client{TmuxBin: b.tmuxBin, SocketPath: "/tmp/my dir/coral.sock"}
	cmd := c.AttachCommand("name with space")
	assert.Contains(t, cmd, "TERMINFO_DIRS='/user/a:"+b.terminfo+":/user/b'", "the whole user list is kept")
	assert.Contains(t, cmd, "-S '/tmp/my dir/coral.sock' attach -t 'name with space'")
}

func resetIncompat() {
	incompatMu.Lock()
	incompat = map[string]string{}
	incompatMu.Unlock()
}

// Both tmux phrasings are detected, the state clears once the same socket
// works again, and a success on a different socket does not clear it.
func TestVersionProblemIsPerSocketCoversBothMessagesAndClearsOnSuccess(t *testing.T) {
	resetIncompat()
	t.Cleanup(resetIncompat)
	state := filepath.Join(t.TempDir(), "mode")
	b := newFakeBundle(t, `mode=$(cat `+state+` 2>/dev/null)
case "$mode" in
  old) echo "protocol version mismatch (client 8, server 7)" >&2; exit 1 ;;
  new) echo "server version is too old for client" >&2; exit 1 ;;
esac
exit 0`)
	_ = b
	dir := t.TempDir()
	c := NewClient(dir)
	ctx := context.Background()
	setMode := func(m string) { require.NoError(t, os.WriteFile(state, []byte(m), 0o600)) }

	for _, mode := range []string{"old", "new"} {
		resetIncompat()
		setMode(mode)
		_, err := c.run(ctx, "has-session", "-t", "x")
		require.Error(t, err)
		msg, ok := IncompatibleServer()
		require.True(t, ok, mode)
		assert.Contains(t, msg, c.SocketPath, mode)
	}

	// A success on another socket leaves it in place.
	setMode("ok")
	_, err := c.runOnSocket(ctx, filepath.Join(dir, "other.sock"), "has-session", "-t", "x")
	require.NoError(t, err)
	_, still := IncompatibleServer()
	assert.True(t, still, "another socket's success must not clear this one")

	// Recovery on the same socket clears it, so the status does not go stale.
	_, err = c.run(ctx, "has-session", "-t", "x")
	require.NoError(t, err)
	_, still = IncompatibleServer()
	assert.False(t, still, "cleared after the socket works again")
}

func TestQuoteIfNeededQuotesEverythingThatIsNotAPlainWord(t *testing.T) {
	for in, want := range map[string]string{
		"tmux":             "tmux",
		"/tmp/s.sock":      "/tmp/s.sock",
		"claude-abc_1:0.0": "claude-abc_1:0.0",
		"a b":              "'a b'",
		"it's":             `'it'\''s'`,
		"line1\nline2":     "'line1\nline2'",
		"cr\rx":            "'cr\rx'",
		"$(rm -rf x)":      "'$(rm -rf x)'",
		"a;b":              "'a;b'",
		"":                 "''",
		"~/x":              "'~/x'",
	} {
		assert.Equal(t, want, quoteIfNeeded(in), "%q", in)
	}
}
