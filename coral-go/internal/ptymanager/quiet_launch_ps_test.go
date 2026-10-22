package ptymanager

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// #5911, Windows: an agent's launch script starts by clearing the screen and the scrollback (ESC[3J), after it has removed itself and before the command, and with
// CORAL_KEEP_LAUNCH_SCRIPTS it stays; the plain handoff is unchanged.
func TestPSHandoffQuietFormClearsFirstAndKeepsWhenAsked(t *testing.T) {
	plainScript, plainInv := psHandoff(`C:\Windows\powershell.exe`, `C:\t\coral-command-1.ps1`, "Write-Output hi")
	quietScript, quietInv := psHandoffWith(`C:\Windows\powershell.exe`, `C:\t\coral-command-1.ps1`, "Write-Output hi", true, false)
	assert.Equal(t, plainInv, quietInv, "the same short line")
	assert.NotContains(t, plainScript, "Clear-Host")
	rm := strings.Index(quietScript, "Remove-Item -Force -LiteralPath")
	clear := strings.Index(quietScript, "Clear-Host")
	run := strings.Index(quietScript, "Write-Output hi")
	assert.True(t, rm >= 0 && rm < clear && clear < run, "removes itself, clears, then runs: %q", quietScript)
	assert.Contains(t, quietScript, "[char]27 + '[3J'", "the scrollback too")
	assert.True(t, strings.HasPrefix(quietScript, utf8BOM))

	keptScript, _ := psHandoffWith(`C:\Windows\powershell.exe`, `C:\t\coral-command-1.ps1`, "Write-Output hi", true, true)
	assert.NotContains(t, keptScript, "Remove-Item -Force -LiteralPath", "kept for debugging")
	assert.Contains(t, keptScript, "Remove-Item Env:PSExecutionPolicyPreference", "the policy variable is still dropped")
	assert.Contains(t, keptScript, "Clear-Host")
}
