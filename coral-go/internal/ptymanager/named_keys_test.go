package ptymanager

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #17 (audit F2): the key names Coral and its docs send become the bytes a terminal sends; the PTY used to type the
// names ("Enter", "Escape", "DownEnter") into the agent.
func TestKeyBytesForEveryNameCoralSends(t *testing.T) {
	for name, want := range map[string]string{
		"Enter": "\r", "Escape": "\x1b", "Tab": "\t", "BTab": "\x1b[Z", "BSpace": "\x7f", "Space": " ",
		"Up": "\x1b[A", "Down": "\x1b[B", "Right": "\x1b[C", "Left": "\x1b[D", "Home": "\x1b[H", "End": "\x1b[F",
		"PageUp": "\x1b[5~", "PageDown": "\x1b[6~", "DC": "\x1b[3~", "F1": "\x1bOP", "F5": "\x1b[15~",
		"C-c": "\x03", "C-u": "\x15", "C-a": "\x01", "C-z": "\x1a", "C-Space": "\x00", "M-x": "\x1bx",
		"0": "0", "1": "1", "10": "10", "y": "y",
	} {
		got, err := KeyBytes(name, false)
		require.NoError(t, err, name)
		assert.Equal(t, want, string(got), name)
	}
}

func TestKeyBytesRefusesWhatIsNotAKey(t *testing.T) {
	for _, name := range []string{"Enterr", "DownEnter", "enter", "ctrl-c", "hello", "", "C-", "F13", "M-xy"} {
		b, err := KeyBytes(name, false)
		assert.True(t, errors.Is(err, ErrUnknownKey), "%q: %v", name, err)
		assert.Nil(t, b, name)
	}
}

// F13: in application cursor-key mode the arrows (and Home, End) are SS3, as tmux sends them.
func TestKeyBytesFollowsTheCursorKeyMode(t *testing.T) {
	for name, want := range map[string]string{"Up": "\x1bOA", "Down": "\x1bOB", "Right": "\x1bOC", "Left": "\x1bOD", "Home": "\x1bOH", "End": "\x1bOF", "PageUp": "\x1b[5~", "Enter": "\r"} {
		got, err := KeyBytes(name, true)
		require.NoError(t, err)
		assert.Equal(t, want, string(got), name)
	}
}

func TestTrackCursorKeysFollowsTheLastModeSequence(t *testing.T) {
	s := &session{}
	for i, step := range []struct {
		chunks []string
		want   bool
	}{
		{[]string{"hello"}, false},
		{[]string{"\x1b[?1h"}, true},
		{[]string{"text", "\x1b[?1l", "more"}, false},
		{[]string{"\x1b[?1h\x1b[?1l"}, false},
		{[]string{"\x1b[?1l\x1b[?1h"}, true},
		{[]string{"\x1b[?25l"}, true},         // another mode: no change
		{[]string{"\x1b[?2004l"}, true},       // bracketed paste is not DECCKM
		{[]string{"\x1b[?1049;1l"}, false},    // 1 among several modes
		{[]string{"\x1b[?10", "49;1h"}, true}, // split across reads
		{[]string{"\x1b[?", "1l"}, false},
		{[]string{"\x1b[?11h"}, false}, // mode 11 is not mode 1
	} {
		for _, c := range step.chunks {
			s.trackCursorKeys([]byte(c))
		}
		assert.Equal(t, step.want, s.cursorKeysApp.Load(), "step %d", i)
	}
}

func rawKeysTerm(t *testing.T, name string) (*PTYSessionTerminal, *session, *fakePTYProcess) {
	t.Helper()
	b, s, proc := newFakeBackend(t, name)
	return NewPTYSessionTerminal(b), s, proc
}

// SendRawInput types key bytes, one write per key, and nothing at all when any entry is not a key.
func TestPTYSendRawInputSendsKeyBytes(t *testing.T) {
	setWaits(t, func(w *waitTimings) { w.rawKeyGap = 1 })
	name := "claude-17-keys"
	term, _, proc := rawKeysTerm(t, name)
	require.NoError(t, term.SendRawInput(context.Background(), name, []string{"Escape", "Escape", "Down", "Enter", "0"}, "claude", ""))
	var got []string
	for _, w := range proc.written() {
		got = append(got, string(w))
	}
	assert.Equal(t, []string{"\x1b", "\x1b", "\x1b[B", "\r", "0"}, got)

	before := len(proc.written())
	err := term.SendRawInput(context.Background(), name, []string{"Enter", "Enterr"}, "claude", "")
	assert.True(t, errors.Is(err, ErrUnknownKey), "%v", err)
	assert.Len(t, proc.written(), before, "an unknown name stops the request before the valid keys before it are sent")
	assert.Error(t, term.ValidateKeys([]string{"Down", "DownEnter"}))
	assert.NoError(t, term.ValidateKeys([]string{"Down", "Enter"}))
}

func TestPTYArrowFollowsTheProgramsCursorKeyMode(t *testing.T) {
	name := "agy-13-arrow"
	term, s, proc := rawKeysTerm(t, name)
	require.NoError(t, term.SendArrow(context.Background(), name, "agy", "", true))
	s.trackCursorKeys([]byte("\x1b[?1h"))
	require.NoError(t, term.SendArrow(context.Background(), name, "agy", "", false))
	w := proc.written()
	require.Len(t, w, 2)
	assert.Equal(t, "\x1b[A", string(w[0]))
	assert.Equal(t, "\x1bOB", string(w[1]))
}

// F15: a gone session is ErrTerminalGone everywhere, not an empty screen or a bare error.
func TestAGonePTYSessionIsTerminalGone(t *testing.T) {
	b := NewPTYBackend()
	term := NewPTYSessionTerminal(b)
	_, err := term.RenderedScreen(context.Background(), "claude-gone", "claude", "00000000-0000-4000-8000-00000000f015")
	assert.True(t, errors.Is(err, ErrTerminalGone), "RenderedScreen: %v", err)
	assert.True(t, errors.Is(b.SendPrompt(context.Background(), "claude-gone", "x"), ErrTerminalGone), "SendPrompt of a session that isn't there")
	assert.True(t, errors.Is(b.SendInput("claude-gone", []byte("x")), ErrTerminalGone))
	assert.True(t, errors.Is(term.SendRawInput(context.Background(), "claude-gone", []string{"Enter"}, "claude", "00000000-0000-4000-8000-00000000f015"), ErrTerminalGone))
}
