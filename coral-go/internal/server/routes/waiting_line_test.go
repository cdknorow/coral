package routes

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Security's rule for the Inbox row text (#6581, #6592): clean the whole text, redact the whole text, mask the home folder, then one line of at most 200 characters, never a blob,
// and nothing at all when the text cannot be checked.

func tokenOf(prefix string, n int) string {
	const alnum = "AbCdEfGhIjKlMnOpQrStUvWxYz0123456789"
	var b strings.Builder
	b.WriteString(prefix)
	for i := 0; i < n; i++ {
		b.WriteByte(alnum[(i*7+3)%len(alnum)])
	}
	return b.String()
}

// noFragment fails when the line holds any run of n characters of secret.
func noFragment(t *testing.T, line, secret string, n int, msg string) {
	t.Helper()
	for i := 0; i+n <= len(secret); i++ {
		assert.NotContains(t, line, secret[i:i+n], msg)
	}
}

func lineOf(t *testing.T, text string) string {
	t.Helper()
	v := waitingLine(text)
	if v == nil {
		return ""
	}
	s, ok := v.(string)
	require.True(t, ok)
	return s
}

func TestWaitingLinePlainRequestIsKept(t *testing.T) {
	assert.Equal(t, "Notification: Claude needs your permission to use Bash", lineOf(t, "Notification: Claude needs your permission to use Bash"))
	assert.Nil(t, waitingLine(""))
	assert.Nil(t, waitingLine("  \n\t "))
}

func TestWaitingLineIsOneLineOfAtMost200Characters(t *testing.T) {
	long := strings.Repeat("word ", 200)
	l := lineOf(t, long)
	assert.LessOrEqual(t, utf8.RuneCountInString(l), 201, "200 characters and an ellipsis")
	assert.True(t, strings.HasSuffix(l, "…"))
	assert.Equal(t, "a b c d", lineOf(t, "a\nb\t c\r\n d"), "white space is one space, newlines included")
	assert.NotContains(t, lineOf(t, "multi\nline\ncommand"), "\n")
}

func TestWaitingLineASecretAcrossTheCutLeavesNothing(t *testing.T) {
	tok := tokenOf("ghp_", 36)
	for _, pad := range []int{150, 170, 185, 190, 195, 199} {
		text := strings.Repeat("x ", pad/2) + tok + " tail words"
		l := lineOf(t, text)
		noFragment(t, l, tok, 8, "no part of the token is shown, whatever the cut falls on")
	}
	// A whole-text redaction, not a cut first: the token sits past the 200th character and is gone from what is shown too.
	l := lineOf(t, strings.Repeat("filler ", 40)+"token "+tok)
	noFragment(t, l, tok, 8, "a token past the cut")
}

func TestWaitingLineAKeyBlockLeavesNoPartOfItself(t *testing.T) {
	body := []string{"MIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQC7", "VGhpcyBpcyBub3QgYSByZWFsIGtleSBidXQgbG9va3MgbGlrZSBvbmU", "bWlkZGxlIGxpbmUgb2YgdGhlIGtleSBib2R5IHdpdGggbm8gZ2Fw"}
	text := "Claude needs your permission to use Bash: cat key.pem\n-----BEGIN PRIVATE KEY-----\n" + strings.Join(body, "\n") + "\n-----END PRIVATE KEY-----"
	l := lineOf(t, text)
	for _, b := range body {
		noFragment(t, l, b, 10, "no line of the key body")
	}
	// The same key with the header out of the window (a text that starts in the middle of it): the body lines are blobs and are not shown.
	l = lineOf(t, strings.Join(body, "\n"))
	for _, b := range body {
		noFragment(t, l, b, 10, "a key body without its header")
	}
}

func TestWaitingLineABlobIsNotShown(t *testing.T) {
	assert.Nil(t, waitingLine(strings.Repeat("QUJDREVGR0hJSktM", 8)), "a base64 run")
	assert.Nil(t, waitingLine(strings.Repeat("deadbeef", 6)), "a hex run")
	assert.NotNil(t, waitingLine("ls -la /tmp/some/dir"), "a command with spaces and a path is not a blob")
}

func TestWaitingLineHasNoTerminalControl(t *testing.T) {
	l := lineOf(t, "\x1b[31mrm -rf build\x1b[0m \x1b]0;title\x07 ‮evil​ text\x00")
	for _, bad := range []string{"\x1b", "\x07", "‮", "​", "\x00"} {
		assert.NotContains(t, l, bad)
	}
	assert.Contains(t, l, "rm -rf build")
}

func TestWaitingLineMasksTheHomeFolder(t *testing.T) {
	old := homeFolder
	homeFolder = func() string { return "/Users/ann" }
	t.Cleanup(func() { homeFolder = old })
	assert.Equal(t, "cat ~/.ssh/config ~ /Users/annabel/x /Users/ann2", lineOf(t, "cat /Users/ann/.ssh/config /Users/ann /Users/annabel/x /Users/ann2"), "the folder and what is under it; not another name that starts the same")
	assert.NotContains(t, lineOf(t, "open /Users/ann/Documents/tax.pdf"), "ann")
}

func TestWaitingLineIsNothingWhenTheRedactorFails(t *testing.T) {
	old := redactWaiting
	redactWaiting = func(string) (string, bool) { return "", false }
	t.Cleanup(func() { redactWaiting = old })
	assert.Nil(t, waitingLine("Claude needs your permission to use Bash: echo hi"), "never the raw text")
}

func TestWaitingLineIfOnlyWhenWaiting(t *testing.T) {
	assert.Nil(t, waitingLineIf(false, "Claude needs your permission"))
	assert.Equal(t, "Claude needs your permission", waitingLineIf(true, "Claude needs your permission"))
}

// A text of any size costs a bounded amount: only the start of it reaches the redactor.
func TestWaitingLineReadsABoundedWindow(t *testing.T) {
	old := redactWaiting
	seen := -1
	redactWaiting = func(text string) (string, bool) { seen = len(text); return old(text) }
	t.Cleanup(func() { redactWaiting = old })
	assert.NotNil(t, waitingLine(strings.Repeat("a b ", 1<<20)))
	assert.Greater(t, seen, 0)
	assert.LessOrEqual(t, seen, maxReasonInput, "what the redactor is given is bounded, whatever the text's size")
}

// The text is cleaned before it is redacted: a control character inside a token cannot split it past the redactor.
func TestWaitingLineIsCleanedBeforeItIsRedacted(t *testing.T) {
	tok := tokenOf("ghp_", 36)
	for name, sep := range map[string]string{"zero-width": "\u200b", "escape sequence": "\x1b[0m", "bidi": "\u202e", "NUL": "\x00", "bell": "\x07"} {
		l := lineOf(t, "token "+tok[:12]+sep+tok[12:])
		noFragment(t, l, tok, 8, name+" in the middle of a token does not hide it from the redactor")
	}
}
