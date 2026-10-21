package ptymanager

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/tmux"
)

// pasteChecked, split into a held paste's turn, the look before the first paste, and the paste with its Enter
// (#4713), decides as pasteCheckedBefore4713 (the function at v1.4.92, below) does: the same result, the same pastes
// and Enters in the agent's terminal, and the same gate afterwards (what is held, why, by what kind, with what retry
// state; the text noted as Coral's; the dialog holds). Each step runs both on fresh gates and identical stand-in
// terminals. Each outcome (each reason a send is held, each way it is given up, delivered or falls back) has a
// floor, so none goes unexercised.

// pcTerm is a stand-in agent terminal with a Claude input box: what its screen shows, what a paste and an Enter do
// to it, and what was pasted and Entered.
type pcSpec struct {
	mode     string // box, prompt, dialog, nobox, error
	box      string // the input box's text in box and dialog modes
	rendered bool
	onPaste  string // lands, dropped, prompt, unavailable, fail
	onEnter  string // submits, stays, dialog, fail
}

type pcTerm struct {
	mu sync.Mutex
	pcSpec
	tries  int // pastes asked for, refused ones included
	pasted []string
	enters int
}

var pcDialog = func() string {
	b, err := os.ReadFile("testdata/screens/claude/2.1.300/bug_report_dialog.ansi")
	if err != nil {
		panic(err)
	}
	// A Windows checkout writes the capture with CRLF line ends: a screen line is read without its \r, as on a terminal.
	return sgrSeq.ReplaceAllString(strings.ReplaceAll(string(b), "\r\n", "\n"), "")
}()

// pcDialogBox is the dialog capture's empty input box (Claude draws a no-break space after its ❯).
var pcDialogBox = regexp.MustCompile(`(?m)^❯[ \x{00a0}]*$`)

func pcBox(text string) string {
	rule := strings.Repeat("─", 60) + "\n"
	return "⏺ OK\n" + rule + "❯\u00a0" + text + "\n" + rule + "  ⏵⏵ auto mode on (shift+tab to cycle)\n"
}

func (p *pcTerm) screenLocked() string {
	switch p.mode {
	case "prompt":
		return permissionMenu
	case "dialog":
		return pcDialogBox.ReplaceAllLiteralString(pcDialog, "❯\u00a0"+p.box)
	case "nobox":
		return "just output, no input box\n"
	}
	return pcBox(p.box)
}

func (p *pcTerm) AgentScreen(context.Context, string, string, string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.mode == "error" {
		return "", errors.New("capture failed")
	}
	return p.screenLocked(), nil
}

func (p *pcTerm) AgentScreenStyled(ctx context.Context, name, agentType, sessionID string) (string, error) {
	return p.AgentScreen(ctx, name, agentType, sessionID)
}

func (p *pcTerm) HasRenderedScreen() bool { return p.rendered }

func (p *pcTerm) SendPrompt(ctx context.Context, _, text, _, _ string) error {
	p.mu.Lock()
	p.tries++
	switch p.onPaste {
	case "unavailable":
		p.mu.Unlock()
		return ErrBracketedPasteUnavailable
	case "fail":
		p.mu.Unlock()
		return errors.New("the pane is gone")
	}
	p.pasted = append(p.pasted, text)
	switch p.onPaste {
	case "lands":
		p.box = text
	case "prompt":
		p.mode = "prompt"
	}
	p.mu.Unlock()
	if !tmux.EnterAllowed(ctx) {
		return tmux.ErrEnterWithheld
	}
	return p.press()
}

func (p *pcTerm) SendEnter(context.Context, string, string, string) error { return p.press() }

func (p *pcTerm) press() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.onEnter == "fail" {
		return errors.New("enter failed")
	}
	p.enters++
	switch p.onEnter {
	case "submits":
		p.box = ""
	case "dialog":
		p.mode = "dialog"
	}
	return nil
}

// pcScreenOnly reads the screen but can't paste: the caller types instead.
type pcScreenOnly struct{ t *pcTerm }

func (s pcScreenOnly) AgentScreen(ctx context.Context, a, b, c string) (string, error) {
	return s.t.AgentScreen(ctx, a, b, c)
}

// pcBlind pastes and presses Enter but can't show its screen.
type pcBlind struct{ t *pcTerm }

func (s pcBlind) SendPrompt(ctx context.Context, a, b, c, d string) error {
	return s.t.SendPrompt(ctx, a, b, c, d)
}
func (s pcBlind) SendEnter(ctx context.Context, a, b, c string) error {
	return s.t.SendEnter(ctx, a, b, c)
}

type pcCase struct {
	term     pcSpec
	wrap     int // 0 the full terminal, 1 screen only, 2 blind
	in       AutomatedInput
	lastPast string
	readyErr bool
}

const pcText = "You have 2 unread messages on the message board. Run 'coral-board read' to see them."

func randomPCCase(rng *rand.Rand, now time.Time) pcCase {
	var c pcCase
	c.term.mode = []string{"box", "box", "box", "box", "prompt", "dialog", "nobox", "error"}[rng.Intn(8)]
	c.term.box = []string{"", "", pcText, "a person's draft", "[Pasted text #1 +12 lines]", "Earlier note from Coral", "Earlier note from Coral"}[rng.Intn(7)]
	c.term.rendered = rng.Intn(5) > 0
	c.term.onPaste = []string{"lands", "lands", "lands", "dropped", "prompt", "unavailable", "fail"}[rng.Intn(7)]
	c.term.onEnter = []string{"submits", "submits", "stays", "dialog", "fail"}[rng.Intn(5)]
	c.wrap = []int{0, 0, 0, 0, 0, 0, 1, 2}[rng.Intn(8)]
	c.in = AutomatedInput{Name: "claude-pc", AgentType: "claude", SessionID: "pc-1", Text: pcText, What: "board nudge", Paste: true}
	c.in.pasteRetry = []int{0, 0, 1, 1, 2}[rng.Intn(5)]
	c.in.entered = rng.Intn(4) == 0
	c.in.enterTries = []int{0, 1, 2, 2}[rng.Intn(4)] // two used: the next Enter that fails gives it up
	c.in.urgent = rng.Intn(4) == 0
	c.in.hadPlaceholder = rng.Intn(6) == 0
	c.in.heldSince = []time.Time{{}, now.Add(-5 * time.Second), now.Add(-30 * time.Second), now.Add(-2 * time.Minute)}[rng.Intn(4)]
	if rng.Intn(3) == 0 {
		c.in.heldWhy = "a prompt is still open"
	}
	if rng.Intn(2) == 0 {
		c.lastPast = squeeze("Earlier note from Coral")
	}
	c.readyErr = rng.Intn(15) == 0
	return c
}

func pcGate(now time.Time, lastPasted string) *inputGate {
	g := &inputGate{held: map[string][]*heldInput{}, reportedFor: map[string]AutomatedInput{}, gens: map[string]uint64{}, shadowLast: map[string]string{},
		delivered: map[string]map[string]time.Time{}, lastPasted: map[string]string{}, dialogHeld: map[string]bool{},
		running: true, // running: no retry loop starts behind the test's back
		timings: gateTimings{tooLong: time.Hour, expiry: 24 * time.Hour, every: time.Hour}}
	clock := func() time.Time { return now }
	g.nowFn.Store(&clock)
	if lastPasted != "" {
		g.lastPasted["claude-pc"] = lastPasted
	}
	return g
}

// pcRun runs one pasteChecked (old or new) on a fresh gate and terminal for c, and describes everything it did.
func pcRun(c pcCase, now time.Time, old bool) (outcome, state string) {
	term := &pcTerm{pcSpec: c.term} // each side starts from the same terminal
	var t any = term
	switch c.wrap {
	case 1:
		t = pcScreenOnly{term}
	case 2:
		t = pcBlind{term}
	}
	in := c.in
	in.Term = t
	if c.readyErr {
		in.Ready = func(context.Context) error { return errors.New("the agent exited") }
	}
	g := pcGate(now, c.lastPast)
	ctx := WithBracketedPasteHint(context.Background(), true) // never the real log directory's hint
	var held bool
	var err error
	var done bool
	if old {
		held, err, done = g.pasteCheckedBefore4713(ctx, in)
	} else {
		held, err, done = g.pasteChecked(ctx, in)
	}
	tries, pasted, enters := term.tries, len(term.pasted), term.enters
	var hs []string
	for _, h := range g.held["claude-pc"] {
		hs = append(hs, fmt.Sprintf("why=%q kind=%q retry=%d entered=%v tries=%d placeholder=%v heldWhy=%q heldAt=%s",
			h.why, h.kind, h.in.pasteRetry, h.in.entered, h.in.enterTries, h.in.hadPlaceholder, h.in.heldWhy, h.heldAt.Format(time.RFC3339Nano)))
	}
	var dialogs []string
	for k, v := range g.dialogHeld {
		dialogs = append(dialogs, fmt.Sprintf("%s=%v", k, v))
	}
	sort.Strings(dialogs)
	state = fmt.Sprintf("held=%v err=%v done=%v pasted=%d enters=%d mode=%s box=%q holds=%v lastPasted=%q dialogs=%v",
		held, err, done, pasted, enters, term.mode, term.box, hs, g.lastPasted["claude-pc"], dialogs)
	switch {
	case !done && tries > 0:
		outcome = "fell back after the paste was refused"
	case !done:
		outcome = "fell back: the terminal can't paste"
	case errors.Is(err, errPasteGaveUp):
		outcome = fmt.Sprintf("given up (pasted %v, entered %v)", pasted > 0, enters > 0)
	case err != nil && c.readyErr && pasted == 0 && enters == 0:
		outcome = "the agent wasn't ready"
	case err != nil:
		outcome = "an error from the terminal"
	case held:
		why := ""
		if hs := g.held["claude-pc"]; len(hs) > 0 {
			why = hs[0].why
			if hs[0].kind != "" {
				why += " [" + hs[0].kind + "]"
			}
		}
		outcome = "held: " + why
	case pasted > 0:
		outcome = "sent: pasted and entered"
	case enters > 0:
		outcome = "sent: only its Enter"
	default:
		outcome = "delivered already: nothing typed"
	}
	return outcome, state
}

func TestPasteCheckedAgreesWithBefore4713(t *testing.T) {
	setWaits(t, func(w *waitTimings) { w.pasteVerifyWait, w.pasteVerifyEvery = 2*time.Millisecond, time.Millisecond }) // the screen changes only on a paste or an Enter
	SetHeldGaveUpFunc(nil)
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	outcomes := map[string]int{}
	for seed := int64(1); seed <= 6000; seed++ {
		rng := rand.New(rand.NewSource(seed))
		c := randomPCCase(rng, now)
		oldOut, oldState := pcRun(c, now, true)
		newOut, newState := pcRun(c, now, false)
		require.Equal(t, oldState, newState, "seed %d: %+v", seed, c)
		require.Equal(t, oldOut, newOut)
		outcomes[newOut]++
	}
	t.Logf("compared, per outcome: %v", outcomes)
	for _, k := range []string{
		"fell back after the paste was refused", "fell back: the terminal can't paste", "the agent wasn't ready", "an error from the terminal",
		"given up (pasted false, entered false)", "given up (pasted true, entered true)",
		"held: the screen couldn't be read", "held: a prompt is still open [prompt]", "held: a prompt is still open [dialog]",
		"held: pasted again but it never reached the input box", "held: " + whyDraft, "held: the input box isn't on screen",
		"held: " + whyLeftover, "held: " + whyNotSubmitted, "held: " + whyNotSubmitted + " [dialog]",
		"held: a prompt opened before its Enter [prompt]", "held: the paste didn't reach the input box",
		"sent: pasted and entered", "sent: only its Enter", "delivered already: nothing typed",
	} {
		assert.GreaterOrEqual(t, outcomes[k], 10, "%q went (almost) unexercised", k)
	}
}

// pasteCheckedBefore4713 is pasteChecked before #4713 split it (automated_input.go at v1.4.92), kept to compare with.
func (g *inputGate) pasteCheckedBefore4713(ctx context.Context, in AutomatedInput) (enterHeld bool, err error, done bool) {
	ps, ok := in.Term.(PromptSender)
	if !ok {
		return false, nil, false
	}
	if in.Ready != nil {
		if err := in.Ready(ctx); err != nil {
			return false, err, true
		}
	}
	agentType, sessionID := in.identity()
	sr, _ := in.Term.(ScreenReader)
	rendered := false
	if rs, ok := in.Term.(RenderedScreen); ok && sr != nil {
		rendered = rs.HasRenderedScreen()
	}
	if in.pasteRetry > 0 && sr != nil {
		// Its turn again (#3457 v2): what is in the input box decides. Our text: only
		// its Enter. Empty (or a placeholder): a menu dropped the paste, so paste
		// again, at most maxPasteRetries times. Anything else (a person's draft, or
		// text Coral can't recognise): never added to; held, and reported.
		screen, serr := sr.AgentScreen(ctx, in.Name, agentType, sessionID)
		if serr != nil {
			if g.pasteStuck(in) {
				return false, g.giveUpPaste(in, "the screen couldn't be read"), true
			}
			g.holdPaste(in, "the screen couldn't be read", false, "")
			return true, nil, true
		}
		if open, why := PromptOnScreen(screen); open {
			g.holdPaste(in, "a prompt is still open", false, promptKind(why))
			return true, nil, true
		}
		state := boxOurs // a PTY's raw output can't show the box: Enter only, never paste again
		if rendered {
			state = in.boxState(ctx, screen, agentType, sessionID)
		} else {
			slog.Info("automated input: unconfirmed delivery (the terminal's box can't be read; sending only the Enter)",
				"session", in.Name, "sender", in.What)
		}
		switch state {
		case boxOurs:
			held, err := g.enterAndConfirm(ctx, in, sr, rendered, agentType, sessionID)
			return held, err, true
		case boxEmpty:
			if in.entered {
				// Its Enter took effect after the last look: delivered, never pasted again.
				g.setLastPasted(in.Name, "")
				return false, nil, true
			}
			if in.pasteRetry > maxPasteRetries {
				if g.pasteStuck(in) {
					return false, g.giveUpPaste(in, "pasted again but it never reached the input box"), true
				}
				g.holdPaste(in, "pasted again but it never reached the input box", false, "")
				return true, nil, true
			}
		case boxOther:
			// Text Coral can't place is in the box after Coral's own paste: never Entered (it
			// could be a person's draft), and given up once stuck rather than blocking (#3708).
			if g.pasteStuck(in) {
				return false, g.giveUpPaste(in, "the input box holds text Coral can't place"), true
			}
			g.holdPaste(in, whyDraft, false, "")
			return true, nil, true
		default:
			if g.pasteStuck(in) {
				return false, g.giveUpPaste(in, "the input box couldn't be found on screen"), true
			}
			g.holdPaste(in, "the input box isn't on screen", false, "")
			return true, nil, true
		}
	}
	if in.pasteRetry == 0 && rendered {
		// Before the first paste (#3457 v3): a box holding someone else's text (a
		// person's draft, or their own paste) is never added to, so it is never
		// submitted with ours; the send waits. Our text already there (an earlier
		// attempt): only its Enter. Checked for every agent type (#3478: agy's
		// drafts were skipped because its box wasn't listed as known): an input
		// line holding other text holds the send whatever the agent.
		if screen, serr := sr.AgentScreen(ctx, in.Name, agentType, sessionID); serr == nil {
			in.hadPlaceholder = hasPlaceholder(screen)
			switch in.boxState(ctx, screen, agentType, sessionID) {
			case boxOther:
				if held, err, own := g.submitLeftover(ctx, in, sr, screen, agentType, sessionID); own {
					if held || err != nil {
						return held, err, true
					}
					break // Coral's earlier text was sent: now ours
				}
				g.holdPaste(in, whyDraft, false, "")
				return true, nil, true
			case boxOurs:
				held, err := g.enterAndConfirm(ctx, in, sr, rendered, agentType, sessionID)
				return held, err, true
			}
		}
	}
	pctx := ctx
	if _, known := BracketedPasteHint(ctx); !known {
		if enabled, known := BracketedPasteFromLog(naming.DefaultLogDir(), agentType, sessionID); known {
			pctx = WithBracketedPasteHint(ctx, enabled)
		}
	}
	var promptOpened atomic.Bool
	check := func(ctx context.Context) bool {
		if sr == nil {
			return true
		}
		// The agent repaints its box a moment after the paste: look for the text for
		// up to pasteVerifyWait before deciding it didn't land (#3457 v2).
		deadline := time.Now().Add(waits().pasteVerifyWait)
		for {
			screen, err := sr.AgentScreen(ctx, in.Name, agentType, sessionID)
			if err != nil {
				return true
			}
			if open, why := PromptOnScreen(screen); open {
				promptOpened.Store(isPromptHold(why))
				return false
			}
			if !rendered || inputBoxState(screen, in.Text, in.hadPlaceholder) == boxOurs {
				return true
			}
			if time.Now().After(deadline) || ctx.Err() != nil {
				return false
			}
			time.Sleep(waits().pasteVerifyEvery)
		}
	}
	g.setLastPasted(in.Name, in.Text) // Coral's, until its Enter is seen to submit it (#4104)
	err = ps.SendPrompt(tmux.WithBeforeEnter(pctx, check), in.Name, in.Text, agentType, sessionID)
	if err == nil {
		held, err := g.confirmSubmitted(ctx, in, sr, rendered, agentType, sessionID)
		return held, err, true
	}
	switch {
	case errors.Is(err, ErrBracketedPasteUnavailable):
		return false, nil, false
	case errors.Is(err, tmux.ErrEnterWithheld):
		if promptOpened.Load() {
			g.holdPaste(in, "a prompt opened before its Enter", true, kindPrompt)
		} else {
			g.holdPaste(in, "the paste didn't reach the input box", true, "")
		}
		return true, nil, true
	}
	return false, err, true
}
