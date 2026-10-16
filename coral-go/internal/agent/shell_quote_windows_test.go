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
		// #3904: the curly single quotes are doubled too, in a flag and in a path.
		"--name=Jane\u2019s; calc":           "'--name=Jane\u2019\u2019s; calc'",
		"x\u2018; calc":                      "'x\u2018\u2018; calc'",
		"\u201a$(calc)\u201b":                "'\u201a\u201a$(calc)\u201b\u201b'",
		"C:\\Users\\Jane\u2019s\\claude.exe": "'C:\\Users\\Jane\u2019\u2019s\\claude.exe'",
	} {
		assert.Equal(t, want, shellQuote(in), "%q", in)
	}
	raw := `"$((Get-Content -Raw 'C:\t\p.txt') -replace '"', '\"')"`
	assert.Equal(t, `& 'C:\Program Files\claude.exe' '$(calc)' `+raw,
		ShellJoin([]string{`C:\Program Files\claude.exe`, "$(calc)", raw}, raw))
}
