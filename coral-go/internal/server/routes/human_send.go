package routes

import (
	"context"
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	at "github.com/subgentic/coral/internal/agenttypes"
	"github.com/subgentic/coral/internal/jsonl"
	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/terminalinput"
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
//   - a message with attached images (the composer puts their upload paths first) waits longer for its event: the CLI
//     reads the images before its hook (#4794). And to an agent in the middle of a turn (its newest event a prompt
//     taken or a tool used), a message no longer in its box once the wait is over was queued behind that work: the
//     CLI takes it, with its UserPromptSubmit, only when the turn ends. Sent.
// One message at a time per agent (Security's MUST on #4587): the steps above assume they are the box's only writer.
// Two sends at once typed both texts into one box ("deploy to stagingrm -rf the build"), each then saw its own text
// gone and both said "sent" with nothing submitted. A second send waits for the first to be confirmed or refused and
// starts from a fresh screen; waiters are bounded (humanSendMaxWaiting per agent, humanSendMaxInFlight in all), over
// that it is refused untyped (409 agent_busy), and a cancelled request gives up its place. A message left in the box
// unsubmitted is remembered by its mark (a hash, its length and at most 200 characters, not the message): while it is still there, a different message is refused untyped (409 box_not_empty)
// rather than typed after it; the same message gets Enter, as above.
// A slash command (/compact, /clear, /model: one line, a slash and a command name) is the CLI's own: it sends no
// UserPromptSubmit, and its Enter often takes it out of the box before the screen is read, so the rule above said
// "not confirmed" for a command that ran (#4705). For a command any event from the agent since it was typed (a
// /clear's SessionStart) counts, as does a menu it opens (/model's picker); with neither, a command no longer in the
// box is sent once the wait for an event is over, from an agent that has started (one still starting with its box
// empty gets it typed again, then "not confirmed", as a message does). A command still in the box after the Enters
// is "not submitted", as a message is.

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
	// The same for a message with attached images (#4794): the CLI reads the images before its UserPromptSubmit hook.
	humanSendAttachConfirmWait = 6 * time.Second
	// Times a message lost while the agent started is typed again, at most.
	humanSendRetypes = 1
	// Messages waiting behind the one being delivered to an agent, at most; and messages being delivered or waiting, in all.
	humanSendMaxWaiting  = 4
	humanSendMaxInFlight = 32
)

// humanSendGate lets one message at a time through to each agent. Its zero value is ready.
type humanSendGate struct {
	mu        sync.Mutex
	agents    map[string]*humanSendTurn
	inFlight  int
	leftovers map[string]ptymanager.BoxMark // agent -> a message of ours left in its box unsubmitted: its mark, never its text
}

// leftover is the mark of the message last left unsubmitted in the agent's box (zero for none); setLeftover records
// or (a zero mark) clears it.
func (g *humanSendGate) leftover(key string) ptymanager.BoxMark {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.leftovers[key]
}

func (g *humanSendGate) setLeftover(key string, m ptymanager.BoxMark) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if m.IsZero() {
		delete(g.leftovers, key)
		return
	}
	if g.leftovers == nil {
		g.leftovers = map[string]ptymanager.BoxMark{}
	}
	g.leftovers[key] = m
}

// humanSendTurn is one agent's turn: a one-slot channel held by the message being delivered, and how many hold or
// wait for it.
type humanSendTurn struct {
	slot  chan struct{}
	count int
}

// enter waits for the agent's turn (key: its session id, else its terminal name). It refuses at once, with
// errSendBusy, when humanSendMaxWaiting messages already wait for this agent or humanSendMaxInFlight are under way in
// all, and gives up its place when ctx ends. The returned leave ends the turn.
func (g *humanSendGate) enter(ctx context.Context, key string) (leave func(), err error) {
	g.mu.Lock()
	t := g.agents[key]
	if t == nil {
		t = &humanSendTurn{slot: make(chan struct{}, 1)}
		if g.agents == nil {
			g.agents = map[string]*humanSendTurn{}
		}
		g.agents[key] = t
	}
	if t.count > humanSendMaxWaiting || g.inFlight >= humanSendMaxInFlight {
		if t.count == 0 {
			delete(g.agents, key)
		}
		g.mu.Unlock()
		return nil, errSendBusy
	}
	t.count++
	g.inFlight++
	g.mu.Unlock()
	done := func() {
		g.mu.Lock()
		t.count--
		g.inFlight--
		if t.count == 0 {
			delete(g.agents, key)
		}
		g.mu.Unlock()
	}
	select {
	case t.slot <- struct{}{}:
		return func() { <-t.slot; done() }, nil
	case <-ctx.Done():
		done()
		return nil, ctx.Err()
	}
}

// errHumanSend is a message refused or not submitted: code and text for the dashboard.
type errHumanSend struct{ code, text string }

func (e *errHumanSend) Error() string { return e.text }

var (
	errSendNotReady     = &errHumanSend{"agent_not_ready", "The agent isn't ready for messages yet: your message was not sent. Try again in a moment."}
	errSendNotSubmit    = &errHumanSend{"not_submitted", "Your message is in the agent's input box but wasn't submitted. Send it again to submit it, or press Enter in its terminal."}
	errSendNotConfirmed = &errHumanSend{"not_confirmed", "The agent didn't confirm your message: it may not have been sent. Check its terminal before sending it again."}
	// An agent whose hook programs are gone (the app it started from was replaced) takes the message but cannot say so: said plainly, not as "may not have been sent" (#6089).
	errSendHooksGone   = &errHumanSend{"hooks_unreachable", "Coral can't reach this agent's hooks, so it can't tell whether your message arrived. Check the agent's terminal. To fix this, reopen Coral or restart the agent."}
	errSendHooksReview = &errHumanSend{"codex_hooks_review_pending", "Answer Codex's question about Coral's hooks above first: your message was not sent. Trust them only if you want them to run outside Codex's sandbox."}
	errSendBusy        = &errHumanSend{"agent_busy", "Other messages to this agent are still being sent: yours was not sent. Try again in a moment."}
	errSendLeftover    = &errHumanSend{"box_not_empty", "An earlier message is still in the agent's input box, not submitted: yours was not sent. Send that one again, or clear it in its terminal."}
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
func (h *SessionsHandler) deliverHumanSend(ctx context.Context, name, agentType, sessionID, text string) (err error) {
	// A person's text: the input debug log keeps its length only (#60 review N3), whatever the terminal: the origin is passed to every send below.
	key := sessionID
	if key == "" {
		key = name
	}
	leave, err := h.sendGate.enter(ctx, key)
	if err != nil {
		return err
	}
	defer leave()
	lo := h.sendGate.leftover(key)
	defer func() {
		if err == nil {
			ptymanager.NoteOperatorText(name, text) // the chat page then knows this turn is a person's, not a notice by its words (#5482)
		}
		if err == nil && agentType == at.Claude && strings.HasPrefix(strings.TrimSpace(text), "/") {
			h.hints.noteSlash(sessionID) // a command like /model may open a dialog that sends no hook event: the chat reads this screen for a prompt for a while (#5488)
		}
		switch {
		case err == errSendNotSubmit:
			h.sendGate.setLeftover(key, ptymanager.MarkOf(text))
		case err == nil && lo.Of(text):
			h.sendGate.setLeftover(key, ptymanager.BoxMark{}) // the leftover itself, submitted now
		}
	}()
	send := func() error { return h.terminal.SendInput(ctx, terminalinput.Person, name, text, agentType, sessionID) }
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
	if ptymanager.CodexHooksReviewOnScreen(screen) && !hooksReviewAnswerRe.MatchString(strings.TrimSpace(text)) {
		return errSendHooksReview // not an answer to the question: its Enter would pick "Review hooks" and the message would be lost
	}
	if open, _ := ptymanager.PromptOnScreen(screen); open {
		return send() // an answer to the prompt, typed as before: no input box to confirm it in
	}
	if !lo.IsZero() && !lo.Of(text) {
		if lo.InBox(screen) {
			return errSendLeftover
		}
		h.sendGate.setLeftover(key, ptymanager.BoxMark{}) // submitted or cleared since
	}
	es, canEnter := h.terminal.(ptymanager.EnterSender)
	base, busy := h.latestEvent(ctx, sessionID)
	confirmWait := humanSendConfirmWait
	if attachesImages(text) {
		confirmWait = humanSendAttachConfirmWait // its hook comes once the CLI has read the images (#4794)
	}
	// The same text already in the box: an earlier send of it that wasn't submitted. It gets Enter; typing it again
	// would put a second copy in the box.
	before := screen                                   // the screen before anything is typed: a send that never arrived leaves it as it is
	hooksGone := h.hooksUnreachable(ctx, sessionID)    // its hook programs are gone: no event will come for this message, so other evidence is all there is (#6089)
	tx := h.transcriptTotal(ctx, sessionID, agentType) // the agent's own transcript, before the typing: a message it took is added to it
	seen := canEnter && ptymanager.TextInInputBox(screen, text)
	if seen {
		if err := es.SendEnter(ctx, name, agentType, sessionID); err != nil {
			return err
		}
	} else if err := send(); err != nil {
		return err
	}
	command := slashCommandRe.MatchString(text)
	extra, retyped := 0, 0
	typedAt, enteredAt := time.Now(), time.Now()
	for {
		if err := sleepCtx(ctx, humanSendPollEvery); err != nil {
			return err
		}
		if h.promptSubmittedAfter(ctx, sessionID, base) {
			return nil
		}
		// One reading of the clock for this pass: the transcript is looked at once the confirm wait is over, and the pass may only give up on the message if it looked. Two readings (this one and the one
		// that guards the give-up below) could straddle the end of the wait, so a pass that had not yet looked at the transcript went on to say "not confirmed" (#6089: a CI run did).
		waited := time.Since(typedAt) >= confirmWait
		if (hooksGone || waited) && tx.tookPrompt(h, ctx, sessionID, agentType, text) {
			return nil // the transcript gained the message: the agent took it, whatever its hooks said (#6089)
		}
		screen, ok := h.humanScreen(ctx, name, agentType, sessionID)
		if !ok {
			return nil // the screen can no longer be read: as for a terminal that never could
		}
		if !ptymanager.TextInInputBox(screen, text) {
			if command && h.eventAfter(ctx, sessionID, base, func(string) bool { return true }) {
				return nil // out of the box, and the agent did something since: the command ran (a /clear's SessionStart)
			}
			if seen {
				return nil // it was in the box and has left it: submitted (or queued behind the agent's work)
			}
			if open, _ := ptymanager.PromptOnScreen(screen); command && open {
				return nil // the command opened its own menu (/model's picker): it ran
			}
			if !waited {
				continue
			}
			if busy && ptymanager.InputBoxEmpty(screen) {
				return nil // typed to an agent in the middle of a turn, and its box is empty: queued behind its work (#4794). A box
				// holding anything else (an image chip and the text the match above could not read, a draft) may still hold the message.
			}
			if (h.agentHasNoEvents(ctx, sessionID) || hooksGone) && ptymanager.InputBoxEmpty(screen) {
				// An empty box is also what a submitted message looks like, and an agent whose hooks are not reaching Coral (or
				// that has none yet) sends no event for it. So a message counts as taken only on evidence of its text: the
				// agent's echo of it, on the screen more times than before the typing. A screen that merely changed (a spinner,
				// a clock) is no evidence: its keystrokes may have been lost.
				if echoedSince(before, screen, text) {
					return nil
				}
				if len(echoKey(text)) < echoMinChars && !command && screen != before {
					return nil // too short to search for: a screen that moved is all there is to go on
				}
				if !command && retyped < humanSendRetypes {
					if err := send(); err != nil { // no trace of it: its keystrokes were lost while it started, and none of it is in the box
						return err
					}
					retyped++
					typedAt, enteredAt = time.Now(), time.Now()
					continue
				}
				// A command is never typed again: /clear or /compact acts on the agent, and the person asked for it once.
			}
			if command && !h.agentHasNoEvents(ctx, sessionID) {
				return nil // a CLI's own command sends no hook: out of the box, with nothing against it, it ran
			}
			if hooksGone {
				return errSendHooksGone // nothing confirms it, and nothing could: say why, not that it may be lost
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

// echoMinChars is the shortest text echoedSince searches for.
const echoMinChars = 4

// noSpace is s with all whitespace removed: a wrap in the middle of a word, or a TUI's indent on continuation lines, cannot hide an echo
// compared this way.
func noSpace(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
}

// echoKey is what a message's echo on the screen is searched for by: the start of its first non-empty line, without whitespace, at most 32
// characters (a long or multi-line message wraps on screen, so the whole text would not be found).
func echoKey(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if f := noSpace(line); f != "" {
			if r := []rune(f); len(r) > 32 {
				f = string(r[:32])
			}
			return f
		}
	}
	return ""
}

// echoedSince reports whether the message's echo is on the screen more times than it was on the screen before the typing (an earlier
// message of the same words does not count). Both screens are compared without whitespace.
func echoedSince(before, now, text string) bool {
	key := echoKey(text)
	if len([]rune(key)) < echoMinChars {
		return false
	}
	return strings.Count(noSpace(now), key) > strings.Count(noSpace(before), key)
}

// hooksReviewAnswerRe is a person picking one of the hooks-review dialog's three options by its digit, which is the dialog's own way
// to answer (the digit alone selects it): the only thing typed into that dialog, because it is the person's choice.
var hooksReviewAnswerRe = regexp.MustCompile(`^[1-3]$`)

// slashCommandRe is a CLI command: a slash, a name, then nothing or arguments on the same line ("/tmp/x is
// missing" is a message: its first word has a second slash).
var slashCommandRe = regexp.MustCompile(`^/[A-Za-z][A-Za-z0-9_:.-]*(?:[ \t][^\n]*)?$`)

// latestEvent is the id of the session's newest event (0 with none): a later prompt_submit is this send's. busy is
// whether that event leaves the agent in the middle of a turn (a prompt taken, or a tool used): a message typed
// then is queued by the CLI and taken when the turn ends, with its UserPromptSubmit only then.
func (h *SessionsHandler) latestEvent(ctx context.Context, sessionID string) (id int64, busy bool) {
	if sessionID == "" || h.ts == nil {
		return 0, false
	}
	events, err := h.ts.ListAgentEvents(ctx, "", 1, &sessionID)
	if err != nil || len(events) == 0 {
		return 0, false
	}
	return events[0].ID, events[0].EventType == "prompt_submit" || events[0].EventType == "tool_use"
}

// attachesImages reports whether a composer message has images attached: the composer puts their upload paths first
// ("<uploads>/a.png <uploads>/b.png the text").
func attachesImages(text string) bool {
	if uploadDir == "" {
		return false
	}
	prefix := filepath.ToSlash(uploadDir) + "/"
	if !strings.HasPrefix(text, prefix) {
		return false
	}
	name, _, _ := strings.Cut(text[len(prefix):], " ") // an upload's own name has no space, and no folder
	return name != "" && !strings.Contains(name, "/")
}

// promptSubmittedAfter reports whether the agent recorded taking a prompt (prompt_submit) since the event base.
func (h *SessionsHandler) promptSubmittedAfter(ctx context.Context, sessionID string, base int64) bool {
	return h.eventAfter(ctx, sessionID, base, func(typ string) bool { return typ == "prompt_submit" })
}

// eventAfter reports whether the agent recorded an event that match accepts since the event base.
func (h *SessionsHandler) eventAfter(ctx context.Context, sessionID string, base int64, match func(eventType string) bool) bool {
	if sessionID == "" || h.ts == nil {
		return false
	}
	events, err := h.ts.ListAgentEvents(ctx, "", 20, &sessionID)
	if err != nil {
		return false
	}
	for _, e := range events {
		if e.ID > base && match(e.EventType) {
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
	return 409, map[string]any{"error": e.text, "code": e.code, "not_sent": e.code != "not_submitted" && e.code != "hooks_unreachable"}, true
}

// transcriptMark is how many messages the agent's own transcript held before a message was typed (#6089): the transcript is a signal that does not depend on the agent's hooks.
type transcriptMark struct {
	total int
	ok    bool
}

func (h *SessionsHandler) transcriptTotal(ctx context.Context, sessionID, agentType string) transcriptMark {
	tv := h.readSendTranscript(ctx, sessionID, agentType)
	if tv == nil {
		return transcriptMark{}
	}
	return transcriptMark{total: tv.Total, ok: true}
}

// sendTranscriptReader reads the transcript for a send's confirmation; a variable so a test can stand in for an agent's transcript file.
var sendTranscriptReader = func(h *SessionsHandler, ctx context.Context, sessionID, agentType string) *jsonl.Transcript {
	return h.readLiveTranscript(ctx, sessionID, agentType)
}

func (h *SessionsHandler) readSendTranscript(ctx context.Context, sessionID, agentType string) *jsonl.Transcript {
	return sendTranscriptReader(h, ctx, sessionID, agentType)
}

func (h *SessionsHandler) readLiveTranscript(ctx context.Context, sessionID, agentType string) *jsonl.Transcript {
	if h.jsonl == nil || h.ss == nil || sessionID == "" {
		return nil
	}
	ls, err := h.ss.GetLiveSession(ctx, sessionID)
	if err != nil || ls == nil {
		return nil
	}
	return h.jsonl.ReadTranscriptForLiveContext(ctx, sessionID, ls.WorkingDir, agentType)
}

// tookPrompt reports whether the transcript gained a user message that is this text since the mark.
func (m transcriptMark) tookPrompt(h *SessionsHandler, ctx context.Context, sessionID, agentType, text string) bool {
	if !m.ok {
		return false
	}
	tv := h.readSendTranscript(ctx, sessionID, agentType)
	if tv == nil || tv.Total <= m.total {
		return false
	}
	return transcriptHasUserText(tv.Window, tv.Total-m.total, text)
}

// transcriptHasUserText reports whether one of the last n messages is a user message holding the text (its echo key, as on a screen: a long message is compared by its start).
func transcriptHasUserText(window []map[string]any, n int, text string) bool {
	key := echoKey(text)
	if len([]rune(key)) < echoMinChars {
		return false
	}
	if n > len(window) {
		n = len(window)
	}
	for _, m := range window[len(window)-n:] {
		if t, _ := m["type"].(string); t != "user" {
			continue
		}
		if strings.Contains(noSpace(messageText(m["content"])), key) {
			return true
		}
	}
	return false
}

// messageText is a transcript message's text: a string, or the text blocks of a list.
func messageText(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		var b strings.Builder
		for _, part := range c {
			switch p := part.(type) {
			case string:
				b.WriteString(p)
			case map[string]any:
				if t, ok := p["text"].(string); ok {
					b.WriteString(t)
				}
			}
			b.WriteByte(' ')
		}
		return b.String()
	}
	return ""
}
