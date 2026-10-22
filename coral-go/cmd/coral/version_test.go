package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3506: coral --version (and --help, a bad flag or a stray argument) answers and exits
// before anything a server does: no [STARTUP] line, nothing written to the data dir or
// home, no port. The child process runs main() with an empty data dir and home of its
// own, CORAL_PORT on a port just closed (never the operator's :8420), --no-browser in
// every case and a 20 s limit, so even a regression that starts a server can't touch the
// live Coral, open a browser or hang the test.
func TestVersionAndHelpExitBeforeTheServerStarts(t *testing.T) {
	if args := os.Getenv("CORAL_TEST_MAIN_ARGS"); args != "" {
		os.Args = append([]string{"coral"}, strings.Split(args, " ")...)
		main()
		return
	}
	for _, c := range []struct {
		args   string
		code   int
		stdout string
		stderr string
	}{
		{"--version --no-browser", 0, "coral ", ""},
		{"-v --no-browser", 0, "coral ", ""},
		{"version --no-browser", 0, "coral ", ""},
		{"--no-browser --version", 0, "coral ", ""}, // after another flag
		{"--no-browser -v", 0, "coral ", ""},
		{"-h --no-browser", 0, "Coral: ", ""}, // help is asked for: stdout, exit 0 (#235)
		{"help --no-browser", 0, "Coral: ", ""},
		{"--no-browser stop", 2, "", `unexpected argument "stop"`},
		{"--no-browser --no-such-flag", 2, "", "coral: unknown option \"--no-such-flag\""}, // coral's own line, not Go's "flag provided but not defined"
		{"--no-browser stray", 2, "", `unexpected argument "stray"`},                       // a positional word never starts a server
	} {
		dataDir, home := t.TempDir(), t.TempDir()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestVersionAndHelpExitBeforeTheServerStarts$")
		cmd.Env = append(cleanEnv(), "CORAL_TEST_MAIN_ARGS="+c.args, "CORAL_DATA_DIR="+dataDir, "HOME="+home,
			"CORAL_PORT="+strconv.Itoa(closedPort(t)), "CORAL_HOST=127.0.0.1")
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)
		cancel()
		require.False(t, timedOut, "%s: the child must exit at once, not run a server", c.args)
		code := 0
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code = exit.ExitCode()
		} else {
			require.NoError(t, err, c.args)
		}
		assert.Equal(t, c.code, code, "%s: %s", c.args, stderr.String())
		assert.True(t, strings.HasPrefix(stdout.String(), c.stdout), "%s stdout: %q", c.args, stdout.String())
		assert.Contains(t, stderr.String(), c.stderr, c.args)
		assert.NotContains(t, stderr.String()+stdout.String(), "[STARTUP]", "%s: no server startup line", c.args)
		for _, dir := range []string{dataDir, home} {
			entries, _ := os.ReadDir(dir)
			assert.Empty(t, entries, "%s wrote nothing in %s", c.args, dir)
		}
		if c.code == 0 && c.stdout == "coral " {
			assert.Equal(t, 1, strings.Count(stdout.String(), "\n"), "%s prints one line: %q", c.args, stdout.String())
		}
	}
}

// cleanEnv is this process's environment without Coral's own variables, so the child
// never points at a real Coral or Coral Hub.
func cleanEnv() []string {
	var out []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "CORAL_") || strings.HasPrefix(kv, "HOME=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// closedPort is a loopback port that was free a moment ago: nothing listens on it.
func closedPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	require.NoError(t, l.Close())
	return port
}
