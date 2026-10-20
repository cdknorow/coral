package routes

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/cdknorow/coral/internal/agentmodels"
	at "github.com/cdknorow/coral/internal/agenttypes"
	"github.com/cdknorow/coral/internal/jsonl"
	"github.com/cdknorow/coral/internal/proxy"
	"github.com/cdknorow/coral/internal/safego"
)

// The live switch of an agent's model and thinking level (#4969): the operator picks a model or level in the chat bar and Coral
// types the CLI's own slash command into the agent's terminal, once the agent is not in the middle of a turn. Only Claude Code has
// a live switch checked so far (/model <model>, /effort <level>); the other types answer 409 needs_restart and the dashboard
// offers "Apply with restart". The agent's stored model and level change only when the transcript confirms the switch, so a later
// restart starts with what the agent really runs.

// liveSwitchTypes are the agent types whose CLI takes /model and /effort in a running session.
var liveSwitchTypes = map[string]bool{at.Claude: true}

// Switch states, as the row's model_switch says them.
const (
	switchPending = "pending"
	switchDone    = "done"
	switchFailed  = "failed"
)

// modelSwitch is one switch's state: what was asked for, how far it got, and why it failed.
type modelSwitch struct {
	State      string  `json:"state"`
	Model      *string `json:"model,omitempty"`
	Thinking   *string `json:"thinking,omitempty"` // "" asks for the CLI's default
	AfterReply bool    `json:"after_reply,omitempty"`
	Message    string  `json:"message,omitempty"`
	updated    time.Time
}

// modelSwitchTracker holds the newest switch of each session. A finished one is kept for switchKeep so every open dashboard sees
// its result, then forgotten.
type modelSwitchTracker struct {
	mu sync.Mutex
	m  map[string]*modelSwitch
}

const switchKeep = 2 * time.Minute

func (t *modelSwitchTracker) get(sessionID string) *modelSwitch {
	t.mu.Lock()
	defer t.mu.Unlock()
	sw := t.m[sessionID]
	if sw == nil {
		return nil
	}
	if sw.State != switchPending && time.Since(sw.updated) > switchKeep {
		delete(t.m, sessionID)
		return nil
	}
	cp := *sw
	return &cp
}

// begin records a new pending switch, or reports false when one is already pending for the session.
func (t *modelSwitchTracker) begin(sessionID string, sw *modelSwitch) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.m == nil {
		t.m = map[string]*modelSwitch{}
	}
	if cur := t.m[sessionID]; cur != nil && cur.State == switchPending {
		return false
	}
	sw.updated = time.Now()
	t.m[sessionID] = sw
	return true
}

func (t *modelSwitchTracker) update(sessionID string, f func(*modelSwitch)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if sw := t.m[sessionID]; sw != nil {
		f(sw)
		sw.updated = time.Now()
	}
}

// switchTimings are the waits of a switch; tests shorten them. A switch runs in a goroutine that can outlive the test that started it
// while the next test sets its own waits, so they are held behind an atomic pointer and each function reads them once (#5148: the race
// detector caught a bare variable).
type switchTimings struct {
	poll, idleWait, modelConfirm, levelConfirm time.Duration
}

var switchTiming atomic.Pointer[switchTimings]

func init() {
	switchTiming.Store(&switchTimings{poll: 500 * time.Millisecond, idleWait: 10 * time.Minute, modelConfirm: 30 * time.Second, levelConfirm: 30 * time.Minute})
}

// modelSwitchRequest is the switch's body: a model, a thinking level, or both. "thinking": "" asks for the CLI's own default
// (/effort auto).
type modelSwitchRequest struct {
	SessionID string  `json:"session_id"`
	AgentType string  `json:"agent_type"`
	Model     *string `json:"model"`
	Thinking  *string `json:"thinking"`
}

// SwitchModel starts a live switch of the agent's model and/or thinking level.
// POST /api/sessions/live/{name}/model
func (h *SessionsHandler) SwitchModel(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	var body modelSwitchRequest
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	if !humanInputCaller(r) {
		writeError( // a person's choice: an agent or an API key does not change what another agent runs
			w, http.StatusForbidden, "operator_only", "only the operator can switch an agent's model")
		return
	}
	ls, status, message := h.validateExactLiveTarget(r.Context(), name, body.AgentType, body.SessionID, false)
	if status != 0 {
		writeExactTargetError(w, status, message)
		return
	}
	agentType := ls.AgentType
	if agentType == "" {
		agentType = body.AgentType
	}
	if body.Model == nil && body.Thinking == nil {
		errBadRequest(w, "model or thinking is required")
		return
	}
	if !liveSwitchTypes[agentType] {
		writeError(w, http.StatusConflict, "needs_restart", "a "+agentType+" agent cannot switch while it runs: apply it with a restart")
		return
	}
	var model string
	if body.Model != nil {
		model = strings.TrimSpace(*body.Model)
		if model == "" || !agentmodels.ValidModelValue(model) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": errInvalidModel, "field": "model"})
			return
		}
		if err := validateAgentModel(agentType, model); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error(), "field": "model"})
			return
		}
	}
	var level string
	if body.Thinking != nil {
		level = strings.TrimSpace(*body.Thinking)
		if !agentmodels.ValidThinking(agentType, level) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": errInvalidThinking(agentType), "field": "thinking"})
			return
		}
	}
	sw := &modelSwitch{State: switchPending}
	if body.Model != nil {
		sw.Model = &model
	}
	if body.Thinking != nil {
		sw.Thinking = &level
	}
	if !h.modelSwitches.begin(ls.SessionID, sw) {
		writeError(w, http.StatusConflict, "switch_in_progress", "a switch is already in progress for this agent")
		return
	}
	_, busy := h.latestEvent(r.Context(), ls.SessionID)
	if busy {
		h.modelSwitches.update(ls.SessionID, func(s *modelSwitch) { s.AfterReply = true })
	}
	started := *sw
	started.AfterReply = busy
	workDir := ls.WorkingDir
	safego.Go("server/routes/model_switch", func() {
		h.runModelSwitch(name, agentType, ls.SessionID, workDir, body.Model != nil, model, body.Thinking != nil, level)
	})
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "switch": started})
}

// switchFail ends a switch as failed, with a message the operator can read.
func (h *SessionsHandler) switchFail(sessionID, message string) {
	h.modelSwitches.update(sessionID, func(s *modelSwitch) { s.State, s.Message = switchFailed, message })
}

var errorOutputR = regexp.MustCompile(`(?i)\b(unknown|invalid|not found|not available|unsupported|error|failed|cannot|can't|unable)\b`)

// runModelSwitch waits for the agent to be between turns, types the commands and watches the transcript for their result.
func (h *SessionsHandler) runModelSwitch(name, agentType, sessionID, workDir string, setModel bool, model string, setLevel bool, level string) {
	tm := switchTiming.Load()
	ctx, cancel := context.WithTimeout(context.Background(), tm.idleWait+tm.modelConfirm+tm.levelConfirm+time.Minute)
	defer cancel()

	// A turn in progress: the change applies after the reply, never in the middle of it.
	idleBy := time.Now().Add(tm.idleWait)
	for {
		if _, busy := h.latestEvent(ctx, sessionID); !busy {
			break
		}
		if time.Now().After(idleBy) {
			h.switchFail(sessionID, "The agent kept working, so the change was not made.")
			return
		}
		if sleepCtx(ctx, tm.poll) != nil {
			h.switchFail(sessionID, "The change was cancelled.")
			return
		}
	}

	sent := time.Now().Add(-time.Second) // the transcript's own clock may differ a little
	if setModel {
		if err := h.deliverHumanSend(ctx, name, agentType, sessionID, "/model "+model); err != nil {
			h.switchFail(sessionID, "Could not type /model into the agent: "+err.Error())
			return
		}
		if msg := h.confirmModel(ctx, sessionID, workDir, model, sent); msg != "" {
			h.switchFail(sessionID, msg)
			return
		}
		_ = h.ss.UpdateContextWindow(ctx, sessionID, proxy.LookupContextWindow(model), model)
	}
	if setLevel {
		cmd := "/effort " + level
		if level == "" {
			cmd = "/effort auto"
		}
		sent = time.Now().Add(-time.Second)
		if err := h.deliverHumanSend(ctx, name, agentType, sessionID, cmd); err != nil {
			h.switchFail(sessionID, "Could not type /effort into the agent: "+err.Error())
			return
		}
		if msg := h.confirmLevel(ctx, sessionID, workDir, level, sent); msg != "" {
			h.switchFail(sessionID, msg)
			return
		}
		_ = h.ss.SetSessionThinking(ctx, sessionID, level)
	}
	h.modelSwitches.update(sessionID, func(s *modelSwitch) { s.State = switchDone })
	h.kickWS() // the feed shows the result now, not on the next tick
}

// confirmModel waits for the transcript's /model entry after the command was typed: Claude Code prints what it did when it takes the
// command, not only at the next reply. It confirms the switch only when the entry is for the model that was asked for (its
// arguments are the model) and its output says it was set ("Set model to ..."). "Kept model as ...", the entry of another model
// (a picker choice, a command the operator typed meanwhile) and any other output are a failed switch, with that text, so the stored
// model never changes to one the agent is not running. It returns "" on success, else the reason.
func (h *SessionsHandler) confirmModel(ctx context.Context, sessionID, workDir, model string, since time.Time) string {
	tm := switchTiming.Load()
	deadline := time.Now().Add(tm.modelConfirm)
	for {
		var cmds []jsonl.LocalCommand
		if h.jsonl != nil {
			cmds = h.jsonl.ReadClaudeLocalCommands(sessionID, workDir, since)
		}
		for _, c := range cmds {
			if c.Name != "/model" || c.Output == "" {
				continue
			}
			if !strings.EqualFold(strings.TrimSpace(c.Args), model) {
				return "Claude Code ran /model for " + shownArgs(c.Args) + ", not " + model + ": " + c.Output
			}
			if errorOutputR.MatchString(c.Output) || !setOutputR.MatchString(c.Output) {
				return "Claude Code did not take the model: " + c.Output
			}
			return ""
		}
		if time.Now().After(deadline) {
			return "Claude Code did not confirm the model change."
		}
		if sleepCtx(ctx, tm.poll) != nil {
			return "The change was cancelled."
		}
	}
}

// setOutputR is what Claude Code prints when it set the model: it starts with "Set".
var setOutputR = regexp.MustCompile(`(?i)^\s*set\b`)

func shownArgs(args string) string {
	if strings.TrimSpace(args) == "" {
		return "the model picker"
	}
	return args
}

// confirmLevel waits for the transcript to show the new level: Claude Code records the level a turn ran at on that turn's assistant
// entry, so a level is confirmed by the next reply (the "confirms after the next reply" the chip says). A refusal Claude Code
// prints for /effort ends it at once. "" (the CLI's own default) has no level to compare, so it is confirmed by the /effort entry.
func (h *SessionsHandler) confirmLevel(ctx context.Context, sessionID, workDir, level string, since time.Time) string {
	tm := switchTiming.Load()
	deadline := time.Now().Add(tm.levelConfirm)
	for {
		if h.jsonl != nil {
			for _, c := range h.jsonl.ReadClaudeLocalCommands(sessionID, workDir, since) {
				if c.Name != "/effort" || c.Output == "" {
					continue
				}
				if errorOutputR.MatchString(c.Output) {
					return "Claude Code did not take the level: " + c.Output
				}
				if level == "" {
					return ""
				}
			}
			h.jsonl.ReadClaudeTurnEvent(sessionID, workDir) // the reader follows the transcript's end: the newest assistant entry's level
			if level != "" && h.jsonl.TranscriptThinking(sessionID) == level {
				return ""
			}
		}
		if time.Now().After(deadline) {
			return "The agent has not replied since, so the level is not confirmed yet."
		}
		if sleepCtx(ctx, tm.poll) != nil {
			return "The change was cancelled."
		}
	}
}

// addModelSwitch puts a session's switch state on its row as "model_switch" (#4969).
func (h *SessionsHandler) addModelSwitch(entry map[string]any, sessionID string) {
	if sw := h.modelSwitches.get(sessionID); sw != nil {
		entry["model_switch"] = sw
	}
}
