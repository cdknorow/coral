package routes

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// QA #5655: hostile and awkward Windows commands for winVerbRisk (the reader of PowerShell and cmd destructive commands for auto-accept), beyond the table of auto_accept_winverbs_test.go. Each
// case is a command that must make auto-accept WAIT (a non-empty reason) or one that must pass (""), with fakeOutside as the path check. A case that fails is a finding, not a typo: auto-accept
// answering yes to a command that deletes or overwrites outside the working folder is the cost.
func TestWinVerbHostileCommandsWait(t *testing.T) {
	for _, cmd := range []string{
		// wrappers that run another command line
		`powershell -Command "Remove-Item -Recurse -Force C:\Windows"`,
		`powershell.exe -NoProfile -Command "Remove-Item C:\x -Recurse"`,
		`pwsh -c "rd /s /q C:\Users"`,
		`cmd /c "rd /s /q C:\Users"`,
		`cmd.exe /c del /s /q C:\*`,
		`iex "Remove-Item -Recurse C:\x"`,
		`Invoke-Expression 'Remove-Item -Recurse C:\x'`,
		`& { Remove-Item -Recurse C:\x }`,
		`& "Remove-Item" -Recurse C:\x`,
		`. { del /s C:\x }`,
		`start /b cmd /c rd /s /q C:\x`,
		`Start-Process cmd -ArgumentList "/c rd /s /q C:\x"`,
		// pipelines
		`Get-ChildItem C:\ | Remove-Item -Recurse -Force`,
		`Get-ChildItem -Recurse | Remove-Item -Force`,
		`ls C:\Users | ForEach-Object { Remove-Item $_.FullName -Recurse }`,
		`Get-ChildItem C:\x | % { Remove-Item $_ }`,
		// separators and quoting
		`echo hi; Remove-Item -Recurse C:\x`,
		`echo hi && rd /s /q C:\x`,
		`echo hi || del /s C:\x`,
		"echo hi\nRemove-Item -Recurse C:\\x",
		"echo hi\r\nrd /s /q C:\\x",
		`Remove-Item -Recurse "C:\x"`,
		"Remove-Item -Recurse 'C:\\x y'",
		"Remove-Item -Recurse C:\\`x",
		`Re""move-Item -Recurse C:\x`,
		// case, spacing, abbreviations, switches in odd places
		`REMOVE-ITEM -RECURSE C:\x`,
		`remove-item   -recurse    -force   C:\x`,
		`Remove-Item C:\x -Rec`,
		`Remove-Item -Force -Recurse -Path C:\x`,
		`del /Q /S C:\x`,
		`del/s/q C:\x`,
		`rd/s/q C:\x`,
		`rmdir /S/Q C:\x`,
		`del /f C:\Windows\system32\x.dll`,
		`erase C:\x`,
		// writes and moves outside
		`Move-Item .\a C:\Windows\a`,
		`move .\a C:\Windows\a`,
		`Copy-Item .\a C:\Windows\a`,
		`copy .\a C:\Windows\a`,
		`xcopy .\a C:\Windows\ /s`,
		`robocopy .\a C:\Windows /mir`,
		`robocopy C:\x C:\y /purge`,
		`Rename-Item C:\x y`,
		`ren C:\x y`,
		`Set-Content C:\Windows\x hi`,
		`Add-Content C:\Windows\x hi`,
		`Out-File C:\Windows\x`,
		`echo hi | Out-File C:\Windows\x`,
		`echo hi > C:\Windows\x`,
		`echo hi >> C:\Windows\x`,
		`echo hi 1> C:\Windows\x`,
		`echo hi 2> C:\Windows\x`,
		`New-Item C:\Windows\x -ItemType File`,
		`ni C:\x`,
		`Clear-Content C:\x`,
		`Set-ItemProperty C:\x -Name a -Value b`,
		// destructive system verbs
		`Format-Volume -DriveLetter C`,
		`Clear-Disk -Number 0 -RemoveData`,
		`diskpart /s x.txt`,
		`shutdown /s /t 0`,
		`Stop-Computer -Force`,
		`Restart-Computer -Force`,
		`takeown /f C:\Windows /r`,
		`icacls C:\Windows /grant Everyone:F /t`,
		`reg delete HKLM\Software\x /f`,
		`Remove-ItemProperty -Path HKLM:\Software\x -Name y`,
		`cipher /w:C:\`,
		// unreadable targets
		`Remove-Item -Recurse $env:USERPROFILE`,
		`Remove-Item -Recurse $HOME`,
		`Remove-Item -Recurse ~`,
		`del /s /q %USERPROFILE%`,
		`rd /s /q %SystemRoot%`,
		`Remove-Item -Recurse (Get-Location).Parent`,
		`Remove-Item -Recurse $(Split-Path -Parent .)`,
		`Remove-Item -Recurse ..\..`,
		`Remove-Item -Recurse .\..\..`,
		`Remove-Item -Recurse .\a\..\..\b`,
		`Remove-Item -Recurse \\server\share\x`,
		`Remove-Item -Recurse //server/share/x`,
		`Remove-Item -Recurse \\?\C:\x`,
		`Remove-Item -Recurse \\.\C:\x`,
		`Remove-Item -Recurse C:x`,
		`Remove-Item -Recurse \Windows`,
		`Remove-Item -Recurse /Windows`,
	} {
		assert.NotEmpty(t, winVerbRisk(cmd, fakeOutside), "must wait: %s", cmd)
	}
}

func TestWinVerbHarmlessCommandsPass(t *testing.T) {
	for _, cmd := range []string{
		`Get-ChildItem`, `dir`, `ls`, `type README.md`, `Get-Content .\a.txt`, `cat a.txt`, `echo hi`, `Write-Output hi`, `git status`, `go test ./...`, `npm run build`,
		`Remove-Item .\build -Recurse`, `Remove-Item .\build\* -Recurse -Force`, `del temp.txt`, `del .\temp.txt`, `rd /s /q build`, `rmdir /s /q .\build`,
		`echo hi > NUL`, `echo hi > $null`, `echo hi 2> NUL`, `dir > out.txt`, `Get-ChildItem | Out-File list.txt`, `Out-File .\x.txt`,
		`Copy-Item .\a .\b`, `Move-Item a b`, `New-Item .\x -ItemType File`, `mkdir build`, `md .\x\y`,
		`Set-Content a.txt hi`, `Add-Content .\a.txt hi`, `Rename-Item a b`,
		`powershell -Command "Get-ChildItem"`, `cmd /c dir`,
	} {
		assert.Empty(t, winVerbRisk(cmd, fakeOutside), "must pass: %s", cmd)
	}
}
