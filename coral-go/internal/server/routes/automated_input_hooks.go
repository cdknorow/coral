package routes

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/cdknorow/coral/internal/agent"
	at "github.com/cdknorow/coral/internal/agenttypes"
	"github.com/cdknorow/coral/internal/logparse"
	"github.com/cdknorow/coral/internal/ptymanager"
	"github.com/cdknorow/coral/internal/sessionstate"
	"github.com/cdknorow/coral/internal/store"
)

// heldInputSummary is the notification recorded when automated input has been
// held a while because the agent waits on a prompt: it shows the agent as
// needing the user on the dashboard (sessionstate classifies "needs your
// input"), instead of anything being typed into that prompt (#3431).
const heldInputSummary = sessionstate.HeldInputSummary

// heldForInputBoxSummary is recorded instead when a send was held a while for
// another reason: text in the agent's input box (a person's unsent draft), or a
// box not on screen. Nobody's answer is needed, so it is informational: no Needs
// input, toast or attention (#3471). It must match none of sessionstate's phrases.
const heldForInputBoxSummary = "Coral is holding automated messages until the text in this agent's input box is sent or cleared"

// InstallAutomatedInputHooks gives the automated-input gate the agents' state
// and a way to surface a send held too long. The server calls it once.
func (h *SessionsHandler) InstallAutomatedInputHooks() {
	ptymanager.SetNeedsInputFunc(h.gateNeedsInput)
	ptymanager.SetStateFunc(h.gateState)
	ptymanager.SetHeldTooLongFunc(h.recordHeldTooLong)
	ptymanager.SetHeldEndedFunc(h.recordHeldEnded)
	ptymanager.SetSignalsSettingFunc(gateSignalsFromSettings(h.ss))
}

// GateSignalsClaudeSetting turns on gate phase 2 for Claude agents (#3578): automated input
// follows the agent's own signals, so a routine board nudge or message waits for the turn to end
// and a mention is typed in at once. Off unless "true" (the default goes on after the shadow
// week). Set only on this computer: PUT /api/settings is Operator-only and never reachable
// through Coral Hub. CORAL_GATE_MODE=screen overrides it (the kill switch).
const GateSignalsClaudeSetting = "gate_signals_claude"

// gateSignalsFromSettings reads the setting at most once a second.
func gateSignalsFromSettings(ss *store.SessionStore) func() bool {
	var mu sync.Mutex
	var at time.Time
	on := false
	return func() bool {
		mu.Lock()
		defer mu.Unlock()
		if time.Since(at) < time.Second {
			return on
		}
		s, err := ss.GetSettings(context.Background())
		on, at = err == nil && s[GateSignalsClaudeSetting] == "true", time.Now()
		return on
	}
}

// gateNeedsInput tells the gate whether the agent's state says it waits on the
// user. Coral's own held notice doesn't count (#3471): otherwise one notice would
// make every later hold look like a prompt hold and keep the agent at Needs input.
func (h *SessionsHandler) gateNeedsInput(ctx context.Context, name, agentType, sessionID string) bool {
	if sessionID == "" {
		_, sessionID = logparse.ParseSessionName(name)
	}
	if sessionID == "" || h.ts == nil {
		return false
	}
	st := h.deriveSingleSessionState(ctx, sessionID, 0)
	return st.NeedsInput && st.WaitingSummary != heldInputSummary
}

// recordHeldTooLong records, once per send held a while, why on the agent's
// events. Only a hold for an open prompt (prompt) shows the agent as needing the
// user. An agent that is only waiting on its teammates (coral-board wait) and
// whose nudge waits behind its input box needs nobody (#3471).
func (h *SessionsHandler) recordHeldTooLong(name, agentType, sessionID, what, kind string) {
	prompt := kind != "" // a request to the user (the gate's hold kind, #3482)
	if sessionID == "" {
		_, sessionID = logparse.ParseSessionName(name)
	}
	if h.ts == nil || sessionID == "" {
		return
	}
	summary := heldInputSummary
	if !prompt {
		summary = heldForInputBoxSummary
	}
	h.recordHeldNote(name, sessionID, summary)
}

// recordHeldEnded records that the sends reported by recordHeldTooLong were
// delivered or dropped, which clears the held notice's Needs input (#3471): an
// agent with no hook events (agy, Codex) would otherwise keep it until its next
// prompt, though nothing waits any more.
func (h *SessionsHandler) recordHeldEnded(name, agentType, sessionID string) {
	if sessionID == "" {
		_, sessionID = logparse.ParseSessionName(name)
	}
	if h.ts == nil || sessionID == "" {
		return
	}
	h.recordHeldNote(name, sessionID, sessionstate.HeldInputEndedSummary)
}

func (h *SessionsHandler) recordHeldNote(name, sessionID, summary string) {
	agentName := name
	if ls, err := h.ss.GetLiveSession(context.Background(), sessionID); err == nil && ls != nil {
		agentName = ls.AgentName
	}
	sid := sessionID
	if _, err := h.ts.InsertAgentEvent(context.Background(), &store.AgentEvent{
		AgentName: agentName, SessionID: &sid, EventType: "notification", Summary: summary,
	}); err != nil {
		slog.Warn("could not record held automated input", "session", name, "error", err)
	}
}

// gateState gives the gate the agent's state from its own signals (#3481, #3475
// phase 1): its hook events, plus the transcript's end of a turn where a CLI fires no
// hook (a denial, an interrupt). ok is false when the session has sent no signal at all
// (no hooks, or none yet): the gate then keeps its screen rule.
//   - Gone: the session ended (SessionEnd).
//   - Blocked: a request the agent itself made is open (PermissionRequest, Elicitation,
//     its permission notification), with what it waits on. Coral's own held notice is
//     not the agent's request and never blocks. For Claude, a question or plan menu is
//     open from its PreToolUse on (menuKinds, #3655), before any notification.
//   - InTurn: a prompt or a tool event is the latest.
//   - Idle: otherwise (a stop, the session's start).
func (h *SessionsHandler) gateState(ctx context.Context, name, agentType, sessionID string) (ptymanager.AgentState, bool) {
	if sessionID == "" {
		_, sessionID = logparse.ParseSessionName(name)
	}
	if sessionID == "" || h.ts == nil {
		return ptymanager.AgentState{}, false
	}
	if agentType == at.Claude && !h.claudeHasRequestSignals(name, sessionID) {
		// An agent launched with an older hook set (or one not heard from since this server
		// started) has no request or turn signals of its own, so they can't say it is idle:
		// the gate keeps its screen rule for it (#3655 F3; QA: fail-safe).
		return ptymanager.AgentState{}, false
	}
	byID, err := h.ts.GetSessionStateEvents(ctx, []string{sessionID})
	events := byID[sessionID]
	if err != nil || len(events) == 0 {
		return ptymanager.AgentState{}, false
	}
	in := SessionStateInput{}
	for _, ev := range events {
		in.Events = append(in.Events, ev.StateEvent())
	}
	workingDir := ""
	if ls, err := h.ss.GetLiveSession(ctx, sessionID); err == nil && ls != nil {
		workingDir = ls.WorkingDir
		if agentType == "" {
			agentType = ls.AgentType
		}
	}
	h.applyTranscriptState(&in, events, agentType, sessionID, workingDir)
	st := sessionstate.Derive(in)
	cliVersion := h.runningCLIVersion(ctx, agentType, sessionID, workingDir)
	as := ptymanager.AgentState{
		State:      ptymanager.StateIdle,
		Since:      st.Since,
		LastEvent:  st.LastEvent,
		LastStop:   st.LastStop,
		CLIVersion: cliVersion,
	}
	pt, pending := h.pending.get(sessionID, time.Now())
	as.ToolPending = pending
	menu := ""
	if pending && agentType == at.Claude && !st.LastStop.After(pt.At) {
		// A menu's PreToolUse counts until its PostToolUse, a stop, a prompt or a reset clears
		// it; Esc closes one with no hook at all, so an end of turn (the transcript's, too)
		// after it means it is closed.
		menu = menuKinds[pt.ToolName]
	}
	switch {
	case st.Gone:
		as.State = ptymanager.StateGone
	case st.OwnRequest:
		as.State = ptymanager.StateBlocked
		as.Kind = st.WaitingKind
		if as.Kind == "" {
			as.Kind = "permission"
		}
	case menu != "":
		as.State = ptymanager.StateBlocked
		as.Kind = menu
		as.Since = pt.At
	case st.InTurn:
		as.State = ptymanager.StateInTurn
	}
	return as, true
}

// claudeHasRequestSignals reports whether a Claude session's hooks include the request and turn
// signals (revision agent.ClaudeSignalsHooksRev or later), from the revision its events carry.
// Unknown (no revision since this server started) is not: the screen rule decides until one
// comes. Events without a revision, or an older one, mean an older hook set, logged once per
// session: the agent needs a relaunch or resume.
func (h *SessionsHandler) claudeHasRequestSignals(name, sessionID string) bool {
	v, known := h.hooksRev.Load(sessionID)
	rev, _ := v.(int)
	if rev >= agent.ClaudeSignalsHooksRev {
		return true
	}
	if _, noRev := h.hooksNoRev.Load(sessionID); known || noRev {
		if _, logged := oldHookSets.LoadOrStore(sessionID, true); !logged {
			slog.Info("automated input: this agent's hooks are out of date; the gate keeps the screen rule for it until it is relaunched or resumed",
				"session", name, "hooks_rev", rev, "want", agent.ClaudeSignalsHooksRev)
		}
	}
	return false
}

// oldHookSets are the sessions already logged as having an old hook set.
var oldHookSets sync.Map

// menuKinds are Claude's tools that open a menu for the user, by what it asks (#3655). Their
// PreToolUse fires before the menu is drawn, in every hook set Coral has installed, while the
// PermissionRequest hook is missing from agents launched before v1.4.27 and the permission
// notification comes some 6 s after the menu opens.
var menuKinds = map[string]string{"AskUserQuestion": "question", "ExitPlanMode": "plan"}
