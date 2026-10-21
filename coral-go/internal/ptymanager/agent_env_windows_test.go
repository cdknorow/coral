//go:build windows

package ptymanager

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
