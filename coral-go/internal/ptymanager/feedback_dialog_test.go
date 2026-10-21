package ptymanager

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/tmux"
)

// #4104: Claude Code's bug-report draft. The shape is that of a real capture (a bordered box
// directly above the input box, its "1 to review · 2 to send · 0 to dismiss" footer, blank lines
// and the box's rule between them); the text is neutral, not the capture's.
func dialogBox(width int) string {
	in := width - 4
	row := func(s string) string { return "│ " + s + strings.Repeat(" ", max(0, in-len([]rune(s)))) + " │\n" }
	return "╭" + strings.Repeat("─", width-2) + "╮\n" +
		row("✻ Bug report drafted: an example summary of what went wrong…") +
		row("│ - What happened: an example line of the draft's text") +
		row("│ that wraps onto a second line…") +
		row("1 to review · 2 to send · 0 to dismiss") +
		"╰" + strings.Repeat("─", width-2) + "╯\n"
}

// claudeScreen draws Claude's input box holding box, with the dialog above it when open, and
// scrollback above everything.
func claudeScreen(scrollback, box string, dialog bool) string {
	rule := strings.Repeat("─", 80)
	var b strings.Builder
	b.WriteString(scrollback)
	if dialog {
		b.WriteString(dialogBox(80) + "\n\n")
	}
	b.WriteString(rule + "\n❯\u00a0" + box + "\n" + rule + "\n  ⏵⏵ bypass permissions on (shift+tab to cycle)\n")
	return b.String()
}

const neutralScrollback = "⏺ Done: the tests pass.\n\n✻ Worked for 12s\n\n"

func TestFeedbackDialogIsADialogOnlyDirectlyAboveTheInputBox(t *testing.T) {
	open, why := PromptOnScreen(claudeScreen(neutralScrollback, "", true))
	require.True(t, open)
	assert.True(t, isDialogHold(why), why)
	assert.Equal(t, KindDialog, promptKind(why))
	assert.False(t, InputBoxOnScreen(claudeScreen(neutralScrollback, "", true)), "the box isn't ready while the dialog is up")

	// The words in the scrollback (an agent quoting them, Orchestrator's #4104 condition), or the
	// box higher up with output between it and the input: not a dialog.
	quoted := "⏺ The box says \"Bug report drafted\" and \"1 to review · 2 to send · 0 to dismiss\".\n\n"
	open, _ = PromptOnScreen(claudeScreen(quoted, "", false))
	assert.False(t, open, "quoted in the scrollback")
	higher := dialogBox(80) + "\n⏺ Later output, after the box was answered.\n\n"
	open, _ = PromptOnScreen(claudeScreen(higher, "", false))
	assert.False(t, open, "a box higher up in the scrollback")
	open, _ = PromptOnScreen(claudeScreen(neutralScrollback, "", false))
	assert.False(t, open, "no box")
	assert.True(t, InputBoxOnScreen(claudeScreen(neutralScrollback, "", false)))

	// The session survey (a plain choices line, no border) is the same kind of dialog.
	open, why = PromptOnScreen(claudeScreen(neutralScrollback+surveyLines, "", false))
	require.True(t, open, "the survey")
	assert.True(t, isDialogHold(why), why)
	// An agent's own numbered list right above the input, with no dismiss choice: not a dialog.
	list := "⏺ Pick one:\n  1: rebase    2: merge    3: squash\n\n"
	open, _ = PromptOnScreen(claudeScreen(list, "", false))
	assert.False(t, open, "numbered choices without a dismiss")
	// The survey's words quoted in an agent's reply, right above the input: not a dialog.
	replyQuote := "⏺ It showed:\n  1: Bad    2: Fine   3: Good   0: Dismiss\n\n"
	open, _ = PromptOnScreen(claudeScreen(replyQuote, "", false))
	assert.False(t, open, "the survey quoted in a reply")
}

// dialogTerm is Claude with the bug-report draft: while the dialog is up a paste still lands in
// the input box (as in the capture) but an Enter does nothing (the stall). With the dialog
// closed an Enter submits the box. It records what was submitted, and every paste and Enter
// made while the dialog was up.
type dialogTerm struct {
	mu            sync.Mutex
	dialog        bool
	box           string
	swallowEnters int // Enters to ignore even with the dialog closed
	pasted        []string
	enters        int
	submitted     []string
	whileDialog   int  // pastes and Enters sent while the dialog was on screen
	survey        bool // the dialog drawn is the session survey, not the bug-report box
}

func (d *dialogTerm) AgentScreen(context.Context, string, string, string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.dialog && d.survey {
		return claudeScreen(neutralScrollback+surveyLines, d.box, false), nil
	}
	return claudeScreen(neutralScrollback, d.box, d.dialog), nil
}

// surveyLines is Claude Code's session survey as it is drawn above the input box (a capture's
// layout, neutral text).
const surveyLines = "● How is Claude doing this session? (optional)\n  1: Bad    2: Fine   3: Good   0: Dismiss\n\n"

func (d *dialogTerm) HasRenderedScreen() bool { return true }
func (d *dialogTerm) SendPrompt(ctx context.Context, _, text, _, _ string) error {
	d.mu.Lock()
	d.pasted = append(d.pasted, text)
	if d.dialog {
		d.whileDialog++
	}
	d.box = text
	d.mu.Unlock()
	if !tmux.EnterAllowed(ctx) {
		return tmux.ErrEnterWithheld
	}
	return d.SendEnter(ctx, "", "", "")
}
func (d *dialogTerm) SendEnter(context.Context, string, string, string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.enters++
	if d.dialog {
		d.whileDialog++
		return nil // the dialog has it: the text stays in the box
	}
	if d.swallowEnters > 0 {
		d.swallowEnters--
		return nil
	}
	if d.box != "" {
		d.submitted = append(d.submitted, d.box)
		d.box = ""
	}
	return nil
}
func (d *dialogTerm) set(f func(d *dialogTerm)) {
	d.mu.Lock()
	f(d)
	d.mu.Unlock()
}
func (d *dialogTerm) state() (pasted, submitted []string, enters, whileDialog int, box string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.pasted...), append([]string(nil), d.submitted...), d.enters, d.whileDialog, d.box
}

func dialogInput(term *dialogTerm, name, key, text string, mention bool) AutomatedInput {
	return AutomatedInput{Term: term, Name: name, AgentType: "claude", SessionID: strings.TrimPrefix(name, "claude-"),
		Key: key, Text: text, What: "board nudge", Paste: true, Mention: mention,
		Send: func(context.Context) error { return nil }}
}

func dialogGate(t *testing.T, giveUp time.Duration) *gaveUpLog {
	t.Helper()
	g := stuckGate(t, time.Hour, time.Hour)
	prev := gate.setTimings(gateTimings{tooLong: time.Hour, expiry: 24 * time.Hour, every: 20 * time.Millisecond,
		dialogReport: 30 * time.Millisecond, dialogMentionGiveUp: giveUp})
	t.Cleanup(func() { gate.setTimings(prev) })
	return g
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The operator's case: notices arrive while the draft box is up. Nothing is pasted or pressed
// while it is there; once it closes each is submitted exactly once.
func TestSendsWaitBehindTheDialogAndGoOnceItCloses(t *testing.T) {
	dialogGate(t, time.Hour)
	ctx := context.Background()
	term := &dialogTerm{dialog: true}
	name := "claude-dialog-held"
	t.Cleanup(func() { DropHeld(name, "test") })

	d, err := DeliverAutomated(ctx, dialogInput(term, name, "board-unread:team", "You have 1 unread message on the message board.", false))
	require.NoError(t, err)
	assert.Equal(t, Held, d)
	d, err = DeliverAutomated(ctx, dialogInput(term, name, "health", "Health check: post a short status.", false))
	require.NoError(t, err)
	assert.Equal(t, Held, d)
	time.Sleep(150 * time.Millisecond) // several retries with the dialog still up
	pasted, _, enters, whileDialog, _ := term.state()
	assert.Empty(t, pasted, "nothing pasted while the dialog is up")
	assert.Zero(t, enters)
	assert.Zero(t, whileDialog, "nothing typed into the dialog")

	term.set(func(d *dialogTerm) { d.dialog = false })
	waitFor(t, "both delivered", func() bool { _, s, _, _, _ := term.state(); return len(s) == 2 })
	pasted, submitted, _, whileDialog, _ := term.state()
	assert.Equal(t, []string{"You have 1 unread message on the message board.", "Health check: post a short status."}, submitted)
	assert.Len(t, pasted, 2, "each pasted once")
	assert.Zero(t, whileDialog)
}

// An Enter that doesn't submit (the dialog appeared just after the paste, or took the Enter)
// leaves the send held as the same record: on its next turn only the Enter is pressed, never a
// second paste (Security's #4104 note).
func TestAnEnterThatDoesNotSubmitIsRetriedWithoutPastingAgain(t *testing.T) {
	dialogGate(t, time.Hour)
	ctx := context.Background()
	term := &dialogTerm{swallowEnters: 1}
	name := "claude-dialog-enter"
	t.Cleanup(func() { DropHeld(name, "test") })
	text := "You have 2 unread messages on the message board."

	d, err := DeliverAutomated(ctx, dialogInput(term, name, "board-unread:team", text, false))
	require.NoError(t, err)
	assert.Equal(t, Held, d, "its Enter didn't submit it: not delivered")
	waitFor(t, "submitted on the retry", func() bool { _, s, _, _, _ := term.state(); return len(s) == 1 })
	pasted, submitted, enters, _, _ := term.state()
	assert.Equal(t, []string{text}, pasted, "pasted once")
	assert.Equal(t, []string{text}, submitted)
	assert.Equal(t, 2, enters, "the first Enter, then only the Enter again")
}

// The Enter that took effect after the gate looked: the box is empty on the retry, so the send
// is delivered and never pasted again.
func TestALateSubmitIsNotPastedAgain(t *testing.T) {
	dialogGate(t, time.Hour)
	ctx := context.Background()
	term := &dialogTerm{swallowEnters: 1}
	name := "claude-dialog-late"
	t.Cleanup(func() { DropHeld(name, "test") })
	d, err := DeliverAutomated(ctx, dialogInput(term, name, "k", "A notice whose Enter lands late.", false))
	require.NoError(t, err)
	require.Equal(t, Held, d)
	term.set(func(d *dialogTerm) { d.submitted, d.box = append(d.submitted, d.box), "" }) // it went through after all
	// The held send is released on its retry: wait for that event (the record leaves the held set), not a fixed
	// time, so a slow machine cannot read the state before the retry ran. A second paste would come before the release.
	waitFor(t, "the held send released on its retry", func() bool { return HeldCount(name) == 0 })
	pasted, submitted, _, _, _ := term.state()
	assert.Len(t, pasted, 1, "never pasted again")
	assert.Len(t, submitted, 1)
}

// Coral's own earlier paste left in the box (its Enter went to the dialog) is Coral's, not a
// draft: its Enter is pressed first, then the new send goes. A person's edit of it is a draft:
// nothing is pressed or pasted.
func TestCoralsLeftoverTextIsSentNotTakenForADraft(t *testing.T) {
	dialogGate(t, time.Hour)
	ctx := context.Background()
	name := "claude-dialog-leftover"
	t.Cleanup(func() { DropHeld(name, "test") })
	old := "You have 1 unread message on the message board."
	gate.setLastPasted(name, old)
	term := &dialogTerm{box: old}

	d, err := DeliverAutomated(ctx, dialogInput(term, name, "board-unread:team", "You have 2 unread messages on the message board.", false))
	require.NoError(t, err)
	assert.Equal(t, Sent, d)
	_, submitted, _, _, _ := term.state()
	assert.Equal(t, []string{old, "You have 2 unread messages on the message board."}, submitted)

	// A person edited it: their draft, left alone.
	name2 := "claude-dialog-draft"
	t.Cleanup(func() { DropHeld(name2, "test") })
	gate.setLastPasted(name2, old)
	person := &dialogTerm{box: old + " and my own words"}
	d, err = DeliverAutomated(ctx, dialogInput(person, name2, "board-unread:team", "You have 3 unread messages on the message board.", false))
	require.NoError(t, err)
	assert.Equal(t, Held, d)
	pasted, submitted, enters, _, box := person.state()
	assert.Empty(t, pasted)
	assert.Empty(t, submitted)
	assert.Zero(t, enters)
	assert.Equal(t, old+" and my own words", box)
}

// A mention isn't kept behind a dialog for ever: it is given up (never pasted, never typed into
// the dialog) and reported with why; a routine notice keeps waiting.
func TestAMentionBehindADialogIsGivenUp(t *testing.T) {
	gaveUp := dialogGate(t, 80*time.Millisecond)
	ctx := context.Background()
	term := &dialogTerm{dialog: true}
	name := "claude-dialog-mention"
	t.Cleanup(func() { DropHeld(name, "test") })

	_, err := DeliverAutomated(ctx, dialogInput(term, name, "board-unread:team", "You have 1 unread message.", false))
	require.NoError(t, err)
	_, err = DeliverAutomated(ctx, dialogInput(term, name, "", "[board] Lead: @Backend Dev please look", true))
	require.NoError(t, err)
	waitFor(t, "the mention given up", func() bool { return len(gaveUp.list()) == 1 })
	assert.Contains(t, gaveUp.list()[0], WhyDialogGaveUp)
	assert.Equal(t, 1, HeldCount(name), "the routine notice still waits")
	pasted, _, enters, whileDialog, _ := term.state()
	assert.Empty(t, pasted)
	assert.Zero(t, enters+whileDialog)
}

// The hold is reported quickly as a dialog hold, so the agent shows "delivery held: a dialog is
// open"; its notice stays up after a mention is given up (the dialog is still open).
func TestADialogHoldIsReportedAsADialog(t *testing.T) {
	dialogGate(t, 80*time.Millisecond)
	var mu sync.Mutex
	var kinds []string
	ended := 0
	SetHeldTooLongFunc(func(_, _, _, _, kind string) { mu.Lock(); kinds = append(kinds, kind); mu.Unlock() })
	SetHeldEndedFunc(func(_, _, _ string) { mu.Lock(); ended++; mu.Unlock() })
	term := &dialogTerm{dialog: true}
	name := "claude-dialog-report"
	t.Cleanup(func() { DropHeld(name, "test") })
	_, err := DeliverAutomated(context.Background(), dialogInput(term, name, "", "[board] Lead: @Backend Dev hi", true))
	require.NoError(t, err)
	waitFor(t, "reported", func() bool { mu.Lock(); defer mu.Unlock(); return len(kinds) > 0 })
	mu.Lock()
	assert.Equal(t, KindDialog, kinds[0])
	mu.Unlock()
	time.Sleep(250 * time.Millisecond) // the mention is given up meanwhile
	assert.Zero(t, HeldCount(name))
	mu.Lock()
	assert.Zero(t, ended, "the dialog's notice stays while the dialog is open")
	mu.Unlock()
}

// These dialogs act on single digit keys: a send starting with a digit ("2 unread messages", a
// task number) typed into the bug-report box could answer it, and "2 to send" sends a report.
// Nothing is pasted or pressed while either dialog is up (the Orchestrator's #4104 condition).
func TestADigitLeadingSendIsNeverTypedIntoADialog(t *testing.T) {
	dialogGate(t, time.Hour)
	ctx := context.Background()
	for _, survey := range []bool{false, true} {
		term := &dialogTerm{dialog: true, survey: survey}
		name := "claude-dialog-digit"
		if survey {
			name += "-survey"
		}
		t.Cleanup(func() { DropHeld(name, "test") })
		for _, text := range []string{"2 unread messages on the message board.", "0", "1 new task: #4104"} {
			d, err := DeliverAutomated(ctx, dialogInput(term, name, text, text, false))
			require.NoError(t, err)
			assert.Equal(t, Held, d, text)
		}
		time.Sleep(120 * time.Millisecond)
		pasted, submitted, enters, whileDialog, _ := term.state()
		assert.Empty(t, pasted, "survey=%v", survey)
		assert.Empty(t, submitted)
		assert.Zero(t, enters+whileDialog, "nothing typed into the dialog (survey=%v)", survey)
	}
}
