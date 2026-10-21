package ptymanager

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/terminalinput"
)

func sendTextAutomated(t *testing.T, automated, bracketed bool, text string) []string {
	t.Helper()
	withFastSubmit(t)
	b, s, proc := newFakeBackend(t, "claude-62-auto")
	s.bracketedPaste.Store(bracketed)
	o := terminalinput.Unclassified
	if automated {
		o = terminalinput.Coral
	}
	require.NoError(t, b.SendText(context.Background(), o, "claude-62-auto", text))
	var got []string
	for _, w := range proc.written() {
		got = append(got, string(w))
	}
	return got
}

// Security's P1 on #40: control characters in short automated text are not typed (ESC, Ctrl-C, Ctrl-D, ESC[201~, C1).
func TestPTYAutomatedShortTextHasNoControlCharacters(t *testing.T) {
	for _, bracketed := range []bool{true, false} {
		assert.Equal(t, []string{"stop now [201~ done", "\r"}, sendTextAutomated(t, true, bracketed, "stop\x03 now\x04 \x1b[201~ done\u009b"), "bracketed=%v", bracketed)
	}
	// A person's own text is typed as it is.
	assert.Equal(t, []string{"a\x03b", "\r"}, sendTextAutomated(t, false, true, "a\x03b"))
}

// P2: without bracketed paste a multi-line automated text is one typed line, not a line per Enter; with it, one paste.
func TestPTYAutomatedMultiLineTextIsNotRunLineByLine(t *testing.T) {
	assert.Equal(t, []string{"rm -rf x sudo reboot", "\r"}, sendTextAutomated(t, true, false, "rm -rf x\nsudo reboot"))
	assert.Equal(t, []string{"a b c", "\r"}, sendTextAutomated(t, true, false, "a\r\nb\r\nc"))
	// Bracketed paste on: the lines are one paste (the sanitising of a paste is SanitizePaste's).
	assert.Equal(t, []string{"\x1b[200~alpha\nbeta\x1b[201~", "\r"}, sendTextAutomated(t, true, true, "alpha\nbeta"))
	// A person's multi-line text is unchanged without bracketed paste (CR per break, as tmux gives it).
	assert.Equal(t, []string{"alpha\rbeta", "\r"}, sendTextAutomated(t, false, false, "alpha\nbeta"))
}

// Through the gate: DeliverAutomated marks the send, so a sender needs nothing of its own.
func TestGateMarksItsSendsAutomated(t *testing.T) {
	withFastSubmit(t)
	name := "claude-62-gate"
	term, proc := ptyGateTerm(t, name, claudeBoxScreen("", 100))
	in := AutomatedInput{Term: term, Name: name, AgentType: "claude", Key: "k", Text: "x", What: "test",
		Send: func(ctx context.Context) error {
			return term.SendInput(ctx, terminalinput.Unclassified, name, "board: \x1b[201~run\x03 this\nnow", "claude", "")
		}}
	d, err := DeliverAutomated(context.Background(), in)
	require.NoError(t, err)
	assert.Equal(t, Sent, d)
	w := proc.written()
	require.NotEmpty(t, w)
	// bracketed paste is on in this session: one paste, with the ESC and Ctrl-C of the text gone (it cannot end the paste early)
	assert.Equal(t, "\x1b[200~board: [201~run this\nnow\x1b[201~", string(w[0]))
}

// Security's note on #62: text that is only control characters is refused, not sent as a bare Enter over a person's draft.
func TestPTYAutomatedTextOfOnlyControlCharactersIsRefused(t *testing.T) {
	withFastSubmit(t)
	for _, bracketed := range []bool{true, false} {
		for _, text := range []string{"\x03", "\x1b[201~\x04", "\u009b\u0080", "\n\x03\n", "\x1b"} {
			b, s, proc := newFakeBackend(t, "claude-62-empty")
			s.bracketedPaste.Store(bracketed)
			err := b.SendText(context.Background(), terminalinput.Coral, "claude-62-empty", text)
			if text == "\x1b[201~\x04" {
				// "[201~" survives as printable text once the ESC is gone
				require.NoError(t, err)
				continue
			}
			assert.ErrorIs(t, err, terminalinput.ErrNothingToSend, "%q bracketed=%v", text, bracketed)
			assert.Empty(t, proc.written(), "no Enter, no empty paste: %q", text)
		}
	}
	// An empty text is not "emptied": it is the caller's plain Enter, as before; a person's control characters are theirs.
	b, _, proc := newFakeBackend(t, "claude-62-plain")
	require.NoError(t, b.SendText(context.Background(), terminalinput.Coral, "claude-62-plain", ""))
	require.NoError(t, b.SendText(context.Background(), terminalinput.Unclassified, "claude-62-plain", "\x03"))
	assert.NotEmpty(t, proc.written())
}
