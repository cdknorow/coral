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

	"github.com/subgentic/coral/internal/agentmodels"
	at "github.com/subgentic/coral/internal/agenttypes"
	"github.com/subgentic/coral/internal/jsonl"
	"github.com/subgentic/coral/internal/proxy"
	"github.com/subgentic/coral/internal/safego"
	"github.com/subgentic/coral/internal/store"
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
	seq        int64              // which switch this is: a switch that was replaced no longer changes the state (#6219)
	cancel     context.CancelFunc // ends this switch's goroutine when a newer choice replaces it
}

// modelSwitchTracker holds the newest switch of each session. A finished one is kept for switchKeep so every open dashboard sees
// its result, then forgotten.
type modelSwitchTracker struct {
	mu   sync.Mutex
	m    map[string]*modelSwitch
	seq  int64
	runs sync.Map // session id -> *sync.Mutex: one switch types at a time, a replaced one finishes before the next starts
}

// runLock is the session's lock for typing a switch's commands: a new choice waits for the one it replaced to stop, so two are never typed at once.
func (t *modelSwitchTracker) runLock(sessionID string) *sync.Mutex {
	m, _ := t.runs.LoadOrStore(sessionID, &sync.Mutex{})
	return m.(*sync.Mutex)
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

// begin records a new pending switch. A switch that is still pending is replaced by it, not refused: the newest choice is what the person wants, and the one before is told to stop (#6219).
func (t *modelSwitchTracker) begin(sessionID string, sw *modelSwitch) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.m == nil {
		t.m = map[string]*modelSwitch{}
	}
	if cur := t.m[sessionID]; cur != nil && cur.State == switchPending && cur.cancel != nil {
		cur.cancel()
	}
	t.seq++
	sw.seq = t.seq
	sw.updated = time.Now()
	t.m[sessionID] = sw
}

// update changes switch seq's state; it does nothing when that switch has been replaced by a newer one.
func (t *modelSwitchTracker) update(sessionID string, seq int64, f func(*modelSwitch)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if sw := t.m[sessionID]; sw != nil && sw.seq == seq {
		f(sw)
		sw.updated = time.Now()
	}
}

// end stops the session's pending switch, if any, and says why in its state (the agent was put to sleep, killed or restarted before Claude confirmed it): never left pending (#6219).
func (t *modelSwitchTracker) end(sessionID, message string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if sw := t.m[sessionID]; sw != nil && sw.State == switchPending {
		if sw.cancel != nil {
			sw.cancel()
		}
		sw.State, sw.Message, sw.updated = switchFailed, message, time.Now()
	}
}

// current reports whether switch seq is still the session's newest.
func (t *modelSwitchTracker) current(sessionID string, seq int64) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	sw := t.m[sessionID]
	return sw != nil && sw.seq == seq
}

// switchTimings are the waits of a switch; tests shorten them. A switch runs in a goroutine that can outlive the test that started it
// while the next test sets its own waits, so they are held behind an atomic pointer and each function reads them once (#5148: the race
// detector caught a bare variable).
type switchTimings struct {
	poll, idleWait, modelConfirm, levelConfirm time.Duration
}

var switchTiming atomic.Pointer[switchTimings]

func init() {
	switchTiming.Store(&switchTimings{poll: 500 * time.Millisecond, idleWait: 10 * time.Minute, modelConfirm: 30 * time.Second, levelConfirm: 30 * time.Second})
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
	tm := switchTiming.Load()
	ctx, cancel := context.WithTimeout(context.Background(), tm.idleWait+tm.modelConfirm+tm.levelConfirm+time.Minute)
	sw.cancel = cancel
	h.modelSwitches.begin(ls.SessionID, sw) // a choice made while another is pending replaces it
	seq := sw.seq
	_, busy := h.latestEvent(r.Context(), ls.SessionID)
	if busy {
		h.modelSwitches.update(ls.SessionID, seq, func(s *modelSwitch) { s.AfterReply = true })
	}
	started := *sw
	started.AfterReply = busy
	workDir := ls.WorkingDir
	safego.Go("server/routes/model_switch", func() {
		defer cancel()
		h.runModelSwitch(ctx, seq, name, agentType, ls.SessionID, workDir, body.Model != nil, model, body.Thinking != nil, level)
	})
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "switch": started})
}

// switchFail ends a switch as failed, with a message the operator can read.
func (h *SessionsHandler) switchFail(sessionID string, seq int64, message string) {
	h.modelSwitches.update(sessionID, seq, func(s *modelSwitch) {
		if s.State == switchPending { // a switch ended with its own reason (Sleep, Kill, Restart) keeps it
			s.State, s.Message = switchFailed, message
		}
	})
}

var errorOutputR = regexp.MustCompile(`(?i)\b(unknown|invalid|not found|not available|unsupported|error|failed|cannot|can't|unable)\b`)

// switchGone says why a switch can no longer finish: its agent was killed (no row), or put to sleep (no terminal). "" while the agent is there. Read at each wait, so Sleep, Sleep all, Kill and a
// restart that replaced the agent end the switch within a poll, whichever route did it.
func (h *SessionsHandler) switchGone(ctx context.Context, sessionID string) string {
	ls, err := h.ss.GetLiveSession(ctx, sessionID)
	switch {
	case err != nil || ls == nil:
		return "The agent was ended or restarted before the change was confirmed."
	case ls.IsSleeping == 1:
		return "The agent was put to sleep before the change was confirmed."
	}
	return ""
}

// runModelSwitch waits for the agent to be between turns, types the commands and watches the transcript for their result.
func (h *SessionsHandler) runModelSwitch(ctx context.Context, seq int64, name, agentType, sessionID, workDir string, setModel bool, model string, setLevel bool, level string) {
	tm := switchTiming.Load()
	// One switch types at a time: a newer choice cancelled the one before it, which stops at its next wait, and this one starts when it has.
	lock := h.modelSwitches.runLock(sessionID)
	lock.Lock()
	defer lock.Unlock()
	if !h.modelSwitches.current(sessionID, seq) {
		return // replaced before it started: nothing was typed for it
	}

	// A turn in progress: the change applies after the reply, never in the middle of it.
	idleBy := time.Now().Add(tm.idleWait)
	for {
		if _, busy := h.latestEvent(ctx, sessionID); !busy {
			break
		}
		if msg := h.switchGone(ctx, sessionID); msg != "" {
			h.switchFail(sessionID, seq, msg)
			return
		}
		if time.Now().After(idleBy) {
			h.switchFail(sessionID, seq, "The agent kept working, so the change was not made.")
			return
		}
		if sleepCtx(ctx, tm.poll) != nil {
			h.switchFail(sessionID, seq, "The change was cancelled.")
			return
		}
	}

	if ctx.Err() != nil { // replaced while it waited (the check above reads a cancelled context as "not busy"): nothing is typed for it
		h.switchFail(sessionID, seq, "The change was cancelled.")
		return
	}
	sent := time.Now().Add(-time.Second) // the transcript's own clock may differ a little
	if setModel {
		if err := h.deliverHumanSend(ctx, name, agentType, sessionID, "/model "+model); err != nil {
			h.switchFail(sessionID, seq, "Could not type /model into the agent: "+err.Error())
			return
		}
		if msg := h.confirmModel(ctx, sessionID, workDir, model, sent); msg != "" {
			h.switchFail(sessionID, seq, msg)
			return
		}
		_ = h.ss.UpdateContextWindow(ctx, sessionID, proxy.LookupContextWindow(model), model)
	}
	if setLevel {
		cmd := "/effort " + level
		if level == "" {
			cmd = "/effort auto"
		}
		if ctx.Err() != nil {
			h.switchFail(sessionID, seq, "The change was cancelled.")
			return
		}
		sent = time.Now().Add(-time.Second)
		if err := h.deliverHumanSend(ctx, name, agentType, sessionID, cmd); err != nil {
			h.switchFail(sessionID, seq, "Could not type /effort into the agent: "+err.Error())
			return
		}
		if msg := h.confirmLevel(ctx, sessionID, workDir, level, sent); msg != "" {
			h.switchFail(sessionID, seq, msg)
			return
		}
		_ = h.ss.SetSessionThinking(ctx, sessionID, level)
		if level == "" { // Default is a choice too: the --effort the agent was launched with must not come back on a wake (#6219)
			if ls, err := h.ss.GetLiveSession(ctx, sessionID); err == nil && ls != nil {
				_ = h.ss.SetSessionFlags(ctx, sessionID, agentmodels.StripThinkingFlags(store.UnmarshalFlags(ls.Flags)))
			}
		}
	}
	h.modelSwitches.update(sessionID, seq, func(s *modelSwitch) { s.State = switchDone })
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
	other := ""
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
				// Another command's answer (a replaced switch's late output, a picker choice, the operator's own): keep waiting for ours; the deadline names it if ours never comes.
				other = "Claude Code ran /model for " + shownArgs(c.Args) + ", not " + model + ": " + c.Output
				continue
			}
			if errorOutputR.MatchString(c.Output) || !setOutputR.MatchString(c.Output) {
				return "Claude Code did not take the model: " + c.Output
			}
			return ""
		}
		if msg := h.switchGone(ctx, sessionID); msg != "" {
			return msg
		}
		if time.Now().After(deadline) {
			if other != "" {
				return other
			}
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

// effortSetR is what Claude Code prints when it took a level: "Set effort level to low (saved as your default for new sessions)", or "Effort level set to auto" for the default.
var effortSetR = regexp.MustCompile(`(?i)(?:set effort level to|effort level set to)\s+([a-z]+)`)

// confirmLevel waits for Claude Code's own entry for /effort, the way confirmModel waits for /model (#6219): its output says "Set effort level to <level>" when it took
// the level, so the switch is done as soon as the CLI says so, not at the next reply (which may be minutes away and held every other choice meanwhile). A refusal ends it
// at once, and a level other than the one asked for is a failure with what Claude Code did. The next reply's recorded level still confirms it, for a transcript that has
// no /effort entry. "" (the CLI's own default) has no level to compare, so any /effort entry that is no refusal confirms it. With neither after levelConfirm the switch fails
// with a message, instead of staying pending.
func (h *SessionsHandler) confirmLevel(ctx context.Context, sessionID, workDir, level string, since time.Time) string {
	tm := switchTiming.Load()
	deadline := time.Now().Add(tm.levelConfirm)
	for {
		if h.jsonl != nil {
			for _, c := range h.jsonl.ReadClaudeLocalCommands(sessionID, workDir, since) {
				if c.Name != "/effort" || c.Output == "" {
					continue
				}
				// Only the entry for the level asked for: a choice that was replaced a moment ago (High, then Extra high) has its own entry, which is not this one's answer (#6219).
				if (level != "" && !strings.EqualFold(c.Args, level)) || (level == "" && c.Args != "" && !strings.EqualFold(c.Args, "auto")) {
					continue
				}
				if m := effortSetR.FindStringSubmatch(c.Output); m != nil { // before the refusal words: the level's own description ("on supported models") is not an error
					if level == "" || strings.EqualFold(m[1], level) {
						return ""
					}
					return "Claude Code set the level to " + m[1] + ", not " + level + "."
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
		if msg := h.switchGone(ctx, sessionID); msg != "" {
			return msg
		}
		if time.Now().After(deadline) {
			return "Claude Code did not confirm the level change."
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
