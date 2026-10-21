package ptymanager

import (
	"errors"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/subgentic/coral/internal/winshell"
)

// #3692: an agent's terminal never starts cmd.exe. A CORAL_SHELL naming it is ignored (and
// reported, for the once-only log); Windows starts PowerShell (pwsh first), and with neither
// installed the launch is refused.
func TestPickShell(t *testing.T) {
	look := func(have ...string) func(string) (string, error) {
		return func(name string) (string, error) {
			for _, h := range have {
				if h == name && len(name) > 1 && name[1] == ':' {
					return name, nil // an absolute path, as it is
				}
				if h == name {
					return `C:\Tools\` + name, nil
				}
			}
			return "", exec.ErrNotFound
		}
	}
	t.Cleanup(winshell.SetSystemPowerShellForTest(func() string { return "" })) // "none installed" is none
	for _, tc := range []struct {
		name, goos, coral, user string
		have                    []string
		want                    []string
		ignored                 bool
		err                     error
	}{
		{"unix: SHELL", "linux", "", "/bin/zsh", nil, []string{"/bin/zsh"}, false, nil},
		{"unix: no SHELL", "darwin", "", "", nil, []string{"/bin/sh"}, false, nil},
		{"unix: CORAL_SHELL wins", "linux", "/usr/bin/fish", "/bin/zsh", nil, []string{"/usr/bin/fish"}, false, nil},
		{"unix: cmd ignored", "linux", "cmd", "/bin/bash", nil, []string{"/bin/bash"}, true, nil},
		// By absolute path, never a bare name (#26 G1A).
		{"windows: pwsh first", "windows", "", "", []string{"pwsh.exe", "powershell.exe"}, []string{`C:\Tools\pwsh.exe`}, false, nil},
		{"windows: Windows PowerShell", "windows", "", "", []string{"powershell.exe"}, []string{`C:\Tools\powershell.exe`}, false, nil},
		{"windows: cmd.exe ignored", "windows", "cmd.exe", "", []string{"pwsh.exe"}, []string{`C:\Tools\pwsh.exe`}, true, nil},
		{"windows: a cmd path ignored", "windows", `C:\Windows\System32\CMD.EXE`, "", []string{"powershell.exe"}, []string{`C:\Tools\powershell.exe`}, true, nil},
		{"windows: CORAL_SHELL pwsh", "windows", `C:\Program Files\PowerShell\7\pwsh.exe`, "", []string{`C:\Program Files\PowerShell\7\pwsh.exe`}, []string{`C:\Program Files\PowerShell\7\pwsh.exe`}, false, nil},
		{"windows: CORAL_SHELL pwsh not there", "windows", `C:\Program Files\PowerShell\7\pwsh.exe`, "", []string{"powershell.exe"}, []string{`C:\Tools\powershell.exe`}, false, nil},
		{"windows: no PowerShell", "windows", "", "", nil, nil, false, errNoPowerShell},
		{"windows: cmd and no PowerShell", "windows", "cmd", "", []string{"cmd.exe"}, nil, true, errNoPowerShell},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ignored, err := pickShell(tc.goos, tc.coral, tc.user, look(tc.have...))
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.ignored, ignored)
			assert.True(t, errors.Is(err, tc.err), "err = %v", err)
		})
	}
	assert.Contains(t, errNoPowerShell.Error(), "install PowerShell", "the refusal says what to do")
}
