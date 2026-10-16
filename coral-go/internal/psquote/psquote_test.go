package psquote

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// #3904: every PowerShell single-quote character is doubled, alone, inside a word, at its end
// and before a statement separator, so none of them can end the string early.
func TestLiteralDoublesEverySingleQuote(t *testing.T) {
	assert.Equal(t, "'it''s'", Literal("it's"))
	assert.Equal(t, "'$env:X; whoami'", Literal("$env:X; whoami"), "nothing expands")
	assert.Equal(t, "''", Literal(""))
	for _, q := range []string{"'", "\u2018", "\u2019", "\u201a", "\u201b"} {
		assert.True(t, IsSingleQuote([]rune(q)[0]), "%U", []rune(q)[0])
		assert.Equal(t, "'"+q+q+"'", Literal(q), "%U alone", []rune(q)[0])
		assert.Equal(t, "'O"+q+q+"Brien'", Literal("O"+q+"Brien"), "%U inside", []rune(q)[0])
		assert.Equal(t, "'x"+q+q+"; calc'", Literal("x"+q+"; calc"), "%U before ;", []rune(q)[0])
		assert.Equal(t, "'C:\\Users\\Jane"+q+q+"s\\claude.exe'", Literal("C:\\Users\\Jane"+q+"s\\claude.exe"), "%U in a path", []rune(q)[0])
	}
	// The double curly quotes don't end a single-quoted string: left as they are.
	for _, q := range []string{"\u201c", "\u201d", "\u201e", "\""} {
		assert.False(t, IsSingleQuote([]rune(q)[0]))
		assert.Equal(t, "'a"+q+"b'", Literal("a"+q+"b"))
	}
}
