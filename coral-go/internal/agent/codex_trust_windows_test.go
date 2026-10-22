//go:build windows

package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

// #153: the real check. A folder this user can change (a temp folder, as a portable or dev build's tools folder is) is
// refused; an installed Coral's tools folder under Program Files, which only an administrator can change, passes. That
// one is only read (its access list), and skipped where Coral isn't installed.
func TestCodexHooksFolderSafeOnWindows(t *testing.T) {
	dir := t.TempDir()
	tool := filepath.Join(dir, "coral-hook-agentic-state.exe")
	if err := os.WriteFile(tool, nil, 0o700); err != nil {
		t.Fatal(err)
	}
	ok, why := codexHooksFolderSafe(tool)
	assert.False(t, ok, "a folder this user can change")
	assert.Contains(t, why, "this user")

	installed := filepath.Join(os.Getenv("ProgramFiles(x86)"), "Coral", "coral-hook-agentic-state.exe")
	if _, err := os.Stat(installed); err != nil {
		t.Skip("no installed Coral here to read")
	}
	ok, why = codexHooksFolderSafe(installed)
	assert.True(t, ok, "the installed Coral's tools folder: %s", why)
}
