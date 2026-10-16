package ptymanager

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// #3690 (#3689 finding 2): the handoff script's own path is quoted for PowerShell, an apostrophe
// doubled, in both places it appears.
func TestPSHandoffQuotesAnApostropheInThePath(t *testing.T) {
	script, inv := psHandoff(`C:\Users\O'Brien\AppData\Local\Temp\coral-command-1.ps1`, "Write-Output ok")
	assert.Equal(t, "Remove-Item -Force -LiteralPath 'C:\\Users\\O''Brien\\AppData\\Local\\Temp\\coral-command-1.ps1'\nWrite-Output ok\n", script)
	assert.Equal(t, `& 'C:\Users\O''Brien\AppData\Local\Temp\coral-command-1.ps1'`, inv)
	assert.Equal(t, `'it''s ''quoted'''`, psLiteral(`it's 'quoted'`))
	assert.Equal(t, `'$env:X; whoami'`, psLiteral(`$env:X; whoami`), "single quotes: nothing expands")
}

// #3904: a curly apostrophe in the handoff path is doubled like ', so it can't end the string.
func TestPSHandoffDoublesCurlyQuotes(t *testing.T) {
	for _, q := range []string{"\u2018", "\u2019", "\u201a", "\u201b"} {
		script, inv := psHandoff(`C:\\Users\\Jane`+q+`s\\Temp\\coral-command-1.ps1`, "Write-Output ok")
		quoted := `'C:\\Users\\Jane` + q + q + `s\\Temp\\coral-command-1.ps1'`
		assert.Equal(t, "Remove-Item -Force -LiteralPath "+quoted+"\nWrite-Output ok\n", script, "%U", []rune(q)[0])
		assert.Equal(t, "& "+quoted, inv, "%U", []rune(q)[0])
	}
}
