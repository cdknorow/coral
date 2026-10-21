package ptymanager

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3692, on Windows itself: with CORAL_SHELL naming cmd.exe, an agent's terminal still starts a
// PowerShell (the runners have one).
func TestDefaultShellOnWindowsIsPowerShell(t *testing.T) {
	t.Setenv("CORAL_SHELL", `C:\Windows\System32\cmd.exe`)
	sh, err := defaultShell()
	require.NoError(t, err)
	require.Len(t, sh, 1)
	assert.Contains(t, []string{"pwsh.exe", "powershell.exe"}, strings.ToLower(filepath.Base(sh[0])))
	assert.True(t, filepath.IsAbs(sh[0]), "by its absolute path (#26 G1A): %s", sh[0])
}
