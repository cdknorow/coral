//go:build windows

package routes

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// QA #5655, on Windows (the part the Linux and macOS runs cannot see): shellRisk, the whole reading auto-accept uses (the POSIX words, the same command with its backslashes as slashes, and
// PowerShell's and cmd's own verbs), over the commands the Windows verb table alone did not rate in QA's hostile matrix (winVerbRisk, qa_winverbs_hostile_test.go): the question is whether
// some other part of shellRisk catches each, so that auto-accept still waits. All of them wait now (#5662).
func TestShellRiskOnWindowsWaitsForTheCommandsTheVerbTableMisses(t *testing.T) {
	wd := t.TempDir()
	for _, cmd := range []string{
		`dir /b C:\x | xargs del`,
		`mkdir C:\Windows\x`,
		`md C:\x`,
		`format C:`,
		`format D: /q /y`,
		`mv .\a C:\Windows\a`,
		`echo hi *> C:\Windows\x`,
		// the rest of the matrix, through the whole reading
		`Remove-Item -Recurse -Force C:\Windows`,
		`del /s /q C:\*`,
		`rd /s /q C:\Users`,
		`cmd /c "rd /s /q C:\Users"`,
		`powershell -Command "Remove-Item -Recurse -Force C:\Windows"`,
		`echo hi > C:\Windows\x`,
		`Copy-Item .\a C:\Windows\a`,
		`robocopy C:\x C:\y /purge`,
		`Remove-Item -Recurse ..\..`,
		`Remove-Item -Recurse $env:USERPROFILE`,
		`rm -rf /c/Windows`,
		`rm -rf C:\Windows`,
		`rm -rf C:/Windows`,
		`rm -rf ../..`,
	} {
		assert.NotEmpty(t, shellRisk(cmd, wd), "auto-accept must wait: %s", cmd)
	}
	for _, cmd := range []string{
		`Get-ChildItem`, `dir`, `git status`, `go test ./...`, `echo hi`, `echo hi > NUL`, `Remove-Item .\build -Recurse`, `del temp.txt`, `rd /s /q build`, `mkdir build`, `md .\x`,
		`Copy-Item .\a .\b`, `type README.md`, `findstr foo a.txt`, `go build -o bin\coral.exe .\cmd\coral`,
	} {
		assert.Empty(t, shellRisk(cmd, wd), "a command inside the folder passes: %s", cmd)
	}
}
