//go:build windows

package agentsignin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The sign-in check runs Claude's shim from the project folder (#183): the child carries NoDefaultCurrentDirectoryInExePath=1, so cmd.exe does not search that folder for the
// programs the shim starts, and a repository's node.exe cannot run as Coral.
func TestTheSignInCheckRunsTheCLIWithoutTheCurrentFolderSearch(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	require.NoError(t, os.MkdirAll(home, 0o755))
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	for _, k := range credentialEnv {
		t.Setenv(k, "")
	}
	t.Setenv("NoDefaultCurrentDirectoryInExePath", "") // the test process may have it set; the check must set it itself
	os.Unsetenv("NoDefaultCurrentDirectoryInExePath")

	record := filepath.Join(root, "seen.txt")
	shim := filepath.Join(root, "claude.cmd")
	script := "@echo [%NoDefaultCurrentDirectoryInExePath%]> \"" + record + "\"\r\n@echo {\"loggedIn\":true}\r\n"
	require.NoError(t, os.WriteFile(shim, []byte(script), 0o755))
	project := filepath.Join(root, "project")
	require.NoError(t, os.MkdirAll(project, 0o755))

	CheckIn(context.Background(), "claude", shim, project)

	b, err := os.ReadFile(record)
	require.NoError(t, err, "the shim ran")
	assert.Equal(t, "[1]", strings.TrimSpace(string(b))) // (bracketed: "echo 1> file" would be a redirection of handle 1)
}
