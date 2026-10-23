package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/updater/verify"
)

// #6290: Mac and Linux are on, Windows stays off until its rehearsal passes. A switch is on only in a build whose pins are all set: the apply answers "pins not set" and installs
// nothing without them, and this fails the build that turns a platform on before the pins are there.
func TestMacAndLinuxAreOnAndWindowsIsOffAndAnOnSwitchNeedsThePins(t *testing.T) {
	assert.True(t, applyEnabledDarwin)
	assert.True(t, applyEnabledLinux)
	assert.False(t, applyEnabledWindows, "Windows stays off until its rehearsal passes")
	assert.True(t, windowsApplyBuilt, "the Windows apply exists (#162), and stays off by applyEnabledWindows")
	assert.True(t, applyOn("darwin", applyEnabledDarwin, applyEnabledLinux, applyEnabledWindows, windowsApplyBuilt))
	assert.True(t, applyOn("linux", applyEnabledDarwin, applyEnabledLinux, applyEnabledWindows, windowsApplyBuilt))
	for _, goos := range []string{"windows", "freebsd", "plan9"} {
		assert.False(t, applyOn(goos, applyEnabledDarwin, applyEnabledLinux, applyEnabledWindows, windowsApplyBuilt), goos)
	}
	p := verify.DefaultPins()
	assert.NotEmpty(t, p.MacTeamIDs, "Mac is on: its signer is pinned")
	assert.NotEmpty(t, p.PromoteRepoID, "the promote identity is pinned")
	assert.Equal(t, ".github/workflows/promote.yml", p.PromoteWorkflow)
	assert.Equal(t, "refs/heads/promote", p.PromoteRef)
	assert.NotEmpty(t, p.RepoID)
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
