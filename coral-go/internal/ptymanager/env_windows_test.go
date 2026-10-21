//go:build windows

package ptymanager

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/naming"
)

// #19 (QA's #18): a session started through ConPTY gets newSession's environment, Coral's
// filtered one (#3389), not the server's whole environment. The agent is this test binary,
// printing its environment.
func TestWindowsAgentGetsTheFilteredEnvironment(t *testing.T) {
	naming.SetDefaultLogDir(t.TempDir())
	t.Cleanup(func() { naming.SetDefaultLogDir("") })
	t.Setenv(envEchoEnv, "1")
	t.Setenv("CORAL_SECRET_TEST", "leak")  // a server setting: never the agent's
	t.Setenv("CORAL_SECRET_KEY", "sealed") // the Coral Hub token's key
	t.Setenv("TERM", "dumb")               // the server's own: the session's replaces it
	t.Setenv("CORAL_DATA_DIR", `C:\coral-test-19`)

	pkg, err := os.Getwd() // the package's folder: the test binary's init reads testdata/
	require.NoError(t, err)
	s, err := newSession("env-19", "test", pkg, "sid-env-19", os.Args[0], 1000, 50)
	require.NoError(t, err)
	defer s.kill()

	var out string
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		out = string(s.replayBytes())
		if strings.Contains(out, "ENV-END") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	require.Contains(t, out, "ENV-END", "the agent printed its environment: %q", out)

	assert.NotContains(t, out, "CORAL_SECRET_TEST", "a server setting reached the agent")
	assert.NotContains(t, out, "CORAL_SECRET_KEY")
	assert.NotContains(t, out, "TERM=dumb")
	for _, want := range []string{"ENV:TERM=xterm-256color", "ENV:COLORTERM=truecolor", "ENV:CORAL_SESSION_NAME=env-19", `ENV:CORAL_DATA_DIR=C:\coral-test-19`} {
		assert.Contains(t, out, want)
	}
	// What Windows itself needs is still there.
	for _, name := range []string{"SystemRoot", "windir", "ComSpec", "PATHEXT", "TEMP", "USERPROFILE", "Path"} {
		if v := os.Getenv(name); v != "" {
			assert.Contains(t, strings.ToUpper(out), "ENV:"+strings.ToUpper(name)+"=", name)
		}
	}
}
