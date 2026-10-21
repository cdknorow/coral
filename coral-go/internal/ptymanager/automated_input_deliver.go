// Delivering an automated input: the checked send and paste, the retries, and holding what cannot go yet (moved out of automated_input.go, #5091; no behaviour change).

package ptymanager

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/subgentic/coral/internal/inputlog"
	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/tmux"
)

// DeliverAutomated types an automated send now if the agent is not waiting on
// the user, and holds it otherwise. A held send is retried until the prompt is
// gone and then typed once.
// ErrNotWanted is returned by a send's Ready or Send when what it was about is over by the time
// it would be typed (a board nudge whose messages the agent has read since, #4362): nothing is
// typed, and the gate drops it quietly instead of reporting a failed send.
var ErrNotWanted = errors.New("automated input no longer wanted")

func DeliverAutomated(ctx context.Context, in AutomatedInput) (Delivery, error) {
	if in.Send == nil {
		return Sent, errors.New("automated input without a send function")
	}
	if in.Key == "" {
		in.Key = in.Text
	}
	unlock := gate.lockSession(in.Name)
	defer unlock()
	gate.mu.Lock()
	in.gen = gate.gens[in.Name]
	gate.mu.Unlock()
	if waiting, why, kind := in.waiting(ctx); waiting {
		gate.hold(in, why, kind)
		return Held, nil
	}
	// Anything already held for this agent goes first, so order is kept. A mention lets a
	// stuck paste of Coral's ahead of it give up sooner (#3708).
	fctx := ctx
	if in.Mention {
		fctx = context.WithValue(ctx, mentionWaitingKey{}, true)
	}
	if gate.flush(fctx, in.Name) {
		// The flush found the agent waiting again: queue behind what it held.
		gate.hold(in, "earlier sends still held", "")
		return Held, nil
	}
	enterHeld, err := gate.sendChecked(ctx, in)
	if enterHeld {
		return Held, nil
	}
	if errors.Is(err, ErrTeamBusy) {
		// The team's next slot is over MaxTeamWait away (#3613): held, and the loop tries again.
		gate.hold(in, whyTeamSpaced, "")
		return Held, nil
	}
	gate.shadowForget(in.Name, in.Key)
	if err == nil {
		gate.noteDelivered(in.Name, in.Key)
		noteDeliveredText(in.Name, in.Text)
	}
	return Sent, err
}

// sendChecked runs in.Send with the screen re-checked just before its final
// Enter. If a prompt opened after the text was typed, the Enter is withheld and
// held on its own, ahead of anything else held for the agent; it reports true
// then. Any other error is returned.
func (g *inputGate) sendChecked(ctx context.Context, in AutomatedInput) (enterHeld bool, err error) {
	// Coral's automated sends to one team are spaced (#3613): the first goes at once, the next
	// 2-3 s after. Only here, where a send really goes: a held send waits nothing until it's due.
	if err := SpaceTeam(ctx, teamFor(in.Name)); err != nil {
		return false, err
	}
	if in.Paste {
		if held, err, done := g.pasteChecked(ctx, in); done {
			return held, err
		}
	}
	var footerOnly atomic.Bool // the prompt seen was only a footer (isPromptHold)
	check := func(ctx context.Context) bool {
		r, ok := in.Term.(ScreenReader)
		if !ok {
			return true
		}
		screen, err := r.AgentScreen(ctx, in.Name, in.AgentType, in.SessionID)
		if err != nil {
			return true
		}
		open, why := PromptOnScreen(screen)
		footerOnly.Store(open && !isPromptHold(why))
		return !open
	}
	err = in.Send(tmux.WithAutomated(tmux.WithBeforeEnter(ctx, check)))
	if errors.Is(err, tmux.ErrEnterWithheld) {
		kind := kindPrompt
		if footerOnly.Load() {
			kind = ""
		}
		g.holdEnter(in, kind)
		return true, nil
	}
	return false, err
}

// pasteChecked sends in as a bracketed paste and its Enter. Enter goes only when no
// prompt is open and (on a rendered screen) the input box shows the text; otherwise
// the send is held first in the agent's queue, and on its turn sends just its Enter
// if the text is in the box, or pastes again (once) if a menu dropped it. done is
// false when the paste can't be used (no PromptSender, bracketed paste off or
// unknown): the caller falls back to typing.
func (g *inputGate) pasteChecked(ctx context.Context, in AutomatedInput) (enterHeld bool, err error, done bool) {
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
		if held, err, decided := g.pasteRetryTurn(ctx, in, sr, rendered, agentType, sessionID); decided {
			return held, err, true
		}
	}
	if in.pasteRetry == 0 && rendered {
		if held, err, decided := g.boxBeforeFirstPaste(ctx, &in, sr, rendered, agentType, sessionID); decided {
			return held, err, true
		}
	}
	return g.pasteAndEnter(ctx, in, ps, sr, rendered, agentType, sessionID)
}

// pasteRetryTurn is a held paste's turn again (#3457 v2): what is in the input box decides. Our text: only
// its Enter. Empty (or a placeholder): a menu dropped the paste, so paste
// again, at most maxPasteRetries times (decided false: the caller pastes). Anything else (a person's draft, or
// text Coral can't recognise): never added to; held, and reported.
func (g *inputGate) pasteRetryTurn(ctx context.Context, in AutomatedInput, sr ScreenReader, rendered bool, agentType, sessionID string) (held bool, err error, decided bool) {
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
		return false, nil, false
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

// boxBeforeFirstPaste looks at the input box before the first paste (#3457 v3): a box holding someone else's text
// (a person's draft, or their own paste) is never added to, so it is never
// submitted with ours; the send waits. Our text already there (an earlier
// attempt): only its Enter. Checked for every agent type (#3478: agy's
// drafts were skipped because its box wasn't listed as known): an input
// line holding other text holds the send whatever the agent. It notes in in whether the box showed a paste
// placeholder; decided false: the caller pastes.
func (g *inputGate) boxBeforeFirstPaste(ctx context.Context, in *AutomatedInput, sr ScreenReader, rendered bool, agentType, sessionID string) (held bool, err error, decided bool) {
	screen, serr := sr.AgentScreen(ctx, in.Name, agentType, sessionID)
	if serr != nil {
		return false, nil, false
	}
	in.hadPlaceholder = hasPlaceholder(screen)
	switch in.boxState(ctx, screen, agentType, sessionID) {
	case boxOther:
		if held, err, own := g.submitLeftover(ctx, *in, sr, screen, agentType, sessionID); own {
			if held || err != nil {
				return held, err, true
			}
			return false, nil, false // Coral's earlier text was sent: now ours
		}
		g.holdPaste(*in, whyDraft, false, "")
		return true, nil, true
	case boxOurs:
		held, err := g.enterAndConfirm(ctx, *in, sr, rendered, agentType, sessionID)
		return held, err, true
	}
	return false, nil, false
}

// pasteAndEnter pastes in's text and presses its Enter once the text shows in the box (looked for up to
// pasteVerifyWait) with no prompt open; then confirms it was submitted. A withheld Enter holds the send; done is
// false when bracketed paste is unavailable (the caller types instead).
func (g *inputGate) pasteAndEnter(ctx context.Context, in AutomatedInput, ps PromptSender, sr ScreenReader, rendered bool, agentType, sessionID string) (enterHeld bool, err error, done bool) {
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
		w := waits()
		deadline := time.Now().Add(w.pasteVerifyWait)
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
			time.Sleep(w.pasteVerifyEvery)
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

// whyDraft: a paste send held because the input box holds text that isn't Coral's.
const whyDraft = "a draft is in the input box (someone else's text); not adding to it"

// releasedBecause says what ended a hold, from its last reason, for the delivery's log
// line: a draft hold ends when the draft is cleared, not when a prompt closes (#3520).
// whyTeamSpaced: a send held because its team's next slot was over MaxTeamWait away (#3613).
const whyTeamSpaced = "the team's sends are spaced"

func releasedBecause(why string) string {
	switch {
	case why == whyTeamSpaced:
		return "the team's slot came"
	case why == whyDraft:
		return "the draft cleared"
	case strings.Contains(why, "input box"):
		return "the input box was ready"
	case why == "earlier sends still held":
		return "the earlier sends were typed"
	case strings.HasPrefix(why, "signals: starting"):
		return "the agent started"
	case why == "signals: in turn":
		return "the turn ended"
	case strings.HasPrefix(why, busyWhy):
		return "the agent stopped working"
	}
	return "the prompt closed"
}

// boxState is inputBoxState for this send, where a box that seems to hold someone
// else's text is checked once more on the styled screen: Claude's faint prompt
// suggestion is an empty box (#3468). A "[Pasted" placeholder is never one.
func (in AutomatedInput) boxState(ctx context.Context, screen, agentType, sessionID string) boxState {
	st := inputBoxState(screen, in.Text, in.hadPlaceholder)
	if st != boxOther || hasPlaceholder(screen) {
		return st
	}
	if ss, ok := in.Term.(StyledScreenReader); ok {
		if styled, err := ss.AgentScreenStyled(ctx, in.Name, agentType, sessionID); err == nil && suggestionOnly(styled) {
			return boxEmpty
		}
	}
	return st
}

// maxPasteRetries bounds pasting a send again into an empty box: after that it stays
// held (and is reported) rather than pasted once more.
const maxPasteRetries = 1

// pasteVerifyWait and pasteVerifyEvery (fields of waits(), seams.go): how long, and how often, the gate looks
// for pasted text in the input box before deciding the paste didn't land.

// holdPaste holds a paste send whose Enter was withheld, first in the agent's queue.
// pasted says the text was just pasted (a later turn may paste it once more); prompt
// that an open prompt holds it (HeldTooLongFunc).
func (g *inputGate) holdPaste(in AutomatedInput, why string, pasted bool, kind string) {
	if pasted {
		in.pasteRetry++
	}
	g.mu.Lock()
	if g.staleLocked(in) {
		g.mu.Unlock()
		return
	}
	heldAt := in.heldSince
	if heldAt.IsZero() {
		heldAt = g.now()
	}
	in.heldSince = heldAt
	again := in.heldWhy == why
	in.heldWhy = why
	g.held[in.Name] = append([]*heldInput{{in: in, heldAt: heldAt, why: why, kind: kind, reported: in.heldReported, reportedPrompt: in.heldReportedPrompt}}, g.held[in.Name]...)
	delete(g.inflight, in.Name) // held again: it is counted there, not twice
	g.noteDialogLocked(in.Name, kind)
	start := !g.running
	g.running = true
	g.mu.Unlock()
	if !again {
		g.logHeld(in, why, "paste held")
	}
	if start {
		g.startLoop()
	}
}

// holdEnter holds the Enter of a send whose text was typed but whose Enter was
// withheld, first in the agent's queue: it belongs to the text already typed.
func (g *inputGate) holdEnter(in AutomatedInput, kind string) {
	enter := AutomatedInput{Term: in.Term, Name: in.Name, AgentType: in.AgentType, SessionID: in.SessionID,
		Key: in.Key + "#enter", Text: "[Enter] " + in.Text, What: in.What + " (its Enter)",
		Send: func(ctx context.Context) error {
			es, ok := in.Term.(EnterSender)
			if !ok {
				return errors.New("this terminal can't press Enter on its own")
			}
			return es.SendEnter(ctx, in.Name, in.AgentType, in.SessionID)
		}}
	enter.gen = in.gen
	g.mu.Lock()
	if g.staleLocked(in) {
		g.mu.Unlock()
		return
	}
	heldAt := in.heldSince // a held send's Enter keeps its age and report
	if heldAt.IsZero() {
		heldAt = g.now()
	}
	enter.heldSince, enter.heldReported, enter.heldReportedPrompt = heldAt, in.heldReported, in.heldReportedPrompt
	g.held[in.Name] = append([]*heldInput{{in: enter, heldAt: heldAt, why: "a prompt opened before its Enter", kind: kind, reported: in.heldReported, reportedPrompt: in.heldReportedPrompt}}, g.held[in.Name]...)
	delete(g.inflight, in.Name) // held again: it is counted there, not twice
	start := !g.running
	g.running = true
	g.mu.Unlock()
	g.logHeld(enter, "a prompt opened between its text and its Enter", "Enter withheld")
	if start {
		g.startLoop()
	}
}

// AutoAcceptSend is the one automated path allowed to type into an open
// prompt: the opt-in auto-accept feature answering an approval it judged safe.
// It is logged in the input debug log. Nothing else may call it (a test
// enforces that).
func AutoAcceptSend(ctx context.Context, name string, send func(ctx context.Context) error) error {
	inputlog.Record(name, inputlog.Keys, "[auto-accept answers an approval prompt]")
	return send(ctx)
}
