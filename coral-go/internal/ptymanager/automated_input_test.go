package ptymanager

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/tmux"
)

const (
	permissionMenu = " Do you want to proceed?\n ❯ 1. Yes\n   2. Yes, and don't ask again\n   3. No\n\n Esc to cancel · Tab to amend\n"
	idleInputBox   = "⏺ Done.\n\n╭────────╮\n│ >      │\n╰────────╯\n  ? for shortcuts\n"
)

// fakeAgent is a terminal whose screen the test sets, recording what is typed.
type fakeAgent struct {
	mu     sync.Mutex
	screen string
	typed  []string
}

func (f *fakeAgent) AgentScreen(context.Context, string, string, string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.screen, nil
}
func (f *fakeAgent) show(s string) { f.mu.Lock(); f.screen = s; f.mu.Unlock() }
func (f *fakeAgent) sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.typed...)
}
func (f *fakeAgent) input(name, key, text string, requireBox bool) AutomatedInput {
	return AutomatedInput{Term: f, Name: name, Key: key, Text: text, What: "test", RequireInputBox: requireBox,
		Send: func(context.Context) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			// A send typed into an open menu answers it: the test fails then.
			if open, _ := PromptOnScreen(f.screen); open {
				f.typed = append(f.typed, "ANSWERED THE PROMPT WITH: "+text)
				return nil
			}
			f.typed = append(f.typed, text)
			return nil
		}}
}

func fastGate(t *testing.T) {
	t.Helper()
	prev := gate.setTimings(gateTimings{tooLong: time.Hour, expiry: 24 * time.Hour, every: 20 * time.Millisecond})
	t.Cleanup(func() { gate.setTimings(prev); SetNeedsInputFunc(nil); SetHeldTooLongFunc(nil) })
}

// The operator's report (#3431): a board message, a task notification and a
// nudge arrive while the agent shows a permission menu. Nothing is typed (the
// menu stays open, no option chosen); once the menu is answered each is typed
// exactly once, in order.
func TestAutomatedInputIsHeldWhileAPermissionMenuIsOpen(t *testing.T) {
	fastGate(t)
	ctx := context.Background()
	a := &fakeAgent{screen: permissionMenu}
	name := "claude-gate-held"

	for _, in := range []AutomatedInput{
		a.input(name, "", "[board] Lead: please review #12", false),
		a.input(name, "", "You have a new task in Coral (#7: fix it)", false),
		a.input(name, "board-unread:team", "You have 1 unread message on the message board.", false),
	} {
		d, err := DeliverAutomated(ctx, in)
		require.NoError(t, err)
		assert.Equal(t, Held, d)
	}
	time.Sleep(100 * time.Millisecond) // several retries while the menu is open
	assert.Empty(t, a.sent(), "nothing typed into the open menu")
	assert.Equal(t, 3, HeldCount(name))

	a.show(idleInputBox) // the user answered the menu
	require.Eventually(t, func() bool { return len(a.sent()) == 3 }, 5*time.Second, 10*time.Millisecond)
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, []string{"[board] Lead: please review #12", "You have a new task in Coral (#7: fix it)", "You have 1 unread message on the message board."}, a.sent(), "each once, in order")
	assert.Zero(t, HeldCount(name))
}

// A newer send with the same key replaces the held one: the newest unread
// count is typed, once.
func TestAHeldSendIsSupersededByTheSameKey(t *testing.T) {
	fastGate(t)
	ctx := context.Background()
	a := &fakeAgent{screen: permissionMenu}
	name := "claude-gate-supersede"
	_, _ = DeliverAutomated(ctx, a.input(name, "board-unread:team", "You have 1 unread message", false))
	_, _ = DeliverAutomated(ctx, a.input(name, "board-unread:team", "You have 3 unread messages", false))
	assert.Equal(t, 1, HeldCount(name))
	a.show(idleInputBox)
	require.Eventually(t, func() bool { return len(a.sent()) == 1 }, 5*time.Second, 10*time.Millisecond)
	time.Sleep(60 * time.Millisecond)
	assert.Equal(t, []string{"You have 3 unread messages"}, a.sent())
}

// With no prompt open the send is typed at once, and a later send waits behind
// ones still held so order is kept.
func TestAutomatedInputIsTypedAtOnceWhenNothingWaits(t *testing.T) {
	fastGate(t)
	a := &fakeAgent{screen: idleInputBox}
	d, err := DeliverAutomated(context.Background(), a.input("claude-gate-now", "", "hello", false))
	require.NoError(t, err)
	assert.Equal(t, Sent, d)
	assert.Equal(t, []string{"hello"}, a.sent())
}

// The state can say "needs input" while the screen is plainly at its input box
// (a hook was missed): the screen wins, so notifications aren't stuck. With an
// open prompt on screen the state doesn't matter.
func TestStateNeedsInputDefersToAVisibleInputBox(t *testing.T) {
	fastGate(t)
	SetNeedsInputFunc(func(context.Context, string, string, string) bool { return true })
	a := &fakeAgent{screen: idleInputBox}
	d, _ := DeliverAutomated(context.Background(), a.input("claude-gate-state", "", "x", false))
	assert.Equal(t, Sent, d)

	b := &fakeAgent{screen: "some output with no box"}
	d, _ = DeliverAutomated(context.Background(), b.input("claude-gate-state2", "", "y", false))
	assert.Equal(t, Held, d, "state says needs input and the screen doesn't show the input box")
	assert.Empty(t, b.sent())
}

// A launch's first prompt (RequireInputBox) waits through the trust-this-folder
// screen and a blank starting screen, and is typed once the input box shows.
func TestFirstPromptWaitsForTheInputBox(t *testing.T) {
	fastGate(t)
	a := &fakeAgent{screen: ""}
	name := "claude-gate-launch"
	d, _ := DeliverAutomated(context.Background(), a.input(name, "initial-prompt", "Build the feature", true))
	assert.Equal(t, Held, d, "nothing on screen yet")
	a.show(" Do you trust the files in this folder?\n\n ❯ 1. Yes, proceed\n   2. No, exit\n\n Enter to confirm · Esc to exit\n")
	time.Sleep(100 * time.Millisecond)
	assert.Empty(t, a.sent(), "the prompt never answers the trust screen")
	a.show(idleInputBox)
	require.Eventually(t, func() bool { return len(a.sent()) == 1 }, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, []string{"Build the feature"}, a.sent())
}

// A send held too long is reported once, so the dashboard can
// show the agent needs the user.
func TestASendHeldTooLongIsReportedOnce(t *testing.T) {
	fastGate(t)
	gate.setTimings(gateTimings{tooLong: 50 * time.Millisecond, expiry: 24 * time.Hour, every: 20 * time.Millisecond})
	var mu sync.Mutex
	var reports []string
	SetHeldTooLongFunc(func(name, _, _, what string) { mu.Lock(); reports = append(reports, name+"/"+what); mu.Unlock() })
	a := &fakeAgent{screen: permissionMenu}
	name := "claude-gate-long"
	_, _ = DeliverAutomated(context.Background(), a.input(name, "", "waiting", false))
	require.Eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(reports) == 1 }, 5*time.Second, 10*time.Millisecond)
	time.Sleep(150 * time.Millisecond)
	mu.Lock()
	assert.Equal(t, []string{name + "/test"}, reports, "reported once")
	mu.Unlock()
	a.show(idleInputBox)
	require.Eventually(t, func() bool { return len(a.sent()) == 1 }, 5*time.Second, 10*time.Millisecond)
}

// A first prompt whose agent never shows a box Coral recognises isn't held for
// ever: after the held-too-long time it is typed once no prompt is open. While
// a prompt is open it still waits.
func TestFirstPromptFallsBackWhenTheBoxIsNeverRecognised(t *testing.T) {
	fastGate(t)
	gate.setTimings(gateTimings{tooLong: 150 * time.Millisecond, expiry: 24 * time.Hour, every: 20 * time.Millisecond})
	a := &fakeAgent{screen: " Do you trust the files in this folder?\n ❯ 1. Yes, proceed\n   2. No, exit\n"}
	name := "claude-gate-unknown-box"
	d, _ := DeliverAutomated(context.Background(), a.input(name, "initial-prompt", "Build it", true))
	assert.Equal(t, Held, d)
	time.Sleep(400 * time.Millisecond)
	assert.Empty(t, a.sent(), "past the fallback time an open prompt still holds it")
	a.show("Some new CLI v9 :: type here")
	require.Eventually(t, func() bool { return len(a.sent()) == 1 }, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, []string{"Build it"}, a.sent())
}

// enterAgent is a fakeAgent whose send behaves like tmux's: text, a pause in
// which a permission prompt opens, then the Enter only if tmux.EnterAllowed.
type enterAgent struct {
	*fakeAgent
	enters int
}

func (e *enterAgent) SendEnter(context.Context, string, string, string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if open, _ := PromptOnScreen(e.screen); open {
		e.typed = append(e.typed, "ENTER ANSWERED THE PROMPT")
		return nil
	}
	e.enters++
	e.typed = append(e.typed, "<Enter>")
	return nil
}

// A prompt that opens between a send's text and its Enter doesn't take the
// Enter (Backend's #3431 review, (a)): the Enter is withheld, held on its own,
// and pressed once after the prompt is answered.
func TestAPromptOpeningBeforeTheEnterWithholdsIt(t *testing.T) {
	fastGate(t)
	e := &enterAgent{fakeAgent: &fakeAgent{screen: idleInputBox}}
	name := "claude-gate-enter"
	in := AutomatedInput{Term: e, Name: name, Text: "hello", What: "test",
		Send: func(ctx context.Context) error {
			e.mu.Lock()
			e.typed = append(e.typed, "hello")
			e.screen = permissionMenu // the agent asks for permission right now
			e.mu.Unlock()
			if !tmux.EnterAllowed(ctx) {
				return tmux.ErrEnterWithheld
			}
			e.mu.Lock()
			e.typed = append(e.typed, "<Enter>")
			e.mu.Unlock()
			return nil
		}}
	d, err := DeliverAutomated(context.Background(), in)
	require.NoError(t, err)
	assert.Equal(t, Held, d, "the Enter is held")
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, []string{"hello"}, e.sent(), "no Enter into the open prompt")
	e.show(idleInputBox)
	require.Eventually(t, func() bool { return len(e.sent()) == 2 }, 5*time.Second, 10*time.Millisecond)
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, []string{"hello", "<Enter>"}, e.sent(), "the text once, then its Enter once")
}

// Held nudges and notifications expire (1 h by default); a first prompt waits
// for as long as its agent lives (Backend's #3431 review, (b)).
func TestHeldNudgesExpireButAFirstPromptDoesNot(t *testing.T) {
	fastGate(t)
	gate.setTimings(gateTimings{tooLong: time.Hour, expiry: 100 * time.Millisecond, every: 20 * time.Millisecond})
	a := &fakeAgent{screen: permissionMenu}
	_, _ = DeliverAutomated(context.Background(), a.input("claude-gate-expire", "n", "old nudge", false))
	_, _ = DeliverAutomated(context.Background(), a.input("claude-gate-expire", "initial-prompt", "first prompt", true))
	require.Eventually(t, func() bool { return HeldCount("claude-gate-expire") == 1 }, 5*time.Second, 10*time.Millisecond, "the nudge expired")
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, 1, HeldCount("claude-gate-expire"), "the first prompt is still held")
	DropHeld("claude-gate-expire", "test cleanup")
}

// Everything held for a session goes when its terminal is killed, restarted or
// relaunched (Backend's #3431 review, required 2): a held first prompt is never
// typed into the next launch in that terminal.
func TestDropHeldDiscardsASessionsHeldSends(t *testing.T) {
	fastGate(t)
	a := &fakeAgent{screen: ""}
	name := "claude-gate-relaunch"
	_, _ = DeliverAutomated(context.Background(), a.input(name, "initial-prompt", "old launch's prompt", true))
	require.Equal(t, 1, HeldCount(name))
	DropHeld(name+".0", "a launch command is typed") // a pane target names the same session
	assert.Zero(t, HeldCount(name))

	// Every form of tmux target names the session (QA's #3431 v3 live check:
	// "claude-x:0.0" from the restart path used to leave the held send).
	b := &fakeAgent{screen: permissionMenu}
	for _, target := range []string{name, name + ".0", name + ":0", name + ":0.0"} {
		_, _ = DeliverAutomated(context.Background(), b.input(name, "", "stale nudge", false))
		require.Equal(t, 1, HeldCount(name), target)
		DropHeld(target, "restarted")
		assert.Zero(t, HeldCount(name), "DropHeld(%q) drops %q's held sends", target, name)
	}
	a.show(idleInputBox)
	time.Sleep(150 * time.Millisecond)
	assert.Empty(t, a.sent(), "nothing from the old launch")
}
