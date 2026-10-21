package ptymanager

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/terminalinput"
	"github.com/subgentic/coral/internal/tmux"
)

// #3708: the coral-hub Orchestrator got no board message for half an hour. A long automated
// paste wrapped over more lines than the gate looked at, so the paste "didn't reach the input
// box" (it had), the send was held as "the input box isn't on screen", and every later send
// waited behind it, with no way out.

// claudeBoxScreen is Claude's screen with text in its input box, wrapped at width columns.
func claudeBoxScreen(text string, width int) string {
	rule := strings.Repeat("─", 120)
	var b strings.Builder
	b.WriteString("⏺ Done: the review is posted.\n\n" + rule + "\n")
	first := true
	for _, para := range strings.Split(text, "\n") {
		for len(para) > 0 || first {
			n := min(width, len(para))
			if first {
				b.WriteString("❯\u00a0" + para[:n] + "\n")
				first = false
			} else {
				b.WriteString("  " + para[:n] + "\n")
			}
			para = para[n:]
		}
	}
	b.WriteString(rule + "\n  ⏸ manual mode on · ? for shortcuts\n")
	return b.String()
}

// The misread: a paste that landed, wrapped over 20 lines, is ours (the old 12-line window
// never found the box's first line); so is one with a quoted "> " line inside it.
func TestALongWrappedPasteIsOurs(t *testing.T) {
	long := "[board] Orchestrator: #3612 is complete. " + strings.Repeat("The completion notice goes on with its details. ", 40)
	screen := claudeBoxScreen(long, 100)
	lines := tailLines(screenText(screen), 1000)
	marker := -1
	for i, l := range lines {
		if strings.HasPrefix(l, "❯") {
			marker = i
		}
	}
	require.Greater(t, len(lines)-marker, 12, "the box's first line is more than 12 lines up: the old window missed it")
	assert.Equal(t, boxOurs, inputBoxState(screen, long, false), "a long paste that landed is ours")

	quoted := "[board] QA: replying to this:\n> " + strings.Repeat("the quoted text runs on and on ", 30) + "\nand my answer follows."
	assert.Equal(t, boxOurs, inputBoxState(claudeBoxScreen(quoted, 100), quoted, false), "a quoted line in the paste isn't taken for the box's start")
	assert.Equal(t, boxEmpty, inputBoxState(claudeBoxScreen("", 100), long, false), "an empty box is still empty")
}

// stuckTerm serves a screen that can change, and records pastes and Enters. Its paste runs the
// gate's check before the Enter, as tmux's does. With echo set it behaves like a working agent:
// a paste shows in the (empty) box, and its Enter empties the box again.
type stuckTerm struct {
	mu       sync.Mutex
	screen   string
	echo     bool
	pasted   []string
	enters   int
	onScreen func() // called, with no lock held, at each read of the screen (tests sample the gate while it is looking)
}

func (s *stuckTerm) set(screen string, echo bool) {
	s.mu.Lock()
	s.screen, s.echo = screen, echo
	s.mu.Unlock()
}
func (s *stuckTerm) AgentScreen(context.Context, string, string, string) (string, error) {
	s.mu.Lock()
	screen, on := s.screen, s.onScreen
	s.mu.Unlock()
	if on != nil {
		on()
	}
	return screen, nil
}
func (s *stuckTerm) HasRenderedScreen() bool { return true }
func (s *stuckTerm) SendPrompt(ctx context.Context, _ terminalinput.Origin, _, text, _, _ string) error {
	s.mu.Lock()
	s.pasted = append(s.pasted, text)
	if s.echo {
		s.screen = claudeBoxScreen(text, 100)
	}
	s.mu.Unlock()
	if !tmux.EnterAllowed(ctx) {
		return tmux.ErrEnterWithheld
	}
	return s.SendEnter(ctx, "", "", "")
}
func (s *stuckTerm) SendEnter(context.Context, string, string, string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enters++
	if s.echo {
		s.screen = claudeBoxScreen("", 100)
	}
	return nil
}
func (s *stuckTerm) got() ([]string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.pasted...), s.enters
}

// noBox: output fills the screen and no input box can be found (the state the wedge stayed in).
const noBox = "⏺ Reading 4 files…\n  ⎿  internal/ptymanager/automated_input.go\n  ⎿  internal/ptymanager/paste_check.go\n"

type gaveUpLog struct {
	mu  sync.Mutex
	got []string
}

func (g *gaveUpLog) record(_, _, _, what, why string) {
	g.mu.Lock()
	g.got = append(g.got, what+": "+why)
	g.mu.Unlock()
}
func (g *gaveUpLog) list() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.got...)
}

func stuckGate(t *testing.T, after, urgent time.Duration) *gaveUpLog {
	t.Helper()
	fastGate(t)
	setWaits(t, func(w *waitTimings) {
		w.pasteVerifyWait, w.pasteVerifyEvery, w.pasteStuckAfter, w.pasteStuckAfterUrgent = 30*time.Millisecond, 10*time.Millisecond, after, urgent
	})
	g := &gaveUpLog{}
	SetHeldGaveUpFunc(g.record)
	t.Cleanup(func() {
		SetHeldGaveUpFunc(nil)
	})
	return g
}

func pasteSend(term *stuckTerm, name, text, what string, mention bool) AutomatedInput {
	return AutomatedInput{Term: term, Name: name, AgentType: "claude", SessionID: "s-" + name, Text: text, What: what,
		Paste: true, Mention: mention, Send: func(context.Context) error { return nil }}
}

// A held paste whose box never confirms it is given up after pasteStuckAfter: dropped, with a
// notice, never Entered, and the next send goes.
func TestAStuckHeldPasteGivesUpAndTheQueueMoves(t *testing.T) {
	gave := stuckGate(t, 150*time.Millisecond, 50*time.Millisecond)
	name := "claude-stuck-paste"
	term := &stuckTerm{screen: noBox}
	t.Cleanup(func() { DropHeld(name, "test done") })

	d, err := DeliverAutomated(context.Background(), pasteSend(term, name, "[board] Orchestrator: #3612 is complete.", "board message", false))
	require.NoError(t, err)
	assert.Equal(t, Held, d, "the paste's box never showed it: held")
	d, err = DeliverAutomated(context.Background(), pasteSend(term, name, "You have 1 unread message on the message board.", "board nudge", false))
	require.NoError(t, err)
	assert.Equal(t, Held, d, "a later send waits behind it")

	time.Sleep(60 * time.Millisecond)
	assert.Empty(t, gave.list(), "not given up before pasteStuckAfter")
	require.Eventually(t, func() bool { return len(gave.list()) > 0 }, 3*time.Second, 10*time.Millisecond, "the stuck paste is given up")
	assert.Contains(t, gave.list()[0], "board message: the input box couldn't be found on screen")
	_, enters := term.got()
	assert.Zero(t, enters, "a box that can't be read is never Entered")

	// The box shows up empty: the send behind it goes now (pasted, then Entered once ours).
	term.set(claudeBoxScreen("", 100), true)
	require.Eventually(t, func() bool { _, e := term.got(); return e == 1 }, 3*time.Second, 10*time.Millisecond, "the next send is pasted and Entered")
	pasted, _ := term.got()
	assert.Equal(t, "You have 1 unread message on the message board.", pasted[len(pasted)-1])
	// The Enter is seen on the terminal before the gate has read the box once more and let go of the send (HeldCount counts it until then, #5205): wait for it.
	require.Eventually(t, func() bool { return HeldCount(name) == 0 }, 3*time.Second, 10*time.Millisecond, "nothing is held any more")
}

// A mention never waits behind a stuck routine paste for long: it makes the stuck paste give up
// after pasteStuckAfterUrgent, where without it the paste stays held.
func TestAMentionDoesNotWaitBehindAStuckPaste(t *testing.T) {
	gave := stuckGate(t, time.Hour, 50*time.Millisecond)
	name := "claude-stuck-mention"
	term := &stuckTerm{screen: noBox}
	t.Cleanup(func() { DropHeld(name, "test done") })

	_, err := DeliverAutomated(context.Background(), pasteSend(term, name, "You have 1 unread message on the message board.", "board nudge", false))
	require.NoError(t, err)
	time.Sleep(150 * time.Millisecond)
	assert.Empty(t, gave.list(), "a routine paste alone waits out pasteStuckAfter (an hour)")
	assert.Equal(t, 1, HeldCount(name))

	_, err = DeliverAutomated(context.Background(), pasteSend(term, name, "[board] QA: @Orchestrator please look", "board mention", true))
	require.NoError(t, err)
	require.Eventually(t, func() bool { return len(gave.list()) > 0 }, 3*time.Second, 10*time.Millisecond, "the stuck paste gives up for the mention")
	assert.Contains(t, gave.list()[0], "board nudge:")
	// The give-up is reported on its own goroutine, before the mention is pasted (#3788).
	require.Eventually(t, func() bool {
		pasted, _ := term.got()
		return slices.Contains(pasted, "[board] QA: @Orchestrator please look")
	}, 3*time.Second, 10*time.Millisecond, "the mention is pasted")
}

// Text Coral can't place in the box after its own paste (a person typed meanwhile) is never
// Entered: once stuck the send is given up and the text left as it is.
func TestAStuckPasteNeverEntersSomeoneElsesText(t *testing.T) {
	gave := stuckGate(t, 150*time.Millisecond, 50*time.Millisecond)
	name := "claude-stuck-other"
	term := &stuckTerm{screen: noBox}
	t.Cleanup(func() { DropHeld(name, "test done") })
	_, err := DeliverAutomated(context.Background(), pasteSend(term, name, "You have 1 unread message on the message board.", "board nudge", false))
	require.NoError(t, err)
	term.set(claudeBoxScreen("my own half-typed question", 100), false)
	require.Eventually(t, func() bool { return len(gave.list()) > 0 }, 3*time.Second, 10*time.Millisecond)
	assert.Contains(t, gave.list()[0], "holds text Coral can't place")
	_, enters := term.got()
	assert.Zero(t, enters, "a person's text is never submitted")
}

// #5205: a held send is counted as held for as long as it is held, also while the retry loop has it off the queue to try it again (it is taken off before
// it is typed, so that it is typed once). TestAMentionDoesNotWaitBehindAStuckPaste read 0 there now and then (1 run in 300) and the sessions list showed
// no held input for the length of each retry. The count is read at every look the gate takes at the screen, which is when a retry is in flight.
func TestAHeldSendIsCountedWhileTheLoopRetriesIt(t *testing.T) {
	gave := stuckGate(t, time.Hour, time.Hour)
	name := "claude-held-inflight"
	term := &stuckTerm{screen: noBox}
	t.Cleanup(func() { DropHeld(name, "test done"); waitGateIdle(t) })

	d, err := DeliverAutomated(context.Background(), pasteSend(term, name, "You have 1 unread message on the message board.", "board nudge", false))
	require.NoError(t, err)
	require.Equal(t, Held, d)

	var mu sync.Mutex
	var counts []int
	term.mu.Lock()
	term.onScreen = func() {
		n := HeldCount(name)
		mu.Lock()
		counts = append(counts, n)
		mu.Unlock()
	}
	term.mu.Unlock()
	require.Eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(counts) >= 12 }, 5*time.Second, 5*time.Millisecond, "the loop looks at the screen again and again")
	term.mu.Lock()
	term.onScreen = nil
	term.mu.Unlock()

	mu.Lock()
	defer mu.Unlock()
	for i, n := range counts {
		assert.Equal(t, 1, n, "the send is held at look %d of %d, whether or not the loop has it off the queue", i+1, len(counts))
	}
	assert.Empty(t, gave.list())
}

// A DropHeld while the loop has the send off the queue for a retry leaves nothing counted: the attempt in flight goes with the rest.
func TestADropWhileTheLoopRetriesASendLeavesNothingCounted(t *testing.T) {
	stuckGate(t, time.Hour, time.Hour)
	name := "claude-held-inflight-drop"
	term := &stuckTerm{screen: noBox}
	t.Cleanup(func() { DropHeld(name, "test done"); waitGateIdle(t) })
	d, err := DeliverAutomated(context.Background(), pasteSend(term, name, "You have 1 unread message on the message board.", "board nudge", false))
	require.NoError(t, err)
	require.Equal(t, Held, d)

	var once sync.Once
	afterDrop := make(chan int, 1)
	term.mu.Lock()
	term.onScreen = func() {
		gate.mu.Lock()
		inFlight := gate.inflight[name] != nil // the look the retry takes while it has the send off the queue
		gate.mu.Unlock()
		if !inFlight {
			return
		}
		once.Do(func() {
			DropHeld(name, "test")
			afterDrop <- HeldCount(name)
		})
	}
	term.mu.Unlock()
	select {
	case n := <-afterDrop:
		assert.Zero(t, n, "dropped: nothing is held, nothing is in flight")
	case <-time.After(5 * time.Second):
		t.Fatal("the loop never looked at the screen")
	}
	term.mu.Lock()
	term.onScreen = nil
	term.mu.Unlock()
}

// waitGateIdle waits for the gate's retry loop to end after the last held send was dropped. These tests leave the loop in the middle of a retry (that is
// where they look), and a loop still running when the next test sets the paste timings reads them while they are written (the race detector's report).
func waitGateIdle(t *testing.T) {
	t.Helper()
	require.Eventually(t, func() bool {
		gate.mu.Lock()
		defer gate.mu.Unlock()
		return !gate.running
	}, 5*time.Second, 5*time.Millisecond, "the gate's loop ends once nothing is held")
}
