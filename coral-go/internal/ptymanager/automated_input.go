package ptymanager

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// The automated-input gate (#3431).
//
// Coral types automated text into agents: board notifications and nudges, task
// notifications, message-wait results, the remote poller, a launch's first
// prompt, agent UI notices. Typed while the agent sits at a prompt for the user
// (a permission or selection menu, an approval question, the trust-this-folder
// screen, a model or login picker), the text plus Enter answers that prompt, and
// on some options auto-accepts it with no human decision. So every automated
// send goes through DeliverAutomated. It types only when the agent is NOT
// waiting on a prompt: neither its state (hook events, NeedsInputFunc) nor its
// screen, read just before typing, shows one. Otherwise the send is held and
// delivered once the prompt is gone, exactly once; a later send with the same
// Key replaces a held one (a newer unread count supersedes an older one).
//
// Explicit human input never goes through here: the operator typing in the
// terminal or the dashboard, and a person picking a prompt option
// (prompt_answer.go). The opt-in auto-accept feature answers approval prompts on
// purpose and uses the AutoAcceptSend exemption, which nothing else may use.

// Delivery says what DeliverAutomated did with a send.
type Delivery int

const (
	// Sent: the text was typed now.
	Sent Delivery = iota
	// Held: the agent is waiting on a prompt; the text is typed once it isn't.
	Held
)

func (d Delivery) String() string {
	if d == Held {
		return "held"
	}
	return "sent"
}

// AutomatedInput is one automated send.
type AutomatedInput struct {
	// Term is the terminal or runtime the text goes to, read for the screen
	// (ScreenReader).
	Term any
	// Name, AgentType and SessionID identify the agent's session.
	Name, AgentType, SessionID string
	// Key identifies what the send is about. A send with the same session and
	// Key replaces a held one; "" means the text itself.
	Key string
	// Text is what will be typed, for the input debug log.
	Text string
	// What names the sender in logs ("board message", "initial prompt", ...).
	What string
	// Send types the text. It runs only when nothing is waiting on the user.
	Send func(ctx context.Context) error
	// Mention: the send is about a board message that @-mentions this agent (by the board's
	// own rule, numbered names included). In signals mode it is typed in mid-turn, as with the
	// screen rule; a routine nudge or message waits for the turn to end (#3578, the Operator's
	// decision on #3494). A prompt or a menu on screen still holds it.
	Mention bool
	// RequireInputBox holds the send until the agent's normal input box is on
	// screen, not just until no prompt is: a launch's first prompt, which a
	// still-drawing trust or login screen could otherwise take as its answer.
	// After the held-too-long time it falls back to the usual rule (no prompt
	// open), so an input box Coral doesn't recognise can't hold a launch for
	// ever; an open prompt still holds it.
	RequireInputBox bool
	// Paste sends Text as one bracketed paste and then Enter where the agent's
	// terminal can (PromptSender, with bracketed paste on), so a menu that opens just
	// before it ignores it instead of taking it as an answer (#3457); Send is then
	// the fallback, for a terminal or agent without bracketed paste. Ready, when
	// set, runs just before the paste (the caller's has-the-agent-exited check).
	Paste bool
	Ready func(ctx context.Context) error

	// pasteRetry: a held paste send. Its text may already be in the input box
	// (only its Enter is left), or a menu dropped it (pasted again, once).
	pasteRetry int
	// hadPlaceholder: the box held a "[Pasted ...]" placeholder before Coral's paste
	// (a person's), so one is never taken as ours.
	hadPlaceholder bool
	// entered: its text was pasted and its Enter pressed, but the text was still in the box
	// afterwards (#4104: a dialog took the Enter). Its next turn presses only the Enter, never
	// pastes again, and an empty box then means the Enter took effect late: delivered.
	// enterTries counts those Enters with no dialog up; after maxEnterTries it is given up.
	entered    bool
	enterTries int
	// urgent: an @-mention waits behind this held send (#3708), so a paste of Coral's that
	// can't be confirmed gives up after pasteStuckAfterUrgent instead of pasteStuckAfter.
	urgent bool
	// heldSince, heldWhy and heldReported carry a held send's first hold time, its
	// reason and whether it was reported across its turns (a paste send is taken out
	// of the queue and held again on each): it ages to the held-too-long notice and
	// the expiry, and is logged once per reason (#3468).
	heldSince    time.Time
	heldWhy      string
	heldReported bool
	// heldReportedPrompt: it was reported as held by a prompt (prompt=true). A send
	// first reported as an input-box hold is reported again once a prompt holds it.
	heldReportedPrompt bool
	// gen is the session's DropHeld generation when the send reached the gate: a send
	// in flight while DropHeld ran (a turn that takes it off the queue and holds it
	// again) is never held again for the new process (#3468 v2).
	gen uint64
}

// EnterSender is implemented by terminals that can press Enter in an agent's
// terminal: a send whose Enter was withheld because a prompt opened between its
// text and its Enter (tmux.ErrEnterWithheld) holds just that Enter.
type EnterSender interface {
	SendEnter(ctx context.Context, name, agentType, sessionID string) error
}

// ArrowSender is implemented by terminals that can press Up or Down in an agent's terminal: what moves the
// selection of a menu whose options carry no numbers (Claude Code's folder-trust and API-key menus, #4543).
type ArrowSender interface {
	SendArrow(ctx context.Context, name, agentType, sessionID string, up bool) error
}

// ScreenReader is implemented by terminals that can show an agent's visible
// screen (the bottom of its tmux pane, or its PTY output).
type ScreenReader interface {
	AgentScreen(ctx context.Context, name, agentType, sessionID string) (string, error)
}

// gateTimings are the gate's intervals (read under the gate's lock; tests shorten them).
type gateTimings struct {
	// tooLong is how long a send waits before HeldTooLongFunc is told.
	tooLong time.Duration
	// expiry drops a nudge or notification still held after this long (the
	// prompt was never answered): a day-old nudge typed later is noise. A
	// first prompt (RequireInputBox) has no time limit; it goes with its agent
	// (DropHeld).
	expiry time.Duration
	// dialogReport: a send held behind a dialog (#4104) is reported after this, not tooLong:
	// the user has something to dismiss now. dialogMentionGiveUp: a mention held behind one is
	// given up after this (never pasted, never typed into the dialog). Zero: the defaults.
	dialogReport, dialogMentionGiveUp time.Duration
	// every is how often held sends are retried.
	every time.Duration
}

// heldPerSession bounds the held sends kept for one agent.
const heldPerSession = 50

// heldBytesPerSession caps the text of a session's held sends (#3578).
const heldBytesPerSession = 256 << 10

type heldInput struct {
	in       AutomatedInput
	heldAt   time.Time
	why      string
	kind     string // what holds it when it is a request to the user ("" otherwise; HeldTooLongFunc)
	reported bool
	// reportedPrompt: reported with prompt=true (a calm report is followed by a
	// prompt one if a prompt holds it later).
	reportedPrompt bool
}

type inputGate struct {
	mu   sync.Mutex
	held map[string][]*heldInput // by session name, oldest first
	// inflight: the send flushQueue took off held (it is removed before it is typed, so that it is typed once) and is trying now, per session. It is
	// still counted as held (HeldCount) until it is sent, held again or dropped: without it a session's held count read 0 for the length of every
	// retry (up to pasteVerifyWait), and the sessions list showed no held input (#5205).
	inflight map[string]*heldInput
	running  bool
	// The retry loop stops on request (StopGate: Coral's shutdown, and the tests, which must not
	// leave it sleeping): stop wakes a sleeping loop, stopping makes any loop that starts or wakes
	// meanwhile end at once, and loops counts the running loops so a stop can wait for them.
	stop     chan struct{}
	stopping bool
	loops    sync.WaitGroup
	sleeps   atomic.Int64                     // how many times the loop has begun a sleep (tests wait for it to be asleep)
	nowFn    atomic.Pointer[func() time.Time] // the clock; nil is time.Now. Tests replace it (setNow) while a loop of an earlier test may still read it
	// sending serialises the sends to one session (a direct send and the
	// retry loop), so held sends keep their order and none is typed twice.
	sending sync.Map // session name -> *sync.Mutex
	timings gateTimings
	// reportedFor: sessions with a send reported to HeldTooLongFunc, until none is
	// held (HeldEndedFunc).
	reportedFor map[string]AutomatedInput
	notes       notifier
	// gens counts DropHeld calls per session (AutomatedInput.gen).
	gens map[string]uint64
	// shadowLast: each held send's last logged screen and signal decisions (shadow
	// mode logs a send's decisions only when they change).
	shadowLast map[string]string
	// delivered: when each session's sends were last typed, by Key (LastDelivered).
	delivered map[string]map[string]time.Time
	// lastPasted: each session's last text Coral pasted, squeezed, until its Enter is seen to
	// submit it (#4104): left in the box (a dialog took the Enter), it is Coral's, not a draft.
	lastPasted map[string]string
	// lastPastedTypedAt: when lastPasted's text was typed, not pasted (bracketed paste off), so its Enter is unconfirmed: it is Coral's for ownTypedMaxAge only.
	lastPastedTypedAt map[string]time.Time
	// ownTexts: each session's recent texts the gate typed, squeezed (own_texts.go, #247): Coral's text left in the box is
	// recognised as its own, alone or several in a row, and submitted rather than held as a draft.
	ownTexts map[string][]ownText
	// repaintedAt: when each session was last asked to redraw because its input box wasn't on the rendered screen (#247).
	repaintedAt map[string]time.Time
	// dialogHeld: sessions whose sends were last held behind a dialog: the held notice stays
	// up (checkEnded) until a send is delivered again.
	dialogHeld map[string]bool
	// surveyTried: when each session was last sent the 0 that dismisses Claude's survey (dismissSurvey, #5114).
	surveyTried map[string]time.Time
}

// Defaults for gateTimings' dialog fields.
const (
	defaultDialogReport        = 10 * time.Second
	defaultDialogMentionGiveUp = 10 * time.Minute
)

// WhyDialogGaveUp starts the reason a mention held behind a dialog was given up (#4104).
const WhyDialogGaveUp = "a dialog was open in the agent's terminal for"

// WhyEnterGaveUp is the reason a paste was given up because its Enter never submitted it, with no
// dialog recognised (#4104): most likely one Coral doesn't know.
const WhyEnterGaveUp = "its Enter didn't submit it, three times"

// dialogReportAfter: g.mu must be held (it reads g.timings).
func (g *inputGate) dialogReportAfter() time.Duration {
	if g.timings.dialogReport > 0 {
		return g.timings.dialogReport
	}
	return defaultDialogReport
}

// dialogGiveUpAfter: g.mu must be held (it reads g.timings).
func (g *inputGate) dialogGiveUpAfter() time.Duration {
	if g.timings.dialogMentionGiveUp > 0 {
		return g.timings.dialogMentionGiveUp
	}
	return defaultDialogMentionGiveUp
}

// setLastPasted records (or, with "", forgets) the text Coral last pasted for name.
func (g *inputGate) setLastPasted(name, text string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.lastPasted == nil {
		g.lastPasted = map[string]string{}
	}
	delete(g.lastPastedTypedAt, name)
	if text == "" {
		delete(g.lastPasted, name)
		return
	}
	g.lastPasted[name] = squeeze(text)
}

func (g *inputGate) lastPastedFor(name string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.lastPasted[name]
}

// noteDialogLocked records whether name's latest hold was behind a dialog (g.mu held).
func (g *inputGate) noteDialogLocked(name, kind string) {
	if kind == KindDialog {
		if g.dialogHeld == nil {
			g.dialogHeld = map[string]bool{}
		}
		g.dialogHeld[name] = true
	}
}

var gate = &inputGate{held: map[string][]*heldInput{}, reportedFor: map[string]AutomatedInput{}, gens: map[string]uint64{}, shadowLast: map[string]string{}, delivered: map[string]map[string]time.Time{},
	timings: gateTimings{tooLong: 2 * time.Minute, expiry: time.Hour, every: 2 * time.Second}}

// now is the gate's clock.
func (g *inputGate) now() time.Time {
	if f := g.nowFn.Load(); f != nil {
		return (*f)()
	}
	return time.Now()
}
