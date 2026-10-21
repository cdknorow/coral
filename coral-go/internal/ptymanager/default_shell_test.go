package ptymanager

import (
	"errors"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
)

// #3692: an agent's terminal never starts cmd.exe. A CORAL_SHELL naming it is ignored (and
// reported, for the once-only log); Windows starts PowerShell (pwsh first), and with neither
// installed the launch is refused.
func TestPickShell(t *testing.T) {
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
		{"windows: pwsh first", "windows", "", "", []string{"pwsh.exe", "powershell.exe"}, []string{"pwsh.exe", "-ExecutionPolicy", "Bypass"}, false, nil},
		{"windows: Windows PowerShell", "windows", "", "", []string{"powershell.exe"}, []string{"powershell.exe", "-ExecutionPolicy", "Bypass"}, false, nil},
		{"windows: cmd.exe ignored", "windows", "cmd.exe", "", []string{"pwsh.exe"}, []string{"pwsh.exe", "-ExecutionPolicy", "Bypass"}, true, nil},
		{"windows: a cmd path ignored", "windows", `C:\Windows\System32\CMD.EXE`, "", []string{"powershell.exe"}, []string{"powershell.exe", "-ExecutionPolicy", "Bypass"}, true, nil},
		{"windows: CORAL_SHELL pwsh", "windows", `C:\Program Files\PowerShell\7\pwsh.exe`, "", nil, []string{`C:\Program Files\PowerShell\7\pwsh.exe`, "-ExecutionPolicy", "Bypass"}, false, nil},
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
