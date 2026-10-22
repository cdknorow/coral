package routes

import (
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
