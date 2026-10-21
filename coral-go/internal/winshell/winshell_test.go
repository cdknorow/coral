package winshell

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// look is a PATH search that finds the names in have, in C:\Tools, and the absolute paths in have
// as they are.
func look(have ...string) func(string) (string, error) {
	return func(name string) (string, error) {
		for _, h := range have {
			if h == name {
				if absolute(name) {
					return name, nil
				}
				return `C:\Tools\` + name, nil
			}
		}
		return "", exec.ErrNotFound
	}
}

func TestPick(t *testing.T) {
	t.Cleanup(SetSystemPowerShellForTest(func() string { return "" }))
	assert.Equal(t, `C:\Tools\pwsh.exe`, Pick("", look("pwsh.exe", "powershell.exe")))
	assert.Equal(t, `C:\Tools\powershell.exe`, Pick("", look("powershell.exe")))
	assert.Equal(t, "", Pick("", look()))
	ps7 := `C:\Program Files\PowerShell\7\pwsh.exe`
	assert.Equal(t, ps7, Pick(ps7, look(ps7)), "CORAL_SHELL naming PowerShell")
	assert.Equal(t, `C:\Tools\powershell.exe`, Pick(ps7, look("powershell.exe")), "a CORAL_SHELL that isn't there: the search")
	assert.Equal(t, `C:\Tools\POWERSHELL`, Pick("POWERSHELL", look("POWERSHELL")), "CORAL_SHELL by name: found on PATH")
	assert.Equal(t, `C:\Tools\powershell.exe`, Pick("cmd.exe", look("powershell.exe")), "cmd.exe is never an agent's shell")
	assert.Equal(t, `C:\Tools\pwsh.exe`, Pick(`C:\Git\bin\bash.exe`, look("pwsh.exe")), "a launch is never wrapped in bash")
	// A relative answer (a lookPath that found it in the current folder) is never used.
	rel := func(string) (string, error) { return `.\powershell.exe`, nil }
	assert.Equal(t, "", Pick("", rel))
	// With nothing on PATH: Windows PowerShell in the system folder.
	t.Cleanup(SetSystemPowerShellForTest(func() string { return `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe` }))
	assert.Equal(t, `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`, Pick("", look()))
}

func TestAbsolute(t *testing.T) {
	for p, want := range map[string]bool{
		`C:\x`: true, `c:/x`: true, `\\server\share`: true, "//server/share": true,
		`powershell.exe`: false, `.\powershell.exe`: false, `C:x`: false, `\x`: false, "": false,
	} {
		assert.Equal(t, want, absolute(p), p)
	}
}

// #26 G1A: a powershell.exe or pwsh.exe in the current folder (the agent's repository) is never
// the one Coral starts: the real search refuses it, and the answer is an absolute path.
func TestPickNeverRunsADecoyInTheCurrentFolder(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows looks in the current folder first")
	}
	decoys := t.TempDir()
	for _, name := range PowerShells {
		require.NoError(t, os.WriteFile(filepath.Join(decoys, name), []byte("MZ"), 0o700))
	}
	t.Chdir(decoys)
	got := Pick("", exec.LookPath)
	require.NotEmpty(t, got)
	assert.True(t, absolute(got), got)
	assert.False(t, strings.HasPrefix(strings.ToLower(got), strings.ToLower(decoys)), "the decoy: %s", got)
	got = Pick("powershell.exe", exec.LookPath)
	assert.False(t, strings.HasPrefix(strings.ToLower(got), strings.ToLower(decoys)), "CORAL_SHELL=powershell.exe, the decoy: %s", got)
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
	if runtime.GOOS != "windows" {
		t.Skip("finds the real PowerShell")
	}
	ps, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skip("no Windows PowerShell")
	}
	t.Setenv("CORAL_SHELL", ps)
	assert.Equal(t, ps, PowerShell())
	t.Setenv("CORAL_SHELL", "cmd.exe")
	assert.True(t, absolute(PowerShell()), "a changed CORAL_SHELL is not answered from the cache: %s", PowerShell())
}
