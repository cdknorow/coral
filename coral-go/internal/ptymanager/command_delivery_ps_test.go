package ptymanager

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// #3690 (#3689 finding 2): the handoff script's own path is quoted for PowerShell, an apostrophe
// doubled, in both places it appears.
func TestPSHandoffQuotesAnApostropheInThePath(t *testing.T) {
	script, inv := psHandoff(`C:\Users\O'Brien\AppData\Local\Temp\coral-command-1.ps1`, "Write-Output ok")
	assert.Equal(t, "Remove-Item -Force 'C:\\Users\\O''Brien\\AppData\\Local\\Temp\\coral-command-1.ps1'\nWrite-Output ok\n", script)
	assert.Equal(t, `& 'C:\Users\O''Brien\AppData\Local\Temp\coral-command-1.ps1'`, inv)
	assert.Equal(t, `'it''s ''quoted'''`, psLiteral(`it's 'quoted'`))
	assert.Equal(t, `'$env:X; whoami'`, psLiteral(`$env:X; whoami`), "single quotes: nothing expands")
}
