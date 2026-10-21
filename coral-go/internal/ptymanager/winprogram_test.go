package ptymanager

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWindowsProgram(t *testing.T) {
	for name, want := range map[string]string{
		`C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`: `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`,
		`C:\Program Files\PowerShell\7\pwsh.exe`:                    `"C:\Program Files\PowerShell\7\pwsh.exe"`,
		`D:\Dev Tools\x.exe`:                                        `"D:\Dev Tools\x.exe"`,
		"C:\\a\tb\\x.exe":                                           "\"C:\\a\tb\\x.exe\"",
		"pwsh.exe":                                                  "pwsh.exe",
	} {
		got, err := windowsProgram(name)
		assert.NoError(t, err, name)
		assert.Equal(t, want, got, name)
	}
	for _, bad := range []string{"", `C:\a"b\x.exe`, "C:\\a\x00b.exe"} {
		_, err := windowsProgram(bad)
		assert.Error(t, err, "%q", bad)
	}
}
