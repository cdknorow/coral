package ptymanager

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/terminalinput"
	"github.com/subgentic/coral/internal/tmux"
)

func sendTextWrites(t *testing.T, bracketed bool, text string) []string {
	t.Helper()
	withFastSubmit(t)
	b, s, proc := newFakeBackend(t, "claude-7-text")
	s.bracketedPaste.Store(bracketed)
	require.NoError(t, b.SendText(context.Background(), terminalinput.Unclassified, "claude-7-text", text))
	var got []string
	for _, w := range proc.written() {
		got = append(got, string(w))
	}
	return got
}

// #23 F7/F8: on PTY, text with a line break is one paste (as tmux's paste-buffer -p), not typed keys.
func TestPTYSendTextPastesMultiLineText(t *testing.T) {
	assert.Equal(t, []string{"\x1b[200~alpha\nbeta\ngamma\x1b[201~", "\r"}, sendTextWrites(t, true, "alpha\nbeta\ngamma"))
}

// F8: CRLF (PowerShell here-strings, files) is one line break per line, one paste, one submission.
func TestPTYSendTextTakesCRLFAsOneLineBreak(t *testing.T) {
	assert.Equal(t, []string{"\x1b[200~one\ntwo\nthree\x1b[201~", "\r"}, sendTextWrites(t, true, "one\r\ntwo\r\nthree"))
	assert.Equal(t, []string{"\x1b[200~one\ntwo\x1b[201~", "\r"}, sendTextWrites(t, true, "one\rtwo"))
}

// F7: a long single line is pasted too, whole, not typed as 10 KB of keystrokes.
func TestPTYSendTextPastesLongText(t *testing.T) {
	long := strings.Repeat("0123456789", 1000)
	got := sendTextWrites(t, true, long)
	require.Len(t, got, 2)
	assert.Equal(t, "\x1b[200~"+long+"\x1b[201~", got[0])
	assert.Equal(t, "\r", got[1])
}

// An application that did not ask for bracketed paste is given a CR for each break, as tmux's paste-buffer gives it.
func TestPTYSendTextWithoutBracketedPasteUsesCR(t *testing.T) {
	assert.Equal(t, []string{"alpha\rbeta", "\r"}, sendTextWrites(t, false, "alpha\r\nbeta"))
}

// Short single-line text is typed, as before.
func TestPTYSendTextTypesShortText(t *testing.T) {
	assert.Equal(t, []string{"hello", "\r"}, sendTextWritesTyped(t, "hello"))
}

func sendTextWritesTyped(t *testing.T, text string) []string {
	t.Helper()
	b, s, proc := newFakeBackend(t, "claude-7-typed")
	s.bracketedPaste.Store(true) // asked for, and still not used for a short line
	require.NoError(t, b.SendText(context.Background(), terminalinput.Unclassified, "claude-7-typed", text))
	var got []string
	for _, w := range proc.written() {
		got = append(got, string(w))
	}
	return got
}

// The text can't end the paste early or inject input: ESC and other control characters are dropped.
func TestPTYSendTextCannotEndThePasteEarly(t *testing.T) {
	got := sendTextWrites(t, true, "a\x1b[201~b\nc\x03")
	require.Len(t, got, 2)
	assert.Equal(t, "\x1b[200~a[201~b\nc\x1b[201~", got[0])
}

// The Enter of a pasted send obeys the gate's last-moment check.
func TestPTYSendTextWithholdsEnterWhenAPromptOpened(t *testing.T) {
	withFastSubmit(t)
	b, s, proc := newFakeBackend(t, "claude-7-withheld")
	s.bracketedPaste.Store(true)
	ctx := tmux.WithBeforeEnter(context.Background(), func(context.Context) bool { return false })
	err := b.SendText(ctx, terminalinput.Unclassified, "claude-7-withheld", "a\nb")
	assert.True(t, errors.Is(err, tmux.ErrEnterWithheld), "%v", err)
	w := proc.written()
	require.Len(t, w, 1, "the paste went, the Enter did not")
	assert.NotContains(t, string(w[0]), "\r")
}

func TestPTYSendTextToAGoneSessionIsTerminalGone(t *testing.T) {
	err := NewPTYBackend().SendText(context.Background(), terminalinput.Unclassified, "claude-gone", "a\nb")
	assert.True(t, errors.Is(err, ErrTerminalGone), "%v", err)
}
