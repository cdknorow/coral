package agent

import (
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// #3692: a CORAL_SHELL naming cmd.exe is ignored for the launch line, which then matches the
// shell the terminal starts: PowerShell on Windows, the platform's own shell elsewhere (it was
// built for cmd.exe, which can't run the prompt argument or the long-command handoff).
func TestDetectShellIgnoresCmd(t *testing.T) {
	t.Setenv("SHELL", "/bin/zsh")
	for _, v := range []string{"cmd", "cmd.exe", `C:\Windows\System32\CMD.EXE`} {
		t.Setenv("CORAL_SHELL", v)
		want := ShellZsh
		if runtime.GOOS == "windows" {
			want = ShellPowerShell
		}
		assert.Equal(t, want, detectShell(), v)
		arg := FormatPromptFileArg("prompt.txt")
		assert.False(t, strings.Contains(arg, "%") || strings.Contains(arg, "type "), "no cmd.exe syntax: %s", arg)
	}
	t.Setenv("CORAL_SHELL", "pwsh")
	assert.Equal(t, ShellPowerShell, detectShell(), "PowerShell named outright is kept")
}
