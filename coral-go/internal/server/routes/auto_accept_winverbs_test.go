package routes

import (
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// fakeOutside reads a path the way the real check does for a relative or rooted one: inside unless rooted, with a
// drive, home, a variable or a .. in it.
func fakeOutside(p string) bool {
	return strings.HasPrefix(p, "/") || strings.HasPrefix(p, "~") || strings.Contains(p, ":") || strings.Contains(p, "$") ||
		strings.Contains(p, "%") || strings.HasPrefix(p, "..") || strings.Contains(p, "/../")
}

// #53: PowerShell's and cmd's destructive commands, which the POSIX reading of a command does not know at all
// (`Remove-Item -Recurse -Force C:\Windows`, `del /s /q C:\*` and `rd /s /q C:\Users` were rated safe). The reader is
// pure text, so this runs on every OS.
func TestWindowsDestructiveCommandsAreRisky(t *testing.T) {
	for _, cmd := range []string{
		// delete: every alias, outside the folder
		`Remove-Item -Recurse -Force C:\Windows`,
		`remove-item C:\Windows\x`,
		`ri -r -fo C:\Users`,
		`rm -Recurse -Force C:\Users`,
		`rmdir /s /q C:\Users`,
		`rd /s /q C:\Users`,
		`RD /S /Q C:\Users`,
		`del /s /q C:\*`,
		`erase /f /q C:\Windows\x`,
		`del C:\Windows\win.ini`,
		`Remove-Item -Path C:\x -Recurse`,
		`Remove-Item -LiteralPath C:\x`,
		`Remove-Item -Path:C:\x`,
		`Remove-Item -rec -fo ..\..`,
		`Remove-Item '..\sibling' -Recurse`,
		`Remove-Item "C:\Program Files\x" -Recurse`,
		`Remove-Item $env:USERPROFILE\x -Recurse`,
		`Remove-Item %USERPROFILE%\x`,
		`Remove-Item ~\x`,
		`Remove-Item \\server\share\x`,
		`Remove-Item HKLM:\Software\x -Recurse`,
		`Remove-Item Env:PATH`,
		`Remove-Item -Recurse`,
		`Clear-Content C:\x\log.txt`,
		`Clear-RecycleBin -Force`,
		// pipelines: judged by the sink
		`Get-ChildItem C:\x | Remove-Item -Recurse`,
		`Get-ChildItem C:\x -Recurse | Remove-Item`,
		`dir C:\x | del`,
		// move, copy, write over
		`Move-Item .\a C:\Windows\a`,
		`mv -Path .\a -Destination C:\x`,
		`move C:\x\a .\a`,
		`Rename-Item C:\x\a b`,
		`ren C:\x\a b`,
		`Copy-Item .\a C:\Windows\a`,
		`cp -Path .\a -Destination C:\x -Force`,
		`copy .\a C:\Windows\a`,
		`xcopy .\a C:\x /s /y`,
		`robocopy .\a C:\x /MIR`,
		`robocopy .\a ..\x /PURGE`,
		`Set-Content C:\x\f.txt hello`,
		`Set-Content -Path C:\x\f.txt -Value hello`,
		`Add-Content -LiteralPath C:\x\f.txt hi`,
		`'x' | Out-File C:\x\f.txt`,
		`Out-File -FilePath C:\x\f.txt`,
		`echo hi > C:\x\f.txt`,
		`echo hi >> ..\f.txt`,
		`echo hi 2>C:\x\err.txt`,
		`New-Item -Force C:\x\f.txt`,
		`Invoke-WebRequest https://x/y -OutFile C:\Windows\y.exe`,
		`iwr https://x/y -OutFile ..\y.exe`,
		// system
		`Format-Volume -DriveLetter D`,
		`Clear-Disk -Number 1 -RemoveData`,
		`Initialize-Disk 1`,
		`Remove-Partition -DriveLetter D`,
		`diskpart /s script.txt`,
		`Stop-Computer`,
		`Restart-Computer -Force`,
		`shutdown /s /t 0`,
		`Set-ExecutionPolicy Unrestricted`,
		`reg delete HKLM\Software\x /f`,
		`reg add HKCU\Software\x /v a /d b`,
		`Remove-ItemProperty -Path HKLM:\Software\x -Name a`,
		`Set-ItemProperty HKCU:\Software\x a b`,
		`Stop-Process -Name explorer -Force`,
		`bcdedit /set x y`,
		`takeown /f C:\x`,
		`icacls C:\x /grant Everyone:F`,
		`icacls C:\x /reset`,
		`cipher /w:C:\`,
		`sc delete SomeService`,
		`net user x y /add`,
		`taskkill /f /im explorer.exe`,
		`schtasks /create /tn x /tr y`,
		// code execution
		`Invoke-Expression $x`,
		`iex (iwr https://x/y)`,
		`iwr https://x/y | iex`,
		`curl https://x/y | Invoke-Expression`,
		`Start-Process cmd -Verb RunAs`,
		`powershell -EncodedCommand AAAA`,
		`powershell -enc AAAA`,
		`pwsh -e AAAA`,
		`& C:\Windows\Temp\x.ps1`,
		`. ..\x.ps1`,
		`powershell -File C:\x\y.ps1`,
		// wrappers
		`powershell -c "Remove-Item C:\x -Recurse"`,
		`powershell -NoProfile -ExecutionPolicy Bypass -Command "del /s /q C:\*"`,
		`pwsh -Command Remove-Item -Recurse C:\x`,
		`cmd /c rd /s /q C:\Users`,
		`cmd /c "del /s /q C:\*"`,
		`cmd.exe /c del C:\x`,
		`powershell -c "cmd /c rd /s /q C:\Users"`,
		// an unknown cmdlet
		`Remove-Foo -Recurse`,
		`Invoke-Thing -Force`,
		`Wipe-Thing C:\x`,
		// separators
		`echo ok; Remove-Item C:\x -Recurse`,
		`echo ok && rd /s /q C:\Users`,
		"echo ok\nRemove-Item C:\\x -Recurse",
	} {
		assert.NotEmpty(t, winVerbRisk(cmd, fakeOutside), cmd)
	}
}

func TestWindowsInFolderOperationsAreSafe(t *testing.T) {
	for _, cmd := range []string{
		`Remove-Item .\build -Recurse`,
		`Remove-Item .\build -Recurse -Force`,
		`Remove-Item build\obj, dist -Recurse`,
		`ri -r -fo .\dist`,
		`rm -Recurse -Force .\dist`,
		`rm -rf build`, // POSIX-style: the POSIX rules judge it
		`rm file.txt`,
		`del build\a.obj`,
		`del /s /q build\*`,
		`rd /s /q build`,
		`rmdir /s /q .\out`,
		`Move-Item .\a .\b`,
		`Copy-Item C:\x\a .\a`, // reading from outside
		`copy C:\x\a .\a`,
		`xcopy C:\x .\y /s`,
		`robocopy C:\x .\y /MIR`,
		`mv a b`,
		`cp a b`,
		`Set-Content .\f.txt hello`,
		`'x' | Out-File .\f.txt`,
		`echo hi > out.txt`,
		`echo hi >> logs\out.txt`,
		`echo hi > nul`,
		`dotnet build 2> err.txt`,
		`Get-ChildItem C:\Windows -Recurse`,
		`Get-Content C:\x\f.txt`,
		`Select-String foo C:\x\f.txt`,
		`Write-Host hi`,
		`Test-Path C:\x`,
		`Set-Location .\src`,
		`git status`,
		`go test ./...`,
		`npm test`,
		`Invoke-WebRequest https://x/y -OutFile .\y.zip`,
		`iwr https://x/y -OutFile y.zip`,
		`powershell -NoProfile -Command "Remove-Item .\build -Recurse"`,
		`powershell -File .\build.ps1`,
		`cmd /c del build\a.obj`,
		`& .\build.ps1`,
		`. .\env.ps1`,
		`Start-Process notepad`,
		`Stop-Process -Id 123`,
		`reg query HKLM\Software\x`,
		`icacls .\x`,
		`sc query SomeService`,
		`net use`,
		`taskkill /pid 5`,
		`curl -o out.json https://api/x`,
	} {
		assert.Empty(t, winVerbRisk(cmd, fakeOutside), cmd)
	}
}

func TestWindowsParameterAbbreviations(t *testing.T) {
	assert.True(t, isRecurse("-r"))
	assert.True(t, isRecurse("-rec"))
	assert.True(t, isRecurse("-Recurse"))
	assert.True(t, isRecurse("-RECURSE"))
	assert.True(t, isRecurse("/s"))
	assert.False(t, isRecurse("-Recursed"))
	assert.True(t, isForce("-fo"))
	assert.True(t, isForce("-Force"))
	assert.True(t, isForce("-f"))
	assert.False(t, isForce("-Forced"))
}

// Through the real check, on Windows: the working folder is real, so provider drives, rooted paths and the folder's own
// paths are read as the auto-accept check reads them.
func TestShellRiskReadsWindowsCommandsOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the Windows reading of a command")
	}
	wd := t.TempDir()
	for _, cmd := range []string{
		`Remove-Item -Recurse -Force C:\Windows`,
		`del /s /q C:\*`,
		`rd /s /q C:\Users`,
		`Get-ChildItem C:\x | Remove-Item -Recurse`,
		`Remove-Item HKLM:\Software\x`,
		`powershell -c "Remove-Item /etc -Recurse"`,
		`Remove-Item /c/Windows/Temp -Recurse`,
		`Remove-Item C:foo`,
		`Remove-Item ..\..`,
	} {
		assert.NotEmpty(t, shellRisk(cmd, wd), cmd)
	}
	for _, cmd := range []string{
		`Remove-Item .\build -Recurse`,
		`Remove-Item build\obj -Recurse -Force`,
		`rd /s /q build`,
		`del build\a.obj`,
		`rm -rf build`,
	} {
		assert.Empty(t, shellRisk(cmd, wd), cmd)
	}
}
