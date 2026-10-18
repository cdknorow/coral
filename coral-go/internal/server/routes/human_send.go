package routes

import (
	"context"
	"errors"
	"time"

	"github.com/cdknorow/coral/internal/ptymanager"
)

// A person's message to an agent, from Chat (#4587, coral-hub's O2). Typed while the agent was still starting, it
// could land in the input box with its Enter lost (Codex 0.155.1 drops an Enter that arrives in its first half second,
// verified against a stand-in model: the text sits in the box until someone presses Enter), or be lost altogether
// (Codex 0.163.0 on a PTY: the keystrokes never reached its box), and the dashboard said "Sent". So a message to an
// agent whose input box Coral recognises is delivered with care, and is "sent" only on evidence that it was submitted:
//   - while the agent is starting (it has sent no event yet) with neither a prompt nor its input box on screen, the
//     message waits for one of them, bounded, and is refused untyped if neither comes; a prompt on screen gets the
//     message as typed, as before, and nothing more (a person may answer a menu by typing its digit);
//   - the same text already sitting in the box (an earlier send of it that wasn't submitted) gets Enter, not a
//     second copy;
//   - after the text and its Enter, it is submitted when the agent says so (a prompt_submit event recorded since,
//     from its UserPromptSubmit hook, or agy's and Gemini's first PreInvocation), or when the text was seen in the
//     box and has left it (a busy Codex queues it). While the box still holds exactly that text, Enter is pressed
//     again (bounded); text still there after that is "not submitted". Text never seen in the box, with no event:
//     typed again (bounded) while the agent is still starting and its box is empty (its keystrokes were lost), else
//     "not confirmed". Never "sent" without one of the two. (A PTY's rendered screen can lag Codex's redraws, so the
//     screen alone said "not submitted" for a message the agent had taken: the event is checked first.)

var (
	// How long a message waits for a starting agent's input box.
	humanSendReadyWait = 10 * time.Second // the dashboard waits 20 s for a composer send's answer: this, plus the typings below, stays under it
	// How often the screen is read while waiting or confirming.
	humanSendPollEvery = 150 * time.Millisecond
	// How long the text may sit in the box after an Enter before the Enter is pressed again.
	humanSendEnterWait = 600 * time.Millisecond
	// Enters pressed again at most, after the send's own.
	humanSendExtraEnters = 3
	// How long a message never seen in the box waits for the agent's event before it is typed again, or not confirmed.
	humanSendConfirmWait = 2500 * time.Millisecond
	// Times a message lost while the agent started is typed again, at most.
	humanSendRetypes = 2
)

// errHumanSend is a message refused or not submitted: code and text for the dashboard.
type errHumanSend struct{ code, text string }

func (e *errHumanSend) Error() string { return e.text }

var (
	errSendNotReady     = &errHumanSend{"agent_not_ready", "The agent isn't ready for messages yet: your message was not sent. Try again in a moment."}
	errSendNotSubmit    = &errHumanSend{"not_submitted", "Your message is in the agent's input box but wasn't submitted. Send it again to submit it, or press Enter in its terminal."}
	errSendNotConfirmed = &errHumanSend{"not_confirmed", "The agent didn't confirm your message: it may not have been sent. Check its terminal before sending it again."}
)

// humanScreen is the agent's screen as text, for its input box: the PTY's rendered (its output is not the
// screen as drawn), else the terminal's own. ok is false when this terminal can't show it.
func (h *SessionsHandler) humanScreen(ctx context.Context, name, agentType, sessionID string) (string, bool) {
	if rs, ok := h.terminal.(ptymanager.RenderedScreenReader); ok {
		s, err := rs.RenderedScreen(ctx, name, agentType, sessionID)
		return s, err == nil
	}
	if sr, ok := h.terminal.(ptymanager.ScreenReader); ok {
		s, err := sr.AgentScreen(ctx, name, agentType, sessionID)
		return s, err == nil
	}
	return "", false
}

// deliverHumanSend types a person's message into the agent's input box and submits it, with the care above. A
// terminal whose screen can't be read, or an agent whose input box Coral doesn't know, gets the plain send.
func (h *SessionsHandler) deliverHumanSend(ctx context.Context, name, agentType, sessionID, text string) error {
	send := func() error { return h.terminal.SendInput(ctx, name, text, agentType, sessionID) }
	if !ptymanager.InputBoxKnown(agentType) {
		return send()
	}
	screen, ok := h.humanScreen(ctx, name, agentType, sessionID)
	if !ok {
		return send()
	}
	// Before typing: a starting agent with neither a prompt nor its input box on screen yet is waited for.
	starting := h.agentHasNoEvents(ctx, sessionID)
	deadline := time.Now().Add(humanSendReadyWait)
	for starting {
		if open, _ := ptymanager.PromptOnScreen(screen); open || ptymanager.InputBoxOnScreen(screen) {
			break
		}
		if time.Now().After(deadline) {
			return errSendNotReady
		}
		if err := sleepCtx(ctx, humanSendPollEvery); err != nil {
			return err
		}
		screen, _ = h.humanScreen(ctx, name, agentType, sessionID)
	}
	if open, _ := ptymanager.PromptOnScreen(screen); open {
		return send() // an answer to the prompt, typed as before: no input box to confirm it in
	}
	es, canEnter := h.terminal.(ptymanager.EnterSender)
	base := h.latestEventID(ctx, sessionID)
	// The same text already in the box: an earlier send of it that wasn't submitted. It gets Enter; typing it again
	// would put a second copy in the box.
	seen := canEnter && ptymanager.TextInInputBox(screen, text)
	if seen {
		if err := es.SendEnter(ctx, name, agentType, sessionID); err != nil {
			return err
		}
	} else if err := send(); err != nil {
		return err
	}
	extra, retyped := 0, 0
	typedAt, enteredAt := time.Now(), time.Now()
	for {
		if err := sleepCtx(ctx, humanSendPollEvery); err != nil {
			return err
		}
		if h.promptSubmittedAfter(ctx, sessionID, base) {
			return nil
		}
		screen, ok := h.humanScreen(ctx, name, agentType, sessionID)
		if !ok {
			return nil // the screen can no longer be read: as for a terminal that never could
		}
		if !ptymanager.TextInInputBox(screen, text) {
			if seen {
				return nil // it was in the box and has left it: submitted (or queued behind the agent's work)
			}
			if time.Since(typedAt) < humanSendConfirmWait {
				continue
			}
			if retyped < humanSendRetypes && h.agentHasNoEvents(ctx, sessionID) && ptymanager.InputBoxEmpty(screen) {
				if err := send(); err != nil { // its keystrokes were lost while it started: none of it is in the box
					return err
				}
				retyped++
				typedAt, enteredAt = time.Now(), time.Now()
				continue
			}
			return errSendNotConfirmed
		}
		seen = true
		if time.Since(enteredAt) < humanSendEnterWait {
			continue
		}
		if !canEnter || extra >= humanSendExtraEnters {
			if h.promptSubmittedAfter(ctx, sessionID, base) {
				return nil
			}
			return errSendNotSubmit
		}
		if open, _ := ptymanager.PromptOnScreen(screen); open {
			return errSendNotSubmit // never an Enter into a prompt that opened over the box
		}
		if err := es.SendEnter(ctx, name, agentType, sessionID); err != nil {
			return err
		}
		extra++
		enteredAt = time.Now()
	}
}

// latestEventID is the id of the session's newest event (0 with none): a later prompt_submit is this send's.
func (h *SessionsHandler) latestEventID(ctx context.Context, sessionID string) int64 {
	if sessionID == "" || h.ts == nil {
		return 0
	}
	events, err := h.ts.ListAgentEvents(ctx, "", 1, &sessionID)
	if err != nil || len(events) == 0 {
		return 0
	}
	return events[0].ID
}

// promptSubmittedAfter reports whether the agent recorded taking a prompt (prompt_submit) since the event base.
func (h *SessionsHandler) promptSubmittedAfter(ctx context.Context, sessionID string, base int64) bool {
	if sessionID == "" || h.ts == nil {
		return false
	}
	events, err := h.ts.ListAgentEvents(ctx, "", 20, &sessionID)
	if err != nil {
		return false
	}
	for _, e := range events {
		if e.ID > base && e.EventType == "prompt_submit" {
			return true
		}
	}
	return false
}

// agentHasNoEvents reports whether the session has sent no state event yet: the agent is still starting.
func (h *SessionsHandler) agentHasNoEvents(ctx context.Context, sessionID string) bool {
	if sessionID == "" {
		return false
	}
	events, err := h.ts.GetSessionStateEvents(ctx, []string{sessionID})
	return err == nil && len(events[sessionID]) == 0
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// humanSendError is the dashboard's answer for a refused or unsubmitted message, or false for another error.
func humanSendError(err error) (int, map[string]any, bool) {
	var e *errHumanSend
	if !errors.As(err, &e) {
		return 0, nil, false
	}
	return 409, map[string]any{"error": e.text, "code": e.code, "not_sent": e.code != "not_submitted"}, true
}
