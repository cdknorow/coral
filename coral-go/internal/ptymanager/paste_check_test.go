package ptymanager

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/terminalinput"
)

// Screens captured from the real CLIs (#3457 rig): whether a paste is in the input box.
func TestPasteLanded(t *testing.T) {
	const text = "Run this shell command with the Bash tool: echo hi-1-2-3 > out.txt"
	sep := "──────────────────────────────────────────────────"
	for name, c := range map[string]struct {
		screen string
		want   bool
	}{
		"claude, pasted":   {" ▝▝   ▝▝   /private/tmp/w\n" + sep + "\n❯ " + text + "\n" + sep + "\n  ⏸ manual mode on · ? for shortcuts\n", true},
		"claude, empty":    {sep + "\n❯ Try \"fix lint errors\"\n" + sep + "\n  ⏸ manual mode on · ? for shortcuts\n", false},
		"claude, long":     {sep + "\n❯ [Pasted text #1 +12 lines]\n" + sep + "\n", false}, // one line is never collapsed: a person's paste (v3)
		"codex, pasted":    {"• You have 1 usage limit reset available.\n› " + text + "\n  gpt-6-astra medium · /private/tmp/cx\n", true},
		"codex, empty":     {"› Ask Codex to do anything\n  gpt-6-astra medium · /private/tmp/cx\n", false},
		"agy, pasted":      {" ┃ esc to dismiss\n" + sep + "\n> " + text + "\n" + sep + "\n? for shortcuts\n", true},
		"wrapped":          {sep + "\n❯ Run this shell comm\n  and with the Bash tool: echo hi-1-2-3 > out.txt\n" + sep + "\n", true},
		"older nudge only": {"❯ " + text + "\n⏺ Done.\n" + sep + "\n❯ \n" + sep + "\n", false},
		"menu open":        {" Do you want to proceed?\n ❯ 1. Yes\n   2. No\n\n Esc to cancel · Tab to amend\n", false},
	} {
		assert.Equal(t, c.want, inputBoxState(c.screen, text, false) == boxOurs, name) // the box shows our text, with no placeholder before
	}
}

// Real Claude 2.1.299 screens (#3457 v2): a no-break space after the marker, which
// regexp's \s doesn't match; v1 never saw Claude's box and pasted every nudge twice.
func TestRealClaudeScreensBoxState(t *testing.T) {
	const nudge = "You have 1 unread message on the message board. Run 'coral-board read' to see them."
	rule := strings.Repeat("─", 120)
	footer := "  ⏸ manual mode on · ? for shortcuts · ← for agents"
	idle := rule + "\n❯\u00a0Try \"how do I log an error?\"\n" + rule + "\n" + footer + "\n"
	pasted := rule + "\n❯\u00a0" + nudge + "\n" + rule + "\n  ⏸ manual mode on\n"
	assert.True(t, InputBoxOnScreen(idle), "Claude's input box is recognised")
	assert.Equal(t, boxEmpty, inputBoxState(idle, nudge, false), "its placeholder is an empty box")
	assert.Equal(t, boxOurs, inputBoxState(pasted, nudge, false))
	assert.Equal(t, boxOther, inputBoxState(rule+"\n❯\u00a0my own draft\n"+rule+"\n", nudge, false))
	menu := " Do you want to proceed?\n ❯\u00a01. Yes\n   2. No\n\n Esc to cancel · Tab to amend\n"
	open, _ := PromptOnScreen(menu)
	assert.True(t, open)
	assert.Equal(t, boxUnknown, inputBoxState(menu, nudge, false), "a menu's highlighted option isn't an input box")
}

// #3457 v3: a "[Pasted ...]" placeholder is ours only when our text could have been
// collapsed into one and none was in the box before Coral pasted.
func TestPastePlaceholderOnlyWhenItCanBeOurs(t *testing.T) {
	rule := strings.Repeat("─", 60)
	withPlaceholder := rule + "\n❯\u00a0[Pasted text #1 +12 lines]\n" + rule + "\n"
	short := "You have 1 unread message on the message board."
	long := "Task brief:\nline 2\nline 3"
	assert.Equal(t, boxOther, inputBoxState(withPlaceholder, short, false), "a one-line nudge is never shown as a placeholder: a person's paste")
	assert.Equal(t, boxOurs, inputBoxState(withPlaceholder, long, false), "a multi-line send can be")
	assert.Equal(t, boxOther, inputBoxState(withPlaceholder, long, true), "a placeholder that was there before Coral's paste is a person's")
	assert.True(t, hasPlaceholder(withPlaceholder))
	assert.False(t, hasPlaceholder(rule+"\n❯\u00a0draft\n"+rule+"\n"))
	assert.Equal(t, boxEmpty, inputBoxState(rule+"\n> Type your message or @path/to/file\n"+rule+"\n", short, false), "Gemini's placeholder")
}

func TestBracketedPasteFromLog(t *testing.T) {
	dir := t.TempDir()
	write := func(s string) {
		require.NoError(t, os.WriteFile(naming.LogFile(dir, "claude", "s1"), []byte(s), 0o644))
	}
	_, known := BracketedPasteFromLog(dir, "claude", "s1")
	assert.False(t, known, "no log")
	write("hello\x1b[?2004hworld")
	on, known := BracketedPasteFromLog(dir, "claude", "s1")
	assert.True(t, on && known)
	write("\x1b[?2004h...\x1b[?2004l")
	on, known = BracketedPasteFromLog(dir, "claude", "s1")
	assert.True(t, known)
	assert.False(t, on, "turned off last")
}

// paste terminal: a PromptSender whose agent has bracketed paste off.
type noPasteTerm struct{ typed []string }

func (n *noPasteTerm) SendPrompt(context.Context, terminalinput.Origin, string, string, string, string) error {
	return ErrBracketedPasteUnavailable
}
func (n *noPasteTerm) AgentScreen(context.Context, string, string, string) (string, error) {
	return "❯ \n", nil
}

// Where the agent hasn't turned bracketed paste on, the send is typed, as before.
func TestPasteFallsBackToTypingWithoutBracketedPaste(t *testing.T) {
	fastGate(t)
	term := &noPasteTerm{}
	d, err := DeliverAutomated(context.Background(), AutomatedInput{Term: term, Name: "claude-fallback", Text: "hi", What: "nudge", Paste: true,
		Send: func(context.Context) error { term.typed = append(term.typed, "hi"); return nil }})
	require.NoError(t, err)
	assert.Equal(t, Sent, d)
	assert.Equal(t, []string{"hi"}, term.typed)
}

// identity reads the agent type and session id from the session name when not given.
func TestAutomatedInputIdentity(t *testing.T) {
	at, sid := AutomatedInput{Name: "claude-0f1e2d3c-4b5a"}.identity()
	assert.Equal(t, "claude", at)
	assert.Equal(t, "0f1e2d3c-4b5a", sid)
	at, sid = AutomatedInput{Name: "x", AgentType: "codex", SessionID: "s"}.identity()
	assert.Equal(t, [2]string{"codex", "s"}, [2]string{at, sid})
}

// #3468: the input box on a styled capture holds only faint text. The first screen is
// QA's real Claude capture (ESC[2m after the marker and its no-break space); the
// others are capture-pane -e's way of writing styles (combined and carried across
// lines).
func TestSuggestionOnly(t *testing.T) {
	rule := "\x1b[38;5;244m" + strings.Repeat("─", 40) + "\n\x1b[39m"
	footer := "  \x1b[38;5;220m⏵⏵ auto mode on\x1b[38;5;246m (shift+tab to cycle)\x1b[39m\n"
	box := func(s string) string {
		return "\x1b[38;5;231m⏺\x1b[39m OK\n" + rule + "❯\u00a0" + s + "\n" + rule + footer
	}
	for name, c := range map[string]struct {
		screen string
		want   bool
	}{
		"QA's capture":              {box("\x1b[2mcoral-board read\x1b[0m"), true},
		"faint with a colour":       {box("\x1b[2;38;5;246mcoral-board read\x1b[39;22m"), true},
		"faint carried to the line": {"\x1b[2m" + strings.Repeat("─", 40) + "\n❯ fix the tests\x1b[0m\n" + rule, true},
		"empty box":                 {box(""), false},
		"a draft":                   {box("my own draft"), false},
		"a draft and ghost text":    {box("git reset \x1b[2mmain\x1b[0m"), false},
		"accepted (normal again)":   {box("\x1b[2m\x1b[22mcoral-board read"), false},
		"faint then normal":         {box("\x1b[2mcoral-board\x1b[0m read"), false},
		"bold is not faint":         {box("\x1b[1mcoral-board read\x1b[0m"), false},
		"a faint paste placeholder": {box("\x1b[2m[Pasted text #1 +12 lines]\x1b[0m"), false},
		"no input box":              {"\x1b[2mjust output\x1b[0m\n", false},
	} {
		assert.Equal(t, c.want, suggestionOnly(c.screen), name)
	}
	// A wrapped box: a faint first line and a normal one under it is someone's text.
	wrapped := rule + "❯\u00a0\x1b[2mcoral-board\x1b[0m\n  read the board\n" + rule
	assert.False(t, suggestionOnly(wrapped), "a normal wrapped line")
}

// fixtureTerm serves a fixed screen captured with capture-pane -e (AgentScreen gets it
// without the escape sequences, as the plain capture is) and records what is pasted
// or Entered.
type fixtureTerm struct {
	mu     sync.Mutex
	styled string
	pasted []string
	enters int
}

var sgrSeq = regexp.MustCompile(`\x1b\[[0-9;:?]*[A-Za-z]`)

func (f *fixtureTerm) AgentScreen(context.Context, string, string, string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return sgrSeq.ReplaceAllString(f.styled, ""), nil
}
func (f *fixtureTerm) AgentScreenStyled(context.Context, string, string, string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.styled, nil
}
func (f *fixtureTerm) HasRenderedScreen() bool { return true }
func (f *fixtureTerm) SendPrompt(_ context.Context, _ terminalinput.Origin, _, text, _, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pasted = append(f.pasted, text)
	return nil
}
func (f *fixtureTerm) SendEnter(context.Context, string, string, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enters++
	return nil
}
func (f *fixtureTerm) got() ([]string, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.pasted...), f.enters
}

// #3478 (QA's real agy 1.3.5 captures, testdata/agy_*.ansi): agy draws its box as a
// bright-blue ">" between grey rules. A person's draft there is held, never joined
// by Coral's paste; the empty box takes the paste.
func TestAnAgyDraftIsNeverAddedTo(t *testing.T) {
	fastGate(t)
	read := func(f string) string {
		b, err := os.ReadFile(filepath.Join("testdata", f))
		require.NoError(t, err)
		return string(b)
	}
	const nudge = "You have 1 unread message on the message board. Run 'coral-board read' to see them."
	draft, idle := read("agy_draft.ansi"), read("agy_idle_empty.ansi")
	plain := func(s string) string { return sgrSeq.ReplaceAllString(s, "") }
	assert.True(t, InputBoxKnown("agy"))
	assert.True(t, InputBoxOnScreen(plain(idle)), "agy's box is recognised")
	assert.Equal(t, boxOther, inputBoxState(plain(draft), nudge, false), "the draft is someone else's text")
	assert.Equal(t, boxEmpty, inputBoxState(plain(idle), nudge, false))
	assert.False(t, suggestionOnly(draft), "typed text is normal intensity")

	send := func(term *fixtureTerm, name, agentType string) Delivery {
		d, err := DeliverAutomated(context.Background(), AutomatedInput{Term: term, Name: name, AgentType: agentType,
			SessionID: "s-" + name, Text: nudge, What: "board nudge", Paste: true,
			Send: func(context.Context) error { t.Error("typed instead of pasted"); return nil }})
		require.NoError(t, err)
		return d
	}
	drafted := &fixtureTerm{styled: draft}
	assert.Equal(t, Held, send(drafted, "agy-draft", "agy"))
	time.Sleep(200 * time.Millisecond) // several of the gate's turns
	pasted, enters := drafted.got()
	assert.Empty(t, pasted, "nothing pasted onto the draft")
	assert.Zero(t, enters, "and nothing submitted")
	DropHeld("agy-draft", "test done")

	empty := &fixtureTerm{styled: idle}
	assert.Equal(t, Sent, send(empty, "agy-idle", "agy"))
	pasted, _ = empty.got()
	assert.Equal(t, []string{nudge}, pasted, "the empty box takes the paste")

	// Fail closed for an agent type whose box isn't listed: an input line holding
	// other text still holds the send.
	other := &fixtureTerm{styled: strings.Replace(draft, "my agy draft QA1", "half-typed question", 1)}
	assert.Equal(t, Held, send(other, "pi-draft", "pi"))
	pasted, _ = other.got()
	assert.Empty(t, pasted)
	DropHeld("pi-draft", "test done")
}
