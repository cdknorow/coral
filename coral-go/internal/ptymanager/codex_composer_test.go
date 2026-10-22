package ptymanager

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/terminalinput"
	"github.com/subgentic/coral/internal/tmux"
)

func codexCapture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	return string(b)
}

// #247: Codex's working line is read just above its input box (captures from Codex 0.161 on Windows): working there, not
// on the idle screen or the screen with Coral's nudge left in the box, and not for the same words further up (a reply).
func TestCodexWorkingLineIsReadOnlyAboveTheBox(t *testing.T) {
	line, busy := codexWorkingOnScreen(codexCapture(t, "codex_working.txt"))
	assert.True(t, busy)
	assert.Equal(t, "• Working (1s • esc to interrupt)", line)
	for _, f := range []string{"codex_idle.txt", "codex_leftover.txt"} {
		_, busy := codexWorkingOnScreen(codexCapture(t, f))
		assert.False(t, busy, f)
	}
	quoted := "• Working (3s • esc to interrupt)\n" + strings.Repeat("• a line of the reply\n", 10) + codexCapture(t, "codex_idle.txt")
	_, busy = codexWorkingOnScreen(quoted)
	assert.False(t, busy, "the words far above the box are output, not the status")
}

// codexTerm draws a Codex screen: the working line while busy, and its input box (the placeholder when empty).
type codexTerm struct {
	mu        sync.Mutex
	busy      bool
	streaming bool // the rendered screen as Codex streams a reply: output only, no working line, no box (#247's reproduction)
	box       string
	pasted    []string
	submitted []string
}

func (c *codexTerm) AgentScreen(context.Context, string, string, string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.streaming {
		return "  ## 6. Eddystone and the Challenge of Building Offshore\n\n  Few sites illustrate lighthouse engineering better than the Eddystone Rocks.\n  Henry Winstan\n\n\n", nil
	}
	s := "  Worked for 6s • 2:23 AM\n\n"
	if c.busy {
		s = "• Working (4s • esc to interrupt)\n\n"
	}
	box := c.box
	if box == "" {
		box = "Ask Codex to do anything"
	}
	return s + "› " + box + "\n\n  GPT-6.1-Sol default · D:\\work\\proj\n  ? for shortcuts\n", nil
}
func (c *codexTerm) HasRenderedScreen() bool { return true }
func (c *codexTerm) SendPrompt(ctx context.Context, _ terminalinput.Origin, _, text, _, _ string) error {
	c.mu.Lock()
	c.pasted = append(c.pasted, text)
	c.box = text
	c.mu.Unlock()
	if !tmux.EnterAllowed(ctx) {
		return tmux.ErrEnterWithheld
	}
	return c.SendEnter(ctx, "", "", "")
}
func (c *codexTerm) SendEnter(context.Context, string, string, string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.box != "" {
		c.submitted = append(c.submitted, c.box)
		c.box = ""
	}
	return nil
}
func (c *codexTerm) state() (pasted, submitted []string, box string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.pasted...), append([]string(nil), c.submitted...), c.box
}

func codexInput(term *codexTerm, name, key, text string, mention bool) AutomatedInput {
	return AutomatedInput{Term: term, Name: name, AgentType: "codex", SessionID: strings.TrimPrefix(name, "codex-"),
		Key: key, Text: text, What: "board nudge", Paste: true, Mention: mention,
		Send: func(context.Context) error { return nil }}
}

// Nothing is typed into a working Codex, a mention included (Codex keeps it, and the gate later took Coral's own text in its
// box for a draft): held as busy, and typed once its working line is gone.
func TestNothingIsTypedIntoAWorkingCodex(t *testing.T) {
	dialogGate(t, time.Hour)
	term := &codexTerm{busy: true}
	name := "codex-composer-busy"
	t.Cleanup(func() { DropHeld(name, "test") })
	d, err := DeliverAutomated(context.Background(), codexInput(term, name, "", "[board] Orchestrator: @Codex Tester please look", true))
	require.NoError(t, err)
	assert.Equal(t, Held, d)
	pasted, _, _ := term.state()
	assert.Empty(t, pasted, "not typed while Codex works")

	term.mu.Lock()
	term.busy = false
	term.mu.Unlock()
	waitFor(t, "the held mention typed once Codex is idle", func() bool { return HeldCount(name) == 0 })
	pasted, submitted, box := term.state()
	assert.Len(t, pasted, 1)
	assert.Equal(t, []string{"[board] Orchestrator: @Codex Tester please look"}, submitted)
	assert.Empty(t, box)
}

// The live case: Coral's own nudge, typed earlier and kept by Codex, is in its box when the next message comes. It was taken for
// a person's draft and everything behind it was held for two hours. Now it is recognised as Coral's: its Enter goes first, then
// the new message.
func TestCoralsOwnNudgeLeftInCodexsBoxIsSubmitted(t *testing.T) {
	dialogGate(t, time.Hour)
	name := "codex-composer-leftover"
	t.Cleanup(func() { DropHeld(name, "test"); gate.forgetOwnTexts(name) })
	old := "You have 1 unread message on the message board. Run 'coral-board read' to see them."
	gate.noteOwnText(name, old) // typed by the gate; its Enter was seen to empty the box, so lastPasted forgot it
	term := &codexTerm{box: old}

	d, err := DeliverAutomated(context.Background(), codexInput(term, name, "", "[Wait resolved] \"Orchestrator\" posted on 'team'.", false))
	require.NoError(t, err)
	assert.Equal(t, Sent, d)
	_, submitted, _ := term.state()
	assert.Equal(t, []string{old, "[Wait resolved] \"Orchestrator\" posted on 'team'."}, submitted)
}

// Several of Coral's texts piled up in the box (pasted while the box couldn't be read, as on the reproduction): still all
// Coral's, submitted together. A person's edit, or the texts out of order, is a draft: nothing pressed or pasted.
func TestCoralsStackedTextsAreItsOwnButAnEditIsADraft(t *testing.T) {
	dialogGate(t, time.Hour)
	a := "[Task #1 available] Run 'coral-board task claim 1' to claim it when your current task is done."
	b := "[Task #2 available] Run 'coral-board task claim 2' to claim it when your current task is done."
	next := "[Task #3 available] Run 'coral-board task claim 3' to claim it when your current task is done."

	name := "codex-composer-stacked"
	t.Cleanup(func() { DropHeld(name, "test"); gate.forgetOwnTexts(name) })
	gate.noteOwnText(name, a)
	gate.noteOwnText(name, b)
	term := &codexTerm{box: a + b}
	d, err := DeliverAutomated(context.Background(), codexInput(term, name, "", next, false))
	require.NoError(t, err)
	assert.Equal(t, Sent, d)
	_, submitted, _ := term.state()
	assert.Equal(t, []string{a + b, next}, submitted)

	for i, box := range []string{a + b + " and my own words", b + a} {
		name := "codex-composer-draft-" + string(rune('a'+i))
		t.Cleanup(func() { DropHeld(name, "test"); gate.forgetOwnTexts(name) })
		gate.noteOwnText(name, a)
		gate.noteOwnText(name, b)
		person := &codexTerm{box: box}
		d, err := DeliverAutomated(context.Background(), codexInput(person, name, "", next, false))
		require.NoError(t, err)
		assert.Equal(t, Held, d, box)
		pasted, submitted, left := person.state()
		assert.Empty(t, pasted, box)
		assert.Empty(t, submitted, box)
		assert.Equal(t, box, left)
	}
}

// Once Coral's leftover was submitted its texts are forgotten: the same words in the box later are a person's.
func TestCoralsTextsAreForgottenOnceSubmitted(t *testing.T) {
	dialogGate(t, time.Hour)
	name := "codex-composer-forget"
	t.Cleanup(func() { DropHeld(name, "test"); gate.forgetOwnTexts(name) })
	old := "You have 1 unread message on the message board. Run 'coral-board read' to see them."
	gate.noteOwnText(name, old)
	require.True(t, gate.ownLeftover(name, old))
	gate.forgetOwnTexts(name)
	assert.False(t, gate.ownLeftover(name, old))
}

// A text is Coral's for ownTextMaxAge; after that the same words are a draft. Spaces and line wraps don't matter.
func TestOwnTextsAgeOutAndIgnoreWrapping(t *testing.T) {
	dialogGate(t, time.Hour)
	name := "codex-composer-age"
	t.Cleanup(func() { gate.forgetOwnTexts(name) })
	now := time.Now()
	setNow(t, gate, func() time.Time { return now })
	gate.noteOwnText(name, "You have 2 unread messages on the message board.")
	assert.True(t, gate.ownLeftover(name, "You have 2 unread messages on the\n  message board."))
	now = now.Add(ownTextMaxAge + time.Minute)
	assert.False(t, gate.ownLeftover(name, "You have 2 unread messages on the message board."))
}

// Codex in its turn by its own signals: every send is held, a mention too, even when the screen shows an idle box (the screen
// rule alone would type it), and typed once the turn ends.
func TestCodexInTurnBySignalsHoldsEverySend(t *testing.T) {
	dialogGate(t, time.Hour)
	set := withState(t, AgentState{State: StateInTurn, Since: time.Now()}, true)
	term := &codexTerm{}
	name := "codex-composer-signals"
	t.Cleanup(func() { DropHeld(name, "test") })
	d, err := DeliverAutomated(context.Background(), codexInput(term, name, "", "[board] Orchestrator: @Codex Tester stop and read", true))
	require.NoError(t, err)
	assert.Equal(t, Held, d)
	pasted, _, _ := term.state()
	assert.Empty(t, pasted)

	set(AgentState{State: StateIdle, Since: time.Now()})
	waitFor(t, "typed once the turn ended", func() bool { return HeldCount(name) == 0 })
	_, submitted, _ := term.state()
	assert.Equal(t, []string{"[board] Orchestrator: @Codex Tester stop and read"}, submitted)
}

// With no input box on the rendered screen (Codex streaming its reply: the reproduction's capture), nothing is pasted blind:
// held, and pasted once the box shows again.
func TestNothingIsPastedBlindIntoCodex(t *testing.T) {
	dialogGate(t, time.Hour)
	term := &codexTerm{streaming: true}
	name := "codex-composer-blind"
	t.Cleanup(func() { DropHeld(name, "test") })
	d, err := DeliverAutomated(context.Background(), codexInput(term, name, "", "[Task #3 available] Run 'coral-board task claim 3' to claim it.", false))
	require.NoError(t, err)
	assert.Equal(t, Held, d)
	time.Sleep(100 * time.Millisecond) // a few retries of the loop
	pasted, _, _ := term.state()
	assert.Empty(t, pasted, "never pasted while the box can't be seen")

	term.mu.Lock()
	term.streaming = false
	term.mu.Unlock()
	waitFor(t, "pasted once the box shows", func() bool { return HeldCount(name) == 0 })
	pasted, submitted, _ := term.state()
	assert.Len(t, pasted, 1)
	assert.Len(t, submitted, 1)
}

// repaintTerm is a codexTerm whose rendered screen misses its box until it is asked to redraw (the reproduction after a long
// reply: the replay lost Codex's scroll region, and a one-column resize brought the box back).
type repaintTerm struct {
	codexTerm
	repaints int
}

func (r *repaintTerm) RepaintSession(context.Context, string, string, string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.repaints++
	r.streaming = false
	return nil
}

// An idle Codex whose box the rendered screen lost after a long reply: Coral asks it to redraw (once), and the held send then
// goes; it is never pasted blind meanwhile.
func TestAMissingCodexBoxIsRedrawnAndTheSendGoes(t *testing.T) {
	dialogGate(t, time.Hour)
	term := &repaintTerm{codexTerm: codexTerm{streaming: true}}
	name := "codex-composer-repaint"
	t.Cleanup(func() { DropHeld(name, "test") })
	in := codexInput(&term.codexTerm, name, "", "[Task #5 available] Run 'coral-board task claim 5'.", false)
	in.Term = term // the repainting terminal
	d, err := DeliverAutomated(context.Background(), in)
	require.NoError(t, err)
	assert.Equal(t, Held, d, "held, not pasted blind, until the box shows")
	waitFor(t, "delivered after the redraw", func() bool { return HeldCount(name) == 0 })
	term.mu.Lock()
	defer term.mu.Unlock()
	assert.Equal(t, 1, term.repaints, "asked to redraw once")
	assert.Equal(t, []string{"[Task #5 available] Run 'coral-board task claim 5'."}, term.submitted)
}

// #262 (found by #257's live run): when the rendered screen loses Codex's composer marker, the lowest "›" line left is a past
// prompt with Codex's reply under it. It is not the box: the box is unknown (so the gate holds and has Codex redraw), never a
// draft that holds every send. A real composer under the same history is still read.
func TestACodexHistoryLineIsNotTheBox(t *testing.T) {
	screen := codexCapture(t, "codex_history_echo.txt")
	_, ok := inputBoxText(screen)
	assert.False(t, ok, "a past prompt with Codex's reply below it is history, not the box")

	withBox := strings.TrimRight(screen, "\n") + "\n\n› Ask Codex to do anything\n\n  ? for shortcuts\n"
	box, ok := inputBoxText(withBox)
	require.True(t, ok)
	assert.Equal(t, boxEmpty, boxTextState(box, "[Task #9 available]", false), "the composer under the history: empty")
}

// historyTerm shows the live capture (the composer's marker lost, a past prompt above) until it is asked to redraw, then Codex's
// idle box.
type historyTerm struct {
	codexTerm
	screen   string
	repaints int
}

func (h *historyTerm) AgentScreen(ctx context.Context, name, agentType, sessionID string) (string, error) {
	h.mu.Lock()
	s := h.screen
	h.mu.Unlock()
	if s != "" {
		return s, nil
	}
	return h.codexTerm.AgentScreen(ctx, name, agentType, sessionID)
}

func (h *historyTerm) RepaintSession(context.Context, string, string, string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.repaints++
	h.screen = ""
	return nil
}

// Through the gate: the send is held (not as a draft), Codex is asked to redraw once, and the send goes.
func TestASendBehindACodexHistoryLineGoesAfterARedraw(t *testing.T) {
	dialogGate(t, time.Hour)
	term := &historyTerm{screen: codexCapture(t, "codex_history_echo.txt")}
	name := "codex-composer-history"
	t.Cleanup(func() { DropHeld(name, "test") })
	in := codexInput(&term.codexTerm, name, "", "You have 1 unread message on the message board. Run 'coral-board read' to see them.", false)
	in.Term = term
	d, err := DeliverAutomated(context.Background(), in)
	require.NoError(t, err)
	assert.Equal(t, Held, d)
	waitFor(t, "delivered after the redraw", func() bool { return HeldCount(name) == 0 })
	term.mu.Lock()
	defer term.mu.Unlock()
	assert.Equal(t, 1, term.repaints)
	assert.Equal(t, []string{"You have 1 unread message on the message board. Run 'coral-board read' to see them."}, term.submitted)
}

// Once Coral's text has been delivered (its Enter seen to empty the box), the same words typed later by a person are a draft: they are not Coral's leftover, so they are
// never submitted for it (QA's finding on #108: the gate forgot its texts only after a leftover submit).
func TestACodexTextDeliveredNormallyIsForgottenSoAPersonsIdenticalDraftIsHeld(t *testing.T) {
	dialogGate(t, time.Hour)
	term := &codexTerm{}
	name := "codex-composer-forget-normal"
	t.Cleanup(func() { DropHeld(name, "test"); gate.forgetOwnTexts(name) })
	text := "You have 1 unread message on the message board. Run 'coral-board read' to see them."

	d, err := DeliverAutomated(context.Background(), codexInput(term, name, "", text, false))
	require.NoError(t, err)
	assert.Equal(t, Sent, d)
	_, submitted, _ := term.state()
	require.Equal(t, []string{text}, submitted, "delivered through the box")
	assert.False(t, gate.ownLeftover(name, text), "the words are no longer taken for Coral's")

	// a person types the same words into the box: that is a draft, and the next send is held behind it, never submitted for Coral
	term.mu.Lock()
	term.box = text
	term.mu.Unlock()
	d, err = DeliverAutomated(context.Background(), codexInput(term, name, "", "[Task #9 available] Run 'coral-board task claim 9' to claim it.", false))
	require.NoError(t, err)
	assert.Equal(t, Held, d, "a person's draft holds the send")
	_, submitted, box := term.state()
	assert.Len(t, submitted, 1, "the draft was not submitted")
	assert.Equal(t, text, box, "and it is still in the box")
}

// forgetOwnText drops only the newest matching record: other texts of Coral's that may still be stacked in a box stay recognised.
func TestForgetOwnTextDropsOnlyThatText(t *testing.T) {
	dialogGate(t, time.Hour)
	name := "codex-composer-forget-one"
	t.Cleanup(func() { gate.forgetOwnTexts(name) })
	a, b := "first nudge text for the agent", "second nudge text for the agent"
	gate.noteOwnText(name, a)
	gate.noteOwnText(name, b)
	gate.forgetOwnText(name, a)
	assert.False(t, gate.ownLeftover(name, a))
	assert.True(t, gate.ownLeftover(name, b), "the other text is still Coral's")
	gate.forgetOwnText(name, "never typed")
	gate.forgetOwnText(name, "")
	assert.True(t, gate.ownLeftover(name, b))
}

// A typed send's text (its Enter is not confirmed from the screen) is remembered for two minutes, not six hours: after that the same words in the box are a person's.
func TestATypedSendsTextIsRememberedOnlyBriefly(t *testing.T) {
	dialogGate(t, time.Hour)
	name := "codex-composer-typed-brief"
	t.Cleanup(func() { gate.forgetOwnTexts(name) })
	now := time.Now()
	setNow(t, gate, func() time.Time { return now })
	typed, pasted := "typed nudge text for the agent box", "pasted nudge text for the agent box"
	gate.noteOwnTyped(name, typed)
	gate.noteOwnText(name, pasted)
	assert.True(t, gate.ownLeftover(name, typed))
	assert.True(t, gate.ownLeftover(name, pasted))
	now = now.Add(3 * time.Minute)
	assert.False(t, gate.ownLeftover(name, typed), "three minutes on, a typed send's words are a person's draft")
	assert.True(t, gate.ownLeftover(name, pasted), "a paste is remembered for hours")
	now = now.Add(7 * time.Hour)
	assert.False(t, gate.ownLeftover(name, pasted))
}

// A typed send (bracketed paste off) has no screen-confirmed Enter, so lastPasted must not outlive ownTypedMaxAge either: a person's
// identical draft typed minutes later is theirs (QA's live re-check of #112).
func TestTypedSendsLastPastedIsOwnOnlyForTwoMinutes(t *testing.T) {
	now := time.Now()
	g := &inputGate{}
	fn := func() time.Time { return now }
	g.nowFn.Store(&fn)
	text := "[Task #1 available] Run 'coral-board task claim 1' to claim it."

	g.noteOwnText("s", text)
	g.setLastPasted("s", text)
	g.markTyped("s", text)
	assert.True(t, g.ownLeftover("s", text), "right after the send")
	now = now.Add(ownTypedMaxAge + time.Second)
	assert.False(t, g.ownLeftover("s", text), "past the typed bound it is a person's draft")

	// a real paste keeps its record until the Enter is seen
	g.noteOwnText("p", text)
	g.setLastPasted("p", text)
	now = now.Add(time.Hour)
	assert.True(t, g.ownLeftover("p", text))
}

// typedTerm takes no bracketed paste (PromptSender says so, as the PTY backend and tmux do when the hint is off or unknown): the gate types.
type typedTerm struct{ codexTerm }

func (c *typedTerm) SendPrompt(context.Context, terminalinput.Origin, string, string, string, string) error {
	return ErrBracketedPasteUnavailable
}

// The path QA's live run took: a board notice (no paste hint) goes to a terminal that can't take a paste, so it is typed, and its Enter is never seen to empty
// the box. Coral's record of it must lapse after ownTypedMaxAge, so a person's identical draft is theirs.
func TestATypedFallbackSendIsCoralsOwnOnlyForTwoMinutes(t *testing.T) {
	dialogGate(t, time.Hour)
	name := "codex-composer-typed-fallback"
	text := "You have 1 unread message on the message board. Run 'coral-board read' to see them."
	t.Cleanup(func() {
		DropHeld(name, "test")
		gate.forgetOwnTexts(name)
		gate.setLastPasted(name, "")
		gate.nowFn.Store(nil)
	})
	term := &typedTerm{}
	in := codexInput(&term.codexTerm, name, "", text, false)
	in.Term = term
	in.Send = func(context.Context) error { term.mu.Lock(); term.box = text; term.mu.Unlock(); return nil }
	_, _ = DeliverAutomated(context.Background(), in)

	assert.True(t, gate.ownLeftover(name, text), "right after the send it is Coral's")
	later := time.Now().Add(ownTypedMaxAge + time.Minute)
	fn := func() time.Time { return later }
	gate.nowFn.Store(&fn)
	assert.False(t, gate.ownLeftover(name, text), "150 s later an identical text is a person's draft")
}
