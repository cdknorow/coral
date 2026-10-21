package tmux

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// On tmux, the typed path (send-keys -l) of an automated send is sanitised and flattened; a person's send is not.
func TestAutomatedTypedSendIsSanitisedOnTmux(t *testing.T) {
	skipWithoutUnixTmux(t)
	c, dir := fakeTmux(t)
	ctx := WithAutomated(context.Background())
	require.NoError(t, c.SendKeys(ctx, "agent", "hi\x1b[201~\x03 there\nsecond line", "claude", longTextSID))
	joined := strings.Join(readCalls(t, dir), "\n")
	assert.Contains(t, joined, "send-keys -t "+promptTestSession+":0.0 -l -- hi[201~ there second line")
	assert.NotContains(t, joined, "paste-buffer", "flattened to one typed line")
	assert.NotContains(t, joined, "\x1b")
}

func TestAPersonsTypedSendIsNotChangedOnTmux(t *testing.T) {
	skipWithoutUnixTmux(t)
	c, dir := fakeTmux(t)
	require.NoError(t, c.SendKeys(context.Background(), "agent", "line one\nline two", "claude", longTextSID))
	joined := strings.Join(readCalls(t, dir), "\n")
	assert.Contains(t, joined, "paste-buffer", "multi-line text from a person is still pasted as it was")
}
