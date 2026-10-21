package winshell

import (
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPick(t *testing.T) {
	look := func(have ...string) func(string) (string, error) {
		return func(name string) (string, error) {
			for _, h := range have {
				if h == name {
					return `C:\Tools\` + name, nil
				}
			}
			return "", exec.ErrNotFound
		}
	}
	assert.Equal(t, "pwsh.exe", Pick("", look("pwsh.exe", "powershell.exe")))
	assert.Equal(t, "powershell.exe", Pick("", look("powershell.exe")))
	assert.Equal(t, "", Pick("", look()))
	assert.Equal(t, `C:\Program Files\PowerShell\7\pwsh.exe`, Pick(`C:\Program Files\PowerShell\7\pwsh.exe`, look()))
	assert.Equal(t, "POWERSHELL", Pick("POWERSHELL", look()), "CORAL_SHELL naming PowerShell, as given")
	assert.Equal(t, "powershell.exe", Pick("cmd.exe", look("powershell.exe")), "cmd.exe is never an agent's shell")
	assert.Equal(t, "pwsh.exe", Pick(`C:\Git\bin\bash.exe`, look("pwsh.exe")), "a launch is never wrapped in bash")
}

func TestLegacyNativeArgs(t *testing.T) {
	for shell, want := range map[string]bool{
		"powershell.exe": true,
		`C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`: true,
		"pwsh.exe":                               false,
		"pwsh":                                   false,
		`C:\Program Files\PowerShell\7\pwsh.exe`: false,
		"PWSH.EXE":                               false,
		"":                                       true,
	} {
		assert.Equal(t, want, LegacyNativeArgs(shell), shell)
	}
}

func TestPowerShellFollowsCoralShell(t *testing.T) {
	t.Setenv("CORAL_SHELL", `C:\PS7\pwsh.exe`)
	assert.Equal(t, `C:\PS7\pwsh.exe`, PowerShell())
	t.Setenv("CORAL_SHELL", "powershell.exe")
	assert.Equal(t, "powershell.exe", PowerShell(), "a changed CORAL_SHELL is not answered from the cache")
}
