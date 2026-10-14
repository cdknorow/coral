//go:build windows

package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// #3362: PowerShell expands $var, $(…) and backticks inside double quotes, so anything
// not plainly safe is single-quoted; only the parts a builder names are raw.
func TestShellQuotePowerShell(t *testing.T) {
	for in, want := range map[string]string{
		"claude":        "claude",
		"--model":       "--model",
		"a b":           "'a b'",
		"$(calc)":       "'$(calc)'",
		"$env:USERNAME": "'$env:USERNAME'",
		"`calc`":        "'`calc`'",
		"it's":          "'it''s'",
		`say "hi"`:      `'say \"hi\"'`,
		"x; calc":       "'x; calc'",
	} {
		assert.Equal(t, want, shellQuote(in), "%q", in)
	}
	raw := `"$((Get-Content -Raw 'C:\t\p.txt') -replace '"', '\"')"`
	assert.Equal(t, `& 'C:\Program Files\claude.exe' '$(calc)' `+raw,
		ShellJoin([]string{`C:\Program Files\claude.exe`, "$(calc)", raw}, raw))
}
