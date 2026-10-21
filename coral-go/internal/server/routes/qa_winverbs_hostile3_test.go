package routes

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// QA #5672: a third matrix for winVerbRisk, for the forms the second one (auto_accept_winverbs_hostile2_test.go) did not name. Each must make auto-accept WAIT; each failing line is a finding.
func TestQAWinVerbThirdMatrixWaits(t *testing.T) {
	for _, cmd := range []string{
		// the command is inside for's in-clause, not after do
		`for /f %i in ('format C:') do @echo %i`,
		`for /f "delims=" %i in ('rd /s /q C:\x') do echo %i`,
		`for /f %i in ('del /s /q C:\x') do echo`,
		// parentheses and else
		`if exist x (del C:\x)`, `if exist x (format C:)`, `(format C:)`, `( format C:)`, `if exist x (echo a) else (format C:)`, `if exist x echo a else format C:`,
		`for %i in (a) do (rd /s /q C:\x)`, `echo a & (format C:)`,
		// cmd glued to its switch, quoted command, start
		`cmd/c format C:`, `"%COMSPEC%" /c format C:`, `start format C:`, `start "" format C:`, `start /min format C:`, `cmd /c start /min format C:`,
		// the program named with a directory and .com
		`format.com C:`, `C:\Windows\System32\format.com C:`, `"C:\Windows\System32\format.exe" C:`, `.\format.com C:`,
		// powershell switches with values before the command
		`powershell -ExecutionPolicy Bypass Remove-Item C:\x -Recurse`, `powershell -ep bypass Remove-Item C:\x -Recurse`, `powershell -WindowStyle Hidden Remove-Item C:\x -Recurse`,
		`powershell -Version 2 Remove-Item C:\x -Recurse`, `powershell -NoProfile -ExecutionPolicy Bypass -Command Remove-Item C:\x -Recurse`,
		`powershell -EncodedCommand ZgBvAHIAbQBhAHQA`, `powershell -enc ZgBvAHIAbQBhAHQA`, `powershell -e ZgBvAHIAbQBhAHQA`, `pwsh -EncodedCommand ZgBvAA==`,
		`powershell "& { Remove-Item C:\x -Recurse }"`, `powershell -c "& { Remove-Item C:\x -Recurse }"`, `powershell -Command "& 'C:\Windows\System32\format.com' C:"`,
		// variables holding the command
		`& $cmd C:`, `& $env:ComSpec /c format C:`, `$f = 'format'; & $f C:`, `call %X% C:`,
		// robocopy and xcopy that remove or overwrite
		`robocopy C:\x .\y /mov`, `robocopy C:\x .\y /move`, `robocopy .\a C:\x /e`, `robocopy .\a C:\x /mov`,
		// downloads: glued and long forms
		`curl -oC:\Windows\x http://e`, `curl -o"C:\x" http://e`, `curl --output-dir C:\x -O http://e/f`, `curl --output-dir=C:\x -O http://e/f`, `wget -OC:\x http://e`,
		`curl -K C:\x\cfg http://e`, `curl --config C:\x http://e`, `iwr http://e -OutFile C:\x`, `Invoke-WebRequest http://e -OutFile "C:\x"`, `Start-BitsTransfer http://e -Destination C:\x`,
		// .NET and COM
		`[System.IO.File]::Delete("C:\x")`, `[IO.Directory]::Delete("C:\x", $true)`, `[System.IO.File]::WriteAllText("C:\x", "a")`, `(Get-Item C:\x).Delete()`,
	} {
		assert.NotEmpty(t, winVerbRisk(cmd, fakeOutside), "must wait: %s", cmd)
	}
}

func TestQAWinVerbThirdMatrixLeavesTheBenignAlone(t *testing.T) {
	for _, cmd := range []string{
		`echo "a & b"`, `git commit -m "fix & more"`, `Write-Host 'a & b'`, `go test ./... & echo done`, `echo a >&2`, `dir 2>&1`, `dir > out.txt 2>&1`,
		`for /f %i in ('dir /b') do echo %i`, `for /f "delims=" %i in ('git rev-parse HEAD') do set X=%i`, `if exist x (echo a) else (echo b)`, `(echo a) & (echo b)`,
		`start notepad`, `start "" .\x.html`, `powershell -ExecutionPolicy Bypass -File .\build.ps1`, `powershell -ep bypass -File build.ps1`, `powershell -WindowStyle Hidden Get-Date`,
		`powershell -Version 5 Get-Date`, `powershell -NoProfile -NonInteractive -Command Get-ChildItem`, `robocopy .\a .\b /e`, `robocopy .\a .\b /mov`, `curl -o out.txt http://e`, `curl -oout.txt http://e`, `curl -O http://e/f`, `curl --output-dir out -O http://e/f`, `wget -O out.txt http://e`,
		`(Get-Item .\x).Length`, // asks, by design (safe direction, not asserted): & $env:ProgramFiles\...\git.exe, [Math]::Max(1,2), [System.IO.File]::ReadAllText("a.txt")
		`format-table`, `Get-Date | Format-Table`, `Get-ChildItem | Format-List`,
	} {
		assert.Empty(t, winVerbRisk(cmd, fakeOutside), "must pass: %s", cmd)
	}
}
