package routes

import (
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// An empty argument (” or "") names no path. winPathsMode indexed its first byte and panicked, so a Windows command with an empty quoted argument (`Remove-Item ”`, `rm ”`, `echo ” > x`,
// `Set-Content ” x`) crashed the risk check instead of being judged.
func TestAnEmptyArgumentIsNotAPath(t *testing.T) {
	assert.Equal(t, []string{"a"}, winPaths([]string{"", "a", ""}))
	assert.Empty(t, winPaths([]string{""}))

	wd := t.TempDir()
	for _, c := range []string{
		`Remove-Item ''`, `rm ''`, `del ""`, `Set-Content '' x`, `Copy-Item '' dest`, `copy "" x`, `Get-ChildItem ''`, `New-Item ''`, `Move-Item '' x`, `rd /s ""`,
		`echo '' > x`, `Remove-Item -Path ''`, `Remove-Item -Recurse ''`, `xcopy "" y`, `Out-File ''`, `Remove-Item -Force ''`, `Set-Location ''`, `attrib ''`,
		`Invoke-WebRequest '' -OutFile x`, `7z x ''`, `tar -xf ''`, `cmd /c del ""`, `git config --global credential.helper ''`,
	} {
		assert.NotPanics(t, func() { _ = shellRisk(c, wd) }, c)
	}
}

// -DestinationPath is a path parameter (Expand-Archive, Compress-Archive): winPathParams only knew its prefix "destination", which "destinationpath" is not a prefix OF, so
// `Expand-Archive -Path x.zip -DestinationPath C:\Windows\Temp\x` named no path and was let through. (The positional Expand-Archive form was already held by the generic rule.)
func TestDestinationPathIsAPathParameter(t *testing.T) {
	// The Windows reader judges Windows paths (winPathsMode hands over "C:/Windows/Temp/x"); pathInside is the host's, so on macOS and Linux "C:/..." is a folder name under the working
	// folder and nothing is held. This stand-in says what Windows would: a drive, a rooted path or a way up is outside, anything else is inside (the host's own check runs below on Windows).
	outsideWin := func(p string) bool {
		return strings.Contains(p, ":") || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "../")
	}
	assert.Equal(t, []string{"x.zip", `C:\out`}, winPaths([]string{"-Path", "x.zip", "-DestinationPath", `C:\out`}))
	assert.Equal(t, []string{`C:\out`}, winPaths([]string{`-DestinationPath:C:\out`}))
	assert.Equal(t, []string{`C:\out`}, winPaths([]string{"-DestinationP", `C:\out`}))
	assert.Equal(t, []string{`C:\out`}, winPaths([]string{"-Destination", `C:\out`}), "-Destination still works")

	wd := t.TempDir()
	for _, c := range []string{
		`Expand-Archive -Path x.zip -DestinationPath C:\Windows\Temp\x`,
		`Expand-Archive -Path x.zip -DestinationPath:C:\Windows\Temp\x`,
		`Expand-Archive x.zip -DestinationPath C:\Windows\Temp\x -Force`,
		`Compress-Archive -Path src -DestinationPath C:\Windows\Temp\x.zip`,
		`Compress-Archive -Path src -DestinationPath:C:\Windows\Temp\x.zip -Update`,
		`Compress-Archive src -DestinationPath ..\..\x.zip`,
	} {
		if runtime.GOOS == "windows" {
			assert.NotEmpty(t, shellRisk(c, wd), "should be held: %s", c)
		}
		assert.NotEmpty(t, winVerbRisk(c, outsideWin), "should be held (the Windows reader): %s", c)
	}
	for _, c := range []string{
		`Expand-Archive -Path x.zip -DestinationPath out`,
		`Expand-Archive -Path x.zip -DestinationPath:out`,
		`Compress-Archive -Path src -DestinationPath out.zip`,
	} {
		assert.Empty(t, winVerbRisk(c, outsideWin), "should be allowed: %s", c)
	}
}
