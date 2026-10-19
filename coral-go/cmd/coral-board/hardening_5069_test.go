package main

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

// A session name from the environment never names a file outside the data folder (#5069): the state file is written and removed there.
func TestStateFileStaysInTheDataFolder(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CORAL_DATA_DIR", dir)
	t.Setenv("CORAL_DIR", "")
	for _, name := range []string{"../../x", "..\\..\\x", "a/b", "/etc/passwd"} {
		t.Setenv("CORAL_SESSION_NAME", name)
		assert.Equal(t, coralDir(), filepath.Dir(stateFilePath()), "the state file for %q", name)
	}
}

// A credential embedded in CORAL_URL is never in a message the tool prints.
func TestRedactedServerURLHidesUserinfo(t *testing.T) {
	old := serverURL
	defer func() { serverURL = old }()
	serverURL = "http://bob:hunter2@10.0.0.2:8420"
	assert.NotContains(t, redactedServerURL(), "hunter2")
	assert.NotContains(t, redactedServerURL(), "bob")
}
