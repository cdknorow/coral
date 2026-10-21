package ptymanager

import (
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
	require.Len(t, sh, 3)
	assert.Contains(t, []string{"pwsh.exe", "powershell.exe"}, sh[0])
}
