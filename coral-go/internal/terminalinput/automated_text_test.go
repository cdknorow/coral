package terminalinput

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Security's P1/P2 on #40: Coral's own text, typed, has no control character and no line break.
func TestAutomatedTextHasNoKeystrokesAndNoLineBreaks(t *testing.T) {
	for in, want := range map[string]string{
		"hello world":                    "hello world",
		"a\x1b[201~b":                    "a[201~b",
		"stop\x03 now\x04":               "stop now",
		"tab\there":                      "tabhere",
		"c1\u009bX\u0080\u009f end":      "c1X end",
		"del\x7f":                        "del",
		"one\ntwo":                       "one two",
		"one\r\ntwo\r\n\r\nthree":        "one two three",
		"lone\rcr":                       "lone cr",
		"trailing\n":                     "trailing",
		"\nleading":                      "leading",
		"unicode é 日本語 🎉":                "unicode é 日本語 🎉",
		"rm -rf /\nsudo reboot":          "rm -rf / sudo reboot",
		"\x1b]0;title\x07":               "]0;title",
		"mixed \x1b[2J\x08\x00\x1f text": "mixed [2J text",
	} {
		assert.Equal(t, want, AutomatedText(in), "%q", in)
	}
	assert.Equal(t, "ab", SanitizeTyped("a\x1bb"))
	assert.Equal(t, "é", SanitizeTyped("é"), "a character that looks like a C1 byte in Latin-1 is a letter in UTF-8")
}

func TestAutomatedTextOfOnlyControlCharactersIsRefused(t *testing.T) {
	for _, in := range []string{"\x03", "\x1b", "\u009b", "\n\r\n", "\x1b\x04\x03"} {
		out, err := automatedTextChecked(in)
		assert.ErrorIs(t, err, ErrNothingToSend, "%q", in)
		assert.Empty(t, out)
	}
	out, err := automatedTextChecked("")
	assert.NoError(t, err)
	assert.Empty(t, out)
	out, err = automatedTextChecked("ok\x03")
	assert.NoError(t, err)
	assert.Equal(t, "ok", out)
}

// automatedTextChecked is what PrepareTyped does for Coral's text.
func automatedTextChecked(text string) (string, error) { return PrepareTyped(Coral, text) }
