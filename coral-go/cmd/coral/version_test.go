package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3506: coral --version (and --help, or a bad flag) answers and exits before anything a
// server does: no [STARTUP] line, nothing written to the data dir or home, no port. The
// child process runs main() with an empty data dir and home of its own.
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
		{"--version", 0, "coral ", ""},
		{"-v", 0, "coral ", ""},
		{"version", 0, "coral ", ""},
		{"-h", 0, "", "Usage"},
		{"--no-such-flag", 2, "", "flag provided but not defined"},
	} {
		dataDir, home := t.TempDir(), t.TempDir()
		cmd := exec.Command(os.Args[0], "-test.run=^TestVersionAndHelpExitBeforeTheServerStarts$")
		cmd.Env = append(cleanEnv(), "CORAL_TEST_MAIN_ARGS="+c.args, "CORAL_DATA_DIR="+dataDir, "HOME="+home)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
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
		if c.code == 0 && c.stdout != "" {
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
