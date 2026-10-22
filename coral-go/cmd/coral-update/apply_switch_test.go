package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #5879: the apply is switched per platform; this commit turns none on.
func TestNoPlatformHasTheApplyOnInThisChange(t *testing.T) {
	assert.False(t, applyEnabledDarwin, "Mac stays off until the operator turns it on, in a change of its own")
	assert.False(t, applyEnabledLinux, "Linux stays off until the operator turns it on, in a change of its own")
	assert.False(t, applyEnabledWindows, "Windows stays off")
	assert.True(t, windowsApplyBuilt, "the Windows apply exists (#162), and stays off by applyEnabledWindows")
	for _, goos := range []string{"darwin", "linux", "windows", "freebsd"} {
		assert.False(t, applyOn(goos, applyEnabledDarwin, applyEnabledLinux, applyEnabledWindows, windowsApplyBuilt), goos)
	}
	assert.False(t, applyEnabled, "the switch this build reads")
}

func TestEachPlatformReadsItsOwnSwitch(t *testing.T) {
	assert.True(t, applyOn("darwin", true, false, false, false))
	assert.False(t, applyOn("darwin", false, true, true, true), "Linux's and Windows's do not turn Mac on")
	assert.True(t, applyOn("linux", false, true, false, false))
	assert.False(t, applyOn("linux", true, false, true, true))
	assert.False(t, applyOn("plan9", true, true, true, true), "an OS with no apply")
}

// Windows cannot be turned on before its apply exists: the constant alone is refused.
func TestWindowsIsRefusedEvenIfItsSwitchIsFlipped(t *testing.T) {
	assert.False(t, applyOn("windows", true, true, true, false))
	assert.True(t, applyOn("windows", false, false, true, true), "only with its apply built")
}

// windowsApplyBuilt may be true only when the Windows apply is real: the MSI major upgrade (#162), apply.RunMSI over Windows Installer
// (apply.WindowsMSI), is what coral-update's Windows apply runs. (pid_windows.go's stubs are the macOS/Linux swap apply's, which Windows
// never takes.)
func TestWindowsApplyBuiltNeedsARealWindowsApply(t *testing.T) {
	raw, err := os.ReadFile("apply_windows.go")
	require.NoError(t, err)
	real := strings.Contains(string(raw), "apply.RunMSI(") && strings.Contains(string(raw), "&apply.WindowsMSI{")
	_, statErr := os.Stat(filepath.Join("..", "..", "internal", "updater", "apply", "apply_msi.go"))
	if windowsApplyBuilt || applyEnabledWindows {
		assert.True(t, real && statErr == nil, "the Windows apply must be the MSI one before Windows can be switched on")
	}
}
