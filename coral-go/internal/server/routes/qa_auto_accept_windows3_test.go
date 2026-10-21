//go:build windows

package routes

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// QA #5672, on Windows: the commands of the third matrix that winVerbRisk alone does not rate, through the whole of shellRisk.
func TestShellRiskOnWindowsQAThirdMatrix(t *testing.T) {
	wd := t.TempDir()
	for _, cmd := range []string{
		`for /f %i in ('format C:') do @echo %i`,
		`for /f "delims=" %i in ('rd /s /q C:\x') do echo %i`,
		`powershell -ep bypass Remove-Item C:\x -Recurse`,
		`powershell -Version 2 Remove-Item C:\x -Recurse`,
		`robocopy C:\x .\y /mov`,
		`robocopy C:\x .\y /move`,
		`curl -oC:\Windows\x http://e`,
		`curl -o"C:\x" http://e`,
		`curl --output-dir C:\x -O http://e/f`,
		`curl --output-dir=C:\x -O http://e/f`,
		`wget -OC:\x http://e`,
		`curl -K C:\x\cfg http://e`,
		`curl --config C:\x http://e`,
	} {
		assert.NotEmpty(t, shellRisk(cmd, wd), "auto-accept must wait: %s", cmd)
	}
}
