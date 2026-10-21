package routes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	at "github.com/subgentic/coral/internal/agenttypes"
	"github.com/subgentic/coral/internal/auth"
	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/sessionstate"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/terminalinput"
	"github.com/subgentic/coral/internal/tmux"
)

// Auto-accept (#3182): when the operator turns it on for an agent, Coral
// answers that agent's approve-once permission prompts itself, server-side, so
// it works with the dashboard closed and through hubs.
//
// Only a prompt the agent type's table recognises as a tool approval is
// answered, and only with its approve-once option. Never chosen: "always
// allow", "don't ask again", "switch to auto mode"/"accept edits" (they change
// settings), or a text/chat option. Questions (AskUserQuestion, request_user_input),
// plan approval, high-risk actions (auto_accept_risk.go) and any unknown prompt
// shape wait for the user, recorded once as auto_accept_skipped.
//
// The digit is sent only after re-reading the screen and finding the same
// prompt (as AnswerPrompt does). A prompt is answered once (fingerprint
// debounce), and a loop guard turns auto-accept off when the same prompt keeps
// coming back or approvals run away.

// The approval tables, verified against real CLIs in a tmux pane:
//
//	Claude Code 2.1.296: "Do you want to proceed?" (Bash), "Do you want to create
//	notes.txt?" (Write), "Do you want to make this edit to notes.txt?" (Edit),
//	each with "1. Yes", "Yes, and ..." options and "No".
//	Codex 0.155.1: "Would you like to run the following command?" and "Would you
//	like to make the following edits?", with "1. Yes, proceed (y)", "Yes, and
//	don't ask again ..." and "No, and tell Codex what to do differently (esc)".
//	The digit answers it ("You approved codex to run ... this time").
//
// Gemini and Antigravity menus navigate by arrow keys and Enter.
var autoAcceptSupported = map[string]bool{
	at.Claude:      true,
	at.Codex:       true,
	at.Agy:         true,
	at.Antigravity: true,
	at.Gemini:      true,
}

// autoAcceptUnsupported is the feed code for an agent type without a verified
// approval table (the UI words it).
const autoAcceptUnsupported = "agent_type_unsupported"

// AutoAcceptSupported reports whether auto-accept is available for an agent type.
func AutoAcceptSupported(agentType string) bool { return autoAcceptSupported[agentType] }

// Tools whose prompt is a question or a plan for the user, never an approval.
var autoAcceptBlockingTools = map[string]string{
	"AskUserQuestion":    "a question waits for your answer",
	"ExitPlanMode":       "plan approval waits for you",
	"ask_question":       "a question waits for your answer",
	"request_user_input": "a question waits for your answer",
}

const (
	autoAcceptInterval    = 2 * time.Second
	autoAcceptDebounce    = 3 * time.Second
	autoAcceptLoopWindow  = time.Minute
	autoAcceptSameLimit   = 5   // the same prompt approved this often in a minute pauses it
	autoAcceptTotalLimit  = 30  // more approvals than this in a minute pauses it
	autoAcceptInputMaxLen = 200 // the command/input an event carries to the browser
	// A prompt left for the user is recorded once (per fingerprint, remembered
	// for autoAcceptSkipMemory), and at most one skipped event per session per
	// autoAcceptSkipEvery, so a screen that flaps between prompts cannot flood
	// the activity log.
	autoAcceptSkipMemory = 10 * time.Minute
	autoAcceptSkipEvery  = time.Minute
	// Enable time limits: a Cloud Hub enable defaults to an hour; nothing may
	// ask for more than 8 hours.
	autoAcceptHubDefault = time.Hour
	autoAcceptMaxLimit   = 8 * time.Hour
)

var (
	claudeApprovalQuestionRe = regexp.MustCompile(`(?i)^do you want to\b`)
	codexApprovalHeaderRe    = regexp.MustCompile(`^\s*Would you like to (run the following command|make the following edits)\?\s*$`)
	agyApprovalHeaderRe      = regexp.MustCompile(`^\s*Requesting permission for:`)
	agyApprovalQuestionRe    = regexp.MustCompile(`(?i)^(run this command\?|allow this action\?|do you want to run\b)`)
	// The start of a tool line, whose arguments may wrap onto the following lines.
	claudeToolStartRe = regexp.MustCompile(`^\s*⏺\s*([A-Za-z_][\w.-]*)\((.*)$`)
	ruleLineRe        = regexp.MustCompile(`^\s*─{8,}\s*$`)
)

// approvalDecision is what the table makes of a terminal screen.
type approvalDecision struct {
	Prompt  bool   // an option prompt is open
	Approve bool   // it is a recognised tool approval
	N       int    // the approve-once option's digit
	Label   string // its label
	Kind    string // codex: "command" or "edit"
	Context []string
	FP      string // fingerprint of the prompt (type, text, options)
	Screen  promptScreen
}

func normalizedLabel(s string) string { return normalizeLabel(s) }

// classifyApproval reads the prompt at the bottom of a capture and decides
// whether it is an approve-once tool approval for the agent type.
func classifyApproval(agentType, capture string) approvalDecision {
	screen, ok := parsePromptScreen(capture)
	if !ok {
		return approvalDecision{}
	}
	lines := strings.Split(strings.ReplaceAll(capture, "\r", ""), "\n")
	firstOpt := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if m := promptOptionRe.FindStringSubmatch(lines[i]); m != nil && m[2] == "1" && indentOf(lines[i]) < 5 {
			firstOpt = i
			break
		}
	}
	d := approvalDecision{Prompt: true, Screen: screen}
	var opts []string
	for _, o := range screen.Options {
		opts = append(opts, o.Label)
	}
	block := promptBlock(lines, firstOpt)
	d.Context = block
	d.FP = fingerprint(agentType, block, opts)

	pick := func(match func(string) bool) bool {
		hasNo := false
		for _, o := range screen.Options {
			if strings.HasPrefix(normalizedLabel(o.Label), "no") {
				hasNo = true
			}
		}
		if !hasNo {
			return false
		}
		for _, o := range screen.Options {
			if o.Action == "select" && !o.Wrapped && match(normalizedLabel(o.Label)) {
				d.N, d.Label, d.Approve = o.N, o.Label, true
				return true
			}
		}
		return false
	}
	switch agentType {
	case at.Claude:
		if !claudeApprovalQuestionRe.MatchString(screen.Question) {
			return d
		}
		pick(func(l string) bool { return l == "yes" })
	case at.Codex:
		header := -1
		for i := firstOpt - 1; i >= 0 && i >= firstOpt-40; i-- {
			if m := codexApprovalHeaderRe.FindStringSubmatch(lines[i]); m != nil {
				header = i
				d.Kind = "command"
				if strings.HasPrefix(m[1], "make") {
					d.Kind = "edit"
				}
				break
			}
		}
		if header < 0 {
			return d
		}
		pick(func(l string) bool { return l == "yes, proceed" || strings.HasPrefix(l, "yes, proceed (") })
	case at.Agy, at.Antigravity, at.Gemini:
		header := -1
		for i := firstOpt - 1; i >= 0 && i >= firstOpt-40; i-- {
			if agyApprovalHeaderRe.MatchString(lines[i]) {
				header = i
				d.Kind = "command"
				break
			}
		}
		if header < 0 && !agyApprovalQuestionRe.MatchString(screen.Question) {
			return d
		}
		if d.Kind == "" {
			d.Kind = "command"
		}
		pick(func(l string) bool {
			if strings.Contains(l, "always allow") || strings.Contains(l, "persist") {
				return false
			}
			return l == "yes, run command" || l == "yes, allow once" || l == "yes"
		})
	}
	return d
}

// promptBlock is the prompt's own text above its options: from the rule that
// opens Claude's prompt panel (or Codex's header line) down to the first option,
// without blank lines. It is what the fingerprint covers, so a different
// command or file is a different prompt.
func promptBlock(lines []string, firstOpt int) []string {
	if firstOpt < 0 {
		return nil
	}
	start := firstOpt - 40
	if start < 0 {
		start = 0
	}
	for i := firstOpt - 1; i >= start; i-- {
		if ruleLineRe.MatchString(lines[i]) || codexApprovalHeaderRe.MatchString(lines[i]) || agyApprovalHeaderRe.MatchString(lines[i]) {
			start = i
			if ruleLineRe.MatchString(lines[i]) {
				start = i + 1
			}
			break
		}
	}
	var out []string
	for _, l := range lines[start:firstOpt] {
		if t := strings.TrimSpace(l); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func fingerprint(agentType string, block, opts []string) string {
	h := sha256.New()
	h.Write([]byte(agentType))
	for _, l := range block {
		h.Write([]byte{0})
		h.Write([]byte(strings.Join(strings.Fields(l), " ")))
	}
	for _, o := range opts {
		h.Write([]byte{1})
		h.Write([]byte(normalizeLabel(o)))
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// ── Engine ─────────────────────────────────────────────────────────────────

// AutoAcceptor answers approval prompts for sessions with auto-accept on. One
// goroutine handles every session, so a session is never answered twice at
// once. Claude sessions are checked when their permission notification
// arrives (Kick) and on every sweep while they still need input; Codex has no
// permission hook, so its screen is read on each sweep. Each sweep also ends
// enables whose time limit ran out.
type AutoAcceptor struct {
	h    *SessionsHandler
	kick chan struct{}
	now  func() time.Time
	// wallNow stamps approvals (real time: events sort by it); tests pin it to force a tie.
	wallNow func() time.Time

	mu       sync.Mutex
	answered map[string]autoAcceptAnswer   // session -> last answer (debounce)
	history  map[string][]autoAcceptAnswer // session -> answers in the loop window
	skipped  map[string]map[string]time.Time
	lastSkip map[string]time.Time // session -> last skipped event
}

type autoAcceptAnswer struct {
	fp string
	at time.Time
}

// NewAutoAcceptor creates the auto-accept engine for a sessions handler.
func NewAutoAcceptor(h *SessionsHandler) *AutoAcceptor {
	a := &AutoAcceptor{
		h:        h,
		kick:     make(chan struct{}, 1),
		now:      time.Now,
		wallNow:  time.Now,
		answered: map[string]autoAcceptAnswer{},
		history:  map[string][]autoAcceptAnswer{},
		skipped:  map[string]map[string]time.Time{},
		lastSkip: map[string]time.Time{},
	}
	h.autoAccept = a
	return a
}

// Kick asks for a check now (a permission notification arrived).
func (a *AutoAcceptor) Kick() {
	if a == nil {
		return
	}
	select {
	case a.kick <- struct{}{}:
	default:
	}
}

// Run checks auto-accept sessions every 2 s and on each kick until ctx ends.
// Call Start first.
func (a *AutoAcceptor) Run(ctx context.Context) {
	t := time.NewTicker(autoAcceptInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-a.kick:
		}
		a.RunOnce(ctx)
	}
}

// Start runs once when Coral starts, before Run: it ends the Cloud Hub's
// enables left from before this process (they never outlive a restart).
func (a *AutoAcceptor) Start(ctx context.Context) {
	if n, err := a.h.ss.ClearHubAutoAccept(ctx, store.AutoAcceptCoralRestarted); err != nil {
		slog.Warn("auto-accept: ending Coral Hub enables after a restart failed", "error", err)
	} else if n > 0 {
		slog.Info("auto-accept: Coral restarted; turned off Coral Hub enables", "agents", n)
	}
}

// RunOnce ends expired enables, then checks every session with auto-accept on once.
func (a *AutoAcceptor) RunOnce(ctx context.Context) {
	if n, err := a.h.ss.ExpireAutoAccept(ctx, a.now()); err != nil {
		slog.Warn("auto-accept: expiring failed", "error", err)
	} else if n > 0 {
		a.h.kickWS()
	}
	sessions, err := a.h.ss.ListAutoAcceptSessions(ctx)
	if err != nil {
		slog.Warn("auto-accept: listing sessions failed", "error", err)
		return
	}
	for _, s := range sessions {
		if ctx.Err() != nil {
			return
		}
		a.check(ctx, s)
	}
}

func (a *AutoAcceptor) check(ctx context.Context, s store.AutoAcceptState) {
	if !AutoAcceptSupported(s.AgentType) {
		return
	}
	var pending *pendingTool
	if t, ok := a.h.pending.get(s.SessionID, a.now()); ok {
		pending = &t
	}
	if s.AgentType == at.Claude {
		// Claude reports its permission prompts. Only an open request whose
		// latest notification is a permission/approval one is answered: never
		// "needs your input" (an MCP input dialog, a question), whatever the
		// screen shows.
		st := a.h.deriveSingleSessionState(ctx, s.SessionID, 0)
		if !st.NeedsInput || !sessionstate.IsApprovalRequest(st.WaitingSummary) {
			return
		}
	}
	capture := a.capture(ctx, s)
	d := classifyApproval(s.AgentType, capture)
	if !d.Prompt {
		return
	}
	if (s.AgentType == at.Claude || s.AgentType == at.Agy || s.AgentType == at.Antigravity || s.AgentType == at.Gemini) && pending == nil {
		// Claude and Antigravity send PreToolUse before every permission prompt; without it the
		// tool is unknown (the hook failed, or Coral restarted while the prompt
		// was open: the record is kept in memory), so it can be neither checked
		// against the high-risk list nor told apart from a question.
		a.skip(ctx, s, d.FP, "Not auto-accepted: the tool is unknown", nil)
		return
	}
	if pending != nil {
		if why, ok := autoAcceptBlockingTools[pending.ToolName]; ok {
			a.skip(ctx, s, d.FP, "Not auto-accepted: "+why, pending)
			return
		}
	}
	if !d.Approve {
		a.skip(ctx, s, d.FP, "Not auto-accepted: unrecognised prompt", pending)
		return
	}
	if why := autoAcceptRisk(s.AgentType, s.WorkingDir, pending, d, capture); why != "" {
		a.skip(ctx, s, d.FP, "Not auto-accepted (high-risk): "+why, pending)
		return
	}

	now := a.now()
	a.mu.Lock()
	last := a.answered[s.SessionID]
	if last.fp == d.FP && now.Sub(last.at) < autoAcceptDebounce {
		a.mu.Unlock()
		return // answered moments ago; the screen has not caught up yet
	}
	recent := a.history[s.SessionID][:0]
	same := 0
	for _, x := range a.history[s.SessionID] {
		if now.Sub(x.at) < autoAcceptLoopWindow {
			recent = append(recent, x)
			if x.fp == d.FP {
				same++
			}
		}
	}
	a.history[s.SessionID] = recent
	a.mu.Unlock()
	switch {
	case same >= autoAcceptSameLimit:
		a.pause(ctx, s, store.AutoAcceptRepeatLoop, "Auto-accept paused: the same prompt repeated 5 times in a minute")
		return
	case len(recent) >= autoAcceptTotalLimit:
		a.pause(ctx, s, store.AutoAcceptRateCap, "Auto-accept paused: more than 30 approvals in a minute")
		return
	}

	// Re-read the screen: answer only if the same prompt is still open.
	again := classifyApproval(s.AgentType, a.capture(ctx, s))
	if !again.Approve || again.FP != d.FP || again.N != d.N {
		return
	}
	if !a.ownPane(ctx, s) {
		return
	}
	// A digit typed into a bare shell after the CLI exited would run as a command (#3219).
	if automatedInputBlocked(ctx, a.h.terminal, paneName(s), s.AgentType, s.SessionID, "auto-accept answer") {
		return
	}
	// The approval is stamped with the moment the key is sent, not when it is recorded: the
	// agent can act on the key and raise its NEXT prompt before the event is written, and an
	// approval that sorted after that prompt's notification would end it as "answered" too
	// (session state orders events by created_at, then id). Wall-clock time, like every other
	// event, one microsecond (the stamps' precision) before the key: a notification created at
	// the same instant as the read can then never tie with the approval and lose on id.
	sentAt := a.wallNow().Add(-time.Microsecond).UTC().Format(store.ISOFormat)
	// The one automated answer to an open prompt, by the operator's opt-in
	// (#3431): through ptymanager's named exemption, logged in the input debug log.
	if err := ptymanager.AutoAcceptSend(ctx, paneName(s), func(ctx context.Context) error {
		if navigatesByArrows(s.AgentType) {
			ok, err := a.h.selectUnnumbered(ctx, paneName(s), s.AgentType, s.SessionID, again.Screen, d.N)
			if err != nil {
				return err
			}
			if !ok {
				return errors.New("auto-accept selection was not confirmed")
			}
			return nil
		}
		return a.h.terminal.SendRawInput(ctx, terminalinput.Internal, paneName(s), []string{strconv.Itoa(d.N)}, s.AgentType, s.SessionID)
	}); err != nil {
		slog.Warn("auto-accept: sending the answer failed", "session", s.SessionID, "error", err)
		return
	}
	a.mu.Lock()
	a.answered[s.SessionID] = autoAcceptAnswer{fp: d.FP, at: now}
	a.history[s.SessionID] = append(a.history[s.SessionID], autoAcceptAnswer{fp: d.FP, at: now})
	a.mu.Unlock()

	tool, input := approvedWhat(s.AgentType, d, pending, capture)
	summary := "Auto-approved " + tool
	if input != "" {
		summary += ": " + input
	}
	recordAutoAcceptEventAt(ctx, a.h.ts, s.AgentName, s.SessionID, "auto_accept", summary, tool, map[string]any{
		"agent_type": s.AgentType, "option": d.Label, "tool": tool, "input": input,
	}, sentAt)
	a.h.kickWS()
}

// paneName is the session's own terminal (named after its session id). With a
// session id the terminal never falls back to another pane (#3211); every read
// and keystroke still checks the pane it resolves to is this session's, as a
// second guard: a digit must never reach another agent.
func paneName(s store.AutoAcceptState) string { return naming.SessionName(s.AgentType, s.SessionID) }

func (a *AutoAcceptor) ownPane(ctx context.Context, s store.AutoAcceptState) bool {
	pane, err := a.h.terminal.FindSession(ctx, paneName(s), s.AgentType, s.SessionID)
	return err == nil && pane != nil && tmux.IsSessionNameFor(pane.SessionName, s.SessionID)
}

func (a *AutoAcceptor) capture(ctx context.Context, s store.AutoAcceptState) string {
	if !a.ownPane(ctx, s) {
		return ""
	}
	if rs, ok := a.h.terminal.(ptymanager.RenderedScreenReader); ok {
		text, err := rs.RenderedScreen(ctx, paneName(s), s.AgentType, s.SessionID)
		if err == nil && text != "" {
			return text
		}
	}
	text, err := a.h.terminal.CaptureOutput(ctx, paneName(s), 80, s.AgentType, s.SessionID)
	if err != nil {
		return ""
	}
	return text
}

// skip records that auto-accept left a prompt for the user: once per prompt,
// and at most one such event per session per minute.
func (a *AutoAcceptor) skip(ctx context.Context, s store.AutoAcceptState, fp, summary string, pending *pendingTool) {
	now := a.now()
	a.mu.Lock()
	seen := a.skipped[s.SessionID]
	if seen == nil {
		seen = map[string]time.Time{}
		a.skipped[s.SessionID] = seen
	}
	for k, t := range seen {
		if now.Sub(t) >= autoAcceptSkipMemory {
			delete(seen, k)
		}
	}
	_, dup := seen[fp]
	last, hadSkip := a.lastSkip[s.SessionID]
	throttled := hadSkip && now.Sub(last) < autoAcceptSkipEvery
	if !dup && !throttled {
		seen[fp] = now
		a.lastSkip[s.SessionID] = now
	}
	a.mu.Unlock()
	if dup || throttled {
		return // a throttled prompt is recorded on a later sweep if it is still open
	}
	tool := ""
	if pending != nil {
		tool = pending.ToolName
	}
	a.record(ctx, s, "auto_accept_skipped", truncateUTF8(summary, autoAcceptInputMaxLen+60), tool, map[string]any{"agent_type": s.AgentType, "tool": tool})
}

// pause turns auto-accept off with a paused code, records it and tells open dashboards.
func (a *AutoAcceptor) pause(ctx context.Context, s store.AutoAcceptState, code, summary string) {
	if err := a.h.ss.PauseAutoAccept(ctx, s.SessionID, code); err != nil {
		slog.Warn("auto-accept: pausing failed", "session", s.SessionID, "error", err)
		return
	}
	a.forget(s.SessionID)
	a.record(ctx, s, "auto_accept_paused", summary, "", map[string]any{"agent_type": s.AgentType, "code": code})
	if a.h.notifications != nil {
		a.h.notifications.Push(Notification{Title: "Auto-accept paused for " + s.AgentName, Message: summary, Level: "warning"})
	}
	a.h.kickWS()
}

func (a *AutoAcceptor) record(ctx context.Context, s store.AutoAcceptState, eventType, summary, tool string, detail map[string]any) {
	recordAutoAcceptEvent(ctx, a.h.ts, s.AgentName, s.SessionID, eventType, summary, tool, detail)
}

func recordAutoAcceptEvent(ctx context.Context, ts *store.TaskStore, agentName, sessionID, eventType, summary, tool string, detail map[string]any) {
	recordAutoAcceptEventAt(ctx, ts, agentName, sessionID, eventType, summary, tool, detail, "")
}

// recordAutoAcceptEventAt records an event created at createdAt (store.ISOFormat), or now when it is empty.
func recordAutoAcceptEventAt(ctx context.Context, ts *store.TaskStore, agentName, sessionID, eventType, summary, tool string, detail map[string]any, createdAt string) {
	ev := &store.AgentEvent{AgentName: agentName, EventType: eventType, Summary: summary, CreatedAt: createdAt}
	ev.SessionID = &sessionID
	if tool != "" {
		ev.ToolName = &tool
	}
	if b, err := json.Marshal(detail); err == nil {
		dj := string(b)
		ev.DetailJSON = &dj
	}
	if _, err := ts.InsertAgentEvent(ctx, ev); err != nil {
		slog.Warn("auto-accept: recording event failed", "session", sessionID, "type", eventType, "error", err)
	}
}

// forget drops a session's debounce, loop-guard and skip history.
func (a *AutoAcceptor) forget(sessionID string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.answered, sessionID)
	delete(a.history, sessionID)
	delete(a.skipped, sessionID)
	delete(a.lastSkip, sessionID)
}

// approvedWhat names the approved tool and its input for the activity log:
// from the pending tool (PreToolUse hook) when there is one, else the screen.
// The input is capped (it reaches the browser in the event).
func approvedWhat(agentType string, d approvalDecision, pending *pendingTool, capture string) (string, string) {
	if pending != nil {
		for _, k := range []string{"command", "file_path", "notebook_path", "path", "url", "query", "pattern", "CommandLine", "TargetFile", "AbsolutePath"} {
			if v, ok := pending.Input[k].(string); ok && strings.TrimSpace(v) != "" {
				return pending.ToolName, trimInput(v)
			}
		}
		return pending.ToolName, ""
	}
	tool, input := screenAction(agentType, d, capture)
	if tool == "" {
		tool = "tool"
	}
	return tool, trimInput(strings.ReplaceAll(input, "\x00", " "))
}

func trimInput(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	return truncateUTF8(s, autoAcceptInputMaxLen)
}

// ── API ────────────────────────────────────────────────────────────────────

// autoAcceptFields are the auto-accept fields of a live session payload:
// always all seven, false/null included, and codes only (no prompt text).
func autoAcceptFields(agentType string, st store.AutoAcceptState) map[string]any {
	var until, source, unsupported, paused, pausedAt any
	if st.Enabled && st.Until.Valid {
		until = st.Until.Int64
	}
	if st.Enabled && st.Source.Valid {
		source = st.Source.String
	}
	supported := AutoAcceptSupported(agentType)
	if !supported {
		unsupported = autoAcceptUnsupported
	}
	if st.PausedCode.Valid {
		paused = st.PausedCode.String
		if st.PausedAt.Valid {
			pausedAt = st.PausedAt.Int64 // when: a client shows each pause once (#4521)
		}
	}
	return map[string]any{
		"auto_accept":                  st.Enabled,
		"auto_accept_until":            until,
		"auto_accept_source":           source,
		"auto_accept_supported":        supported,
		"auto_accept_unsupported_code": unsupported,
		"auto_accept_paused_code":      paused,
		"auto_accept_paused_at":        pausedAt,
	}
}

// autoAcceptPayload is autoAcceptFields for one session read from the store.
func (h *SessionsHandler) autoAcceptPayload(ctx context.Context, sessionID string) map[string]any {
	st, _ := h.ss.GetAutoAcceptState(ctx, sessionID)
	agentType := st.AgentType
	if agentType == "" {
		if ls, err := h.ss.GetLiveSession(ctx, sessionID); err == nil && ls != nil {
			agentType = ls.AgentType
		}
	}
	return autoAcceptFields(agentType, st)
}

// dashboardBrowser reports whether r is a dashboard in a browser: the local
// dashboard (localDashboard), or a remote one (the LAN address, or the phone via
// the QR link) that logged in with a session cookie, as a same-origin fetch. An
// API key (a script, the self-hosted hub's Bearer proxy) and a tunnel stamp
// never qualify. A remote browser with a session already has full control of
// the agents (keys, answer-prompt), so this widens nothing.
func dashboardBrowser(r *http.Request) bool {
	if viaTunnel(r) || hasCredentialQuery(r) ||
		strings.HasPrefix(strings.ToLower(strings.TrimSpace(r.Header.Get("Authorization"))), "bearer ") {
		return false
	}
	if auth.MethodFrom(r.Context()) == auth.MethodSession {
		return sameOriginFetch(r)
	}
	return localDashboard(r)
}

// Headers that identify an agent's own tooling (coral-agent, coral-board and
// hooks may send them). A request carrying any of them never enables auto-accept.
var agentIdentityHeaders = []string{"X-Coral-Agent", "X-Coral-Subscriber-ID", "X-Coral-Session", "X-Coral-Session-ID"}

func refuseAutoAccept(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": msg, "code": code})
}

// autoAcceptTarget finds the session a request names: session_id in the body,
// with the path segment its agent name or its session id.
func (h *SessionsHandler) autoAcceptTarget(w http.ResponseWriter, r *http.Request, sessionID string) *store.LiveSession {
	name := chi.URLParam(r, "name")
	if sessionID == "" {
		errBadRequest(w, "session_id is required")
		return nil
	}
	ls, err := h.ss.GetLiveSession(r.Context(), sessionID)
	if err != nil {
		errInternalServer(w, err.Error())
		return nil
	}
	if ls == nil || (ls.AgentName != name && ls.SessionID != name && naming.SessionName(ls.AgentType, ls.SessionID) != name) {
		errNotFound(w, "session not found")
		return nil
	}
	return ls
}

// EnableAutoAccept turns auto-accept on for one agent.
// POST /api/sessions/live/{name}/auto-accept/enable {"session_id","acknowledged":true,"duration_s"?}
//
// Who may: a dashboard in a browser (a same-origin fetch from this computer, or
// from a browser logged in with a session cookie; see dashboardBrowser) or the Cloud Hub through this process's connector (the
// stamp must match the per-process secret: cloud.TunneledAuthentic), never a
// request carrying an agent's identity. acknowledged:true is sent by the
// dashboard only after the operator confirmed the warning.
//
// Limit (documented): a local process can forge Sec-Fetch-Site as easily as a
// browser sends it, and an agent can type into its own pane anyway. These checks
// keep every Coral agent API path and honest client from turning it on; they do
// not stand against a hostile process on the same computer. The stamp, being a
// secret, cannot be forged by one.
func (h *SessionsHandler) EnableAutoAccept(w http.ResponseWriter, r *http.Request) {
	began := h.autoAcceptStops.Load() // a disable-all after this point ends this enable before it turns anything on
	var body struct {
		SessionID    string `json:"session_id"`
		Acknowledged bool   `json:"acknowledged"`
		DurationS    *int64 `json:"duration_s"`
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	if !body.Acknowledged {
		errBadRequest(w, "acknowledged:true is required to enable auto-accept (the warning must be confirmed)")
		return
	}
	if body.DurationS != nil && (*body.DurationS < 1 || time.Duration(*body.DurationS)*time.Second > autoAcceptMaxLimit) {
		errBadRequest(w, "duration_s must be between 1 and 28800 (8 hours)")
		return
	}
	for _, hd := range agentIdentityHeaders {
		if r.Header.Get(hd) != "" {
			refuseAutoAccept(w, http.StatusForbidden, "agent_identity", "Auto-accept is turned on by the user in the dashboard, not by an agent")
			return
		}
	}
	hub := cloud.TunneledAuthentic(r)
	if !hub && !dashboardBrowser(r) {
		refuseAutoAccept(w, http.StatusForbidden, "no_dashboard_provenance", "Auto-accept can only be turned on from the Coral dashboard or the Coral Hub")
		return
	}
	if hub {
		settings, err := h.ss.GetSettings(r.Context())
		if err != nil {
			errInternalServer(w, err.Error())
			return
		}
		if !HubAutoAcceptAllowed(settings) {
			refuseAutoAccept(w, http.StatusForbidden, "hub_auto_accept_disabled", "Coral Hub auto-accept is turned off on this computer")
			return
		}
	}
	ls := h.autoAcceptTarget(w, r, body.SessionID)
	if ls == nil {
		return
	}
	if !AutoAcceptSupported(ls.AgentType) {
		refuseAutoAccept(w, http.StatusConflict, autoAcceptUnsupported, "Auto-accept is not available for this agent type yet")
		return
	}
	if ls.IsSleeping == 1 {
		refuseAutoAccept(w, http.StatusConflict, "asleep", "The agent is asleep; wake it first")
		return
	}
	source, origin := store.AutoAcceptSourceLocal, "the dashboard"
	var limit time.Duration
	if body.DurationS != nil {
		limit = time.Duration(*body.DurationS) * time.Second
	}
	if hub {
		source, origin = store.AutoAcceptSourceHub, "the Coral Hub"
		if limit == 0 {
			limit = autoAcceptHubDefault
		}
	}
	var until int64
	if limit > 0 {
		until = h.autoAcceptNow().Add(limit).Unix()
	}
	if f := h.onAutoEnableDeciding; f != nil {
		f()
	}
	found, stopped, err := h.enableUnlessStopped(r.Context(), began, ls.SessionID, source, until)
	if stopped {
		refuseAutoAccept(w, http.StatusConflict, "stopped_meanwhile", "Auto-accept was stopped while this request was being handled; turn it on again if you still want it")
		return
	}
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	if !found {
		errNotFound(w, "session not found")
		return
	}
	h.autoAccept.forget(ls.SessionID)
	detail := map[string]any{"source": source, "until": nil}
	if until > 0 {
		detail["until"] = until
	}
	recordAutoAcceptEvent(r.Context(), h.ts, ls.AgentName, ls.SessionID, "auto_accept_enabled", "Auto-accept turned on from "+origin, "", detail)
	h.autoAccept.Kick() // a prompt already open is answered now
	defer h.kickWS()
	writeJSON(w, http.StatusOK, h.autoAcceptPayload(r.Context(), ls.SessionID))
}

// enableUnlessStopped writes the enable unless a disable-all ran since the request began (began is the count it saw). The check and the write are one step under the
// lock that disable-all also takes, so an enable is ordered wholly before a disable-all (which then ends it) or wholly after it (a new request, which began after).
func (h *SessionsHandler) enableUnlessStopped(ctx context.Context, began uint64, sessionID, source string, until int64) (found, stopped bool, err error) {
	h.autoAcceptOrder.Lock()
	defer h.autoAcceptOrder.Unlock()
	if h.autoAcceptStops.Load() != began {
		return false, true, nil
	}
	found, err = h.ss.EnableAutoAccept(ctx, sessionID, source, until)
	return found, false, err
}

func (h *SessionsHandler) autoAcceptNow() time.Time {
	if h.autoAccept != nil {
		return h.autoAccept.now()
	}
	return time.Now()
}

// DisableAutoAccept turns auto-accept off for one agent. Always allowed.
// POST /api/sessions/live/{name}/auto-accept/disable {"session_id"}
func (h *SessionsHandler) DisableAutoAccept(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SessionID string `json:"session_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	ls := h.autoAcceptTarget(w, r, body.SessionID)
	if ls == nil {
		return
	}
	before, err := h.ss.GetAutoAcceptState(r.Context(), ls.SessionID)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	// A Stop always wins (Security on #4468): under the order an enable's write also takes, and it bumps
	// the count an enable reads when it begins, so an enable still deciding ends without turning the run on.
	// The count is shared with disable-all: an enable for another agent at that instant is refused too,
	// and its person turns it on again.
	h.autoAcceptOrder.Lock()
	_, err = h.ss.DisableAutoAccept(r.Context(), ls.SessionID)
	h.autoAcceptStops.Add(1) // also on an error, as disable-all does
	h.autoAcceptOrder.Unlock()
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	h.autoAccept.forget(ls.SessionID)
	if before.Enabled {
		origin := "the dashboard"
		if cloud.TunneledAuthentic(r) {
			origin = "the Coral Hub"
		} else if !localDashboard(r) {
			origin = "a local client"
		}
		recordAutoAcceptEvent(r.Context(), h.ts, ls.AgentName, ls.SessionID, "auto_accept_disabled", "Auto-accept turned off from "+origin, "", map[string]any{})
	}
	defer h.kickWS()
	writeJSON(w, http.StatusOK, h.autoAcceptPayload(r.Context(), ls.SessionID))
}

// DisableAllAutoAccept turns auto-accept off for every agent ("Stop all
// auto-accept"; also `coral auto-accept off --all`). Always allowed.
// POST /api/auto-accept/disable-all -> {"stopped": N}
func (h *SessionsHandler) DisableAllAutoAccept(w http.ResponseWriter, r *http.Request) {
	h.autoAcceptOrder.Lock()
	n, err := h.ss.ClearAllAutoAccept(r.Context())
	h.autoAcceptStops.Add(1) // also on an error: an enable that began before this must not slip past a stop that may have half run
	h.autoAcceptOrder.Unlock()
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	if h.autoAccept != nil {
		h.autoAccept.mu.Lock()
		h.autoAccept.answered = map[string]autoAcceptAnswer{}
		h.autoAccept.history = map[string][]autoAcceptAnswer{}
		h.autoAccept.mu.Unlock()
	}
	defer h.kickWS()
	writeJSON(w, http.StatusOK, map[string]int{"stopped": n})
}
