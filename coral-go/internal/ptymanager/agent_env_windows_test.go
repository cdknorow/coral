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

// #25 G1B: every agent's environment says not to look in the current folder for programs.
func TestSessionEnvStopsCurrentFolderExeLookup(t *testing.T) {
	t.Setenv("NoDefaultCurrentDirectoryInExePath", "") // the server's own setting does not matter
	assert.Contains(t, sessionEnv("claude-x"), "NoDefaultCurrentDirectoryInExePath=1")
}

func TestSessionEnvHasTheVariableOnce(t *testing.T) {
	t.Setenv("NoDefaultCurrentDirectoryInExePath", "")
	n := 0
	for _, kv := range sessionEnv("claude-x") {
		if len(kv) > 34 && kv[:34] == "NoDefaultCurrentDirectoryInExePath" {
			n++
		}
	}
	assert.Equal(t, 1, n)
}

// #25 G1B through the real ConPTY: the variable reaches the process the session starts. It needs #19 (ConPTY gets the
// session's environment), so it fails on a tree without it: the dependency is enforced, not remembered.
func TestWindowsAgentGetsNoDefaultCurrentDirectoryInExePath(t *testing.T) {
	naming.SetDefaultLogDir(t.TempDir())
	t.Cleanup(func() { naming.SetDefaultLogDir("") })
	t.Setenv(envEchoEnv, "1")
	pkg, err := os.Getwd()
	require.NoError(t, err)
	s, err := newSession("env-g1b", "test", pkg, "sid-env-g1b", os.Args[0], 1000, 50)
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
	assert.Contains(t, out, "ENV:NoDefaultCurrentDirectoryInExePath=1")
}
