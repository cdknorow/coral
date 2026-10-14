package ptymanager

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/naming"
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
		assert.Equal(t, c.want, pasteLanded(c.screen, text), name)
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

func (n *noPasteTerm) SendPrompt(context.Context, string, string, string, string) error {
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
