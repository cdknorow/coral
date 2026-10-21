package ptymanager

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/terminalinput"
	"github.com/subgentic/coral/internal/tmux"
)

// crlf: the screens the tests feed are drawn as a terminal receives them.
func crlf(s string) string { return strings.ReplaceAll(s, "\n", "\r\n") }

func ptyGateTerm(t *testing.T, name, screen string) (*PTYSessionTerminal, *fakePTYProcess) {
	t.Helper()
	b, s, proc := newFakeBackend(t, name)
	s.ringMax = 1 << 16
	s.ring = []byte(crlf(screen))
	s.cols, s.rows = 100, 30
	s.bracketedPaste.Store(true)
	return NewPTYSessionTerminal(b), proc
}

// F3: the gate treats the PTY's screen as rendered, so it reads the input box.
func TestPTYTerminalHasARenderedScreenForTheGate(t *testing.T) {
	var term any = NewPTYSessionTerminal(NewPTYBackend())
	rs, ok := term.(RenderedScreen)
	require.True(t, ok, "the gate asks Term for RenderedScreen")
	assert.True(t, rs.HasRenderedScreen())
}

// F3: a person's draft in the box holds an automated paste: nothing is written over it.
func TestPTYGateDoesNotPasteOverAPersonsDraft(t *testing.T) {
	withFastSubmit(t)
	name := "claude-3-draft"
	term, proc := ptyGateTerm(t, name, claudeBoxScreen("xyz", 100))
	in := AutomatedInput{
		Term: term, Name: name, AgentType: "claude", Key: "k", Text: "You have a new task", What: "test", Paste: true,
		Send: func(ctx context.Context) error {
			return term.SendPrompt(ctx, terminalinput.Unclassified, name, "You have a new task", "claude", "")
		},
	}
	d, err := DeliverAutomated(context.Background(), in)
	require.NoError(t, err)
	assert.Equal(t, Held, d, "a draft in the box holds the send")
	assert.Empty(t, proc.written(), "nothing is pasted onto the draft, and no Enter is pressed")
	DropHeld(name, "test")
}

// F4: a prompt that opens between a typed send's text and its Enter withholds the Enter.
func TestPTYTypedSendWithholdsEnterWhenAPromptOpened(t *testing.T) {
	name := "claude-4-enter"
	term, proc := ptyGateTerm(t, name, claudeBoxScreen("", 100))
	ctx := tmux.WithBeforeEnter(context.Background(), func(context.Context) bool { return false })
	err := term.SendInput(ctx, terminalinput.Unclassified, name, "hello", "claude", "")
	assert.True(t, errors.Is(err, tmux.ErrEnterWithheld), "got %v", err)
	w := proc.written()
	require.Len(t, w, 1, "the text was typed")
	assert.Equal(t, "hello", string(w[0]))
	for _, x := range w {
		assert.False(t, bytes.Contains(x, []byte("\r")), "no Enter was written")
	}
}

// F10: the PTY's styled screen marks Claude's faint suggestion, so it is not a person's draft.
func TestPTYStyledScreenMarksAFaintSuggestion(t *testing.T) {
	rule := strings.Repeat("─", 80)
	faint := rule + "\n❯ \x1b[2mcoral-board read\x1b[22m\n" + rule + "\n"
	typed := rule + "\n❯ xyz\n" + rule + "\n"
	name := "claude-10-faint"
	term, _ := ptyGateTerm(t, name, faint)
	var ss StyledScreenReader = term
	styled, err := ss.AgentScreenStyled(context.Background(), name, "claude", "")
	require.NoError(t, err)
	assert.True(t, suggestionOnly(styled), "styled screen: %q", styled)
	term2, _ := ptyGateTerm(t, "claude-10-typed", typed)
	styled2, err := term2.AgentScreenStyled(context.Background(), "claude-10-typed", "claude", "")
	require.NoError(t, err)
	assert.False(t, suggestionOnly(styled2), "text a person typed is not a suggestion: %q", styled2)
}

// An empty render is not replaced by the raw output's tail: the gate holds on it.
func TestPTYAgentScreenIsNeverTheRawTail(t *testing.T) {
	name := "claude-6-empty"
	term, _ := ptyGateTerm(t, name, "\x1b[2J") // nothing on screen
	screen, err := term.AgentScreen(context.Background(), name, "claude", "")
	require.NoError(t, err)
	assert.Empty(t, strings.TrimSpace(screen))
}

// recTerm records the order of the gate's screen reads and Enters.
type recTerm struct {
	*PTYSessionTerminal
	mu  sync.Mutex
	log []string
}

func (r *recTerm) note(s string) { r.mu.Lock(); r.log = append(r.log, s); r.mu.Unlock() }
func (r *recTerm) AgentScreen(ctx context.Context, n, a, s string) (string, error) {
	r.note("screen")
	return r.PTYSessionTerminal.AgentScreen(ctx, n, a, s)
}
func (r *recTerm) SendEnter(ctx context.Context, n, a, s string) error {
	r.note("enter")
	return r.PTYSessionTerminal.SendEnter(ctx, n, a, s)
}

// A held Enter (the Enter of a typed send withheld because a prompt opened) is always preceded by the gate
// reading the screen again, and is not sent while a prompt is open.
func TestHeldEnterIsRetriedOnlyAfterTheGateReadsTheScreen(t *testing.T) {
	setWaits(t, func(w *waitTimings) { w.promptSubmitDelay = time.Millisecond })
	name := "claude-7-heldenter"
	base, proc := ptyGateTerm(t, name, " Do you want to proceed?\n ❯ 1. Yes\n   2. No\n")
	term := &recTerm{PTYSessionTerminal: base}
	in := AutomatedInput{Term: term, Name: name, AgentType: "claude", Key: "k", Text: "hello", What: "test"}
	gate.holdEnter(in, kindPrompt)
	t.Cleanup(func() { DropHeld(name, "test") })

	gate.flush(context.Background(), name)
	assert.Empty(t, proc.written(), "no Enter while the prompt is open")
	assert.NotContains(t, term.log, "enter")

	b, ok := base.backend.sessions[name]
	require.True(t, ok)
	b.ringMax = 1 << 16
	b.ring = []byte(crlf(claudeBoxScreen("hello", 100)))
	term.mu.Lock()
	term.log = nil
	term.mu.Unlock()
	gate.flush(context.Background(), name)
	require.Contains(t, term.log, "enter", "the Enter goes once the prompt is gone")
	assert.Equal(t, "screen", term.log[0], "the screen was read before the Enter")
	for i, e := range term.log {
		if e == "enter" {
			assert.Equal(t, "screen", term.log[i-1], "every Enter directly follows a screen read: %v", term.log)
		}
	}
}
